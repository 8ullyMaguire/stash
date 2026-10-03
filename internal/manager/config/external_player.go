package config

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// #2747 - an external player, launched by the server, playing a scene on the LAN.
//
// ## WHY A COMMAND TEMPLATE AND NOT A BINARY PATH PLUS FIXED FLAGS
//
// Every player takes its seek flags differently: mpv wants `--start=30`, VLC wants `--start-time
// 30`, Jellyfin's own player wants neither because it speaks HTTP. Hard-coding one player's
// flags would make the feature a mpv feature with a misleading name. So the config is a
// template with placeholders, and the admin supplies the whole invocation.
//
// ## THE PLACEHOLDERS, AND WHY EXACTLY THESE
//
//	{file}      absolute path to the media file
//	{start}     window start, in SECONDS, as a plain number. Empty when the scene is the whole file.
//	{end}       window end, in SECONDS. Empty when open-ended or the whole file.
//	{title}     the scene's title, for players that show one
//
// Seconds are not negotiable, and it is the reason this feature needs #3530 to exist first: a
// scene that is a WINDOW of its file must be handed to the player AS A WINDOW, or the player
// shows the whole file and the scene appears to be the wrong length.
//
// A placeholder that appears in the template but is NOT one of the four above is an ERROR AT
// LAUNCH rather than being passed through literally. A replacer would hand `{startt}` straight
// to the player, which then refuses to open the file -- and that reads as a media problem, so
// the admin goes looking in the wrong place entirely.
//
// ## WHY THE EMPTY STRINGS ARE THE POINT
//
// "This scene is the whole file" and "this scene is a window" must be distinguishable, because
// the first needs no seek flags at all and the second does. Passing `0` for a missing start is
// numerically fine but is a different SIGNAL to a player -- an explicit 0 can mean a full remount
// rather than "no seek". So the whole-file case substitutes the EMPTY STRING and the admin's
// template decides what an empty value means, usually by leaving the flags out of the template
// entirely and using a separate optional token.
//
// ## SECURITY: WHY THE ENVIRONMENT IS SCRUBBED, BY ALLOWLIST
//
// This process is spawned BY THE SERVER, so it inherits the server's privileges AND whatever the
// server was given in its environment -- which for a Stash deployment routinely includes the API
// key, database credentials, and any bearer token. A media player is a long-lived process on a
// machine the admin may well be sharing; it has no business holding the key that can rewrite the
// entire database.
//
// Fixed allowlist rather than "the server's minus the known secrets": a denylist silently leaks
// every secret added to the environment after it was written, which is the normal case over the
// life of a deployment.
//
// ## SECURITY: WHY THE BINARY PATH MUST BE ABSOLUTE AND EXIST
//
// The template is whitespace-split and the first field is exec'd directly, so `mpv {file}` would
// resolve `mpv` through the SERVER's PATH -- the resolved binary depends on the server's launch
// context, not the admin's shell. Requiring an absolute path to an existing regular file removes
// the PATH indirection, and exec'ing argv[0] directly (never through a shell) removes the
// metacharacter question as well.
//
// Never auto-download a player. Stash fetching and executing a binary on the host is a much
// larger decision than a config file, and the admin has to know where that binary came from.
//
// ## NO CREDENTIALS ARE PASSED THROUGH
//
// Deliberately absent, and the absence is the design: the player fetches media over plain HTTP.
// If the instance requires auth, the admin puts a token in the URL template, which means it lives
// in the config file where anyone who can read it can see it. Handing a session cookie to a
// third-party process would be worse. Recorded so the gap reads as a decision, not an oversight.

const (
	ExternalPlayerEnabled     = "external_player.enabled"
	ExternalPlayerCommand     = "external_player.command"
	ExternalPlayerURLTemplate = "external_player.url_template"

	// ExternalPlayerToken authorises a REMOTE player to register and wait (#2747).
	//
	// A separate credential from the session store, deliberately. This endpoint starts programs
	// and hands out media URLs, so the operator must be able to revoke it WITHOUT logging
	// themselves -- and out of every other device -- by editing one config value.
	//
	// There is exactly one token, not one per user. The threat is "a program on the LAN", and a
	// per-user token needs an issuance endpoint and a revocation list to be worth anything; an
	// operator who wants no remote players sets no token, which is the same switch as
	// `enabled`.
	ExternalPlayerToken = "external_player.token"

	// ExternalPlayerBaseURL is the externally reachable root, e.g. https://stash.lan:9999.
	//
	// Needed because the dispatched URLs are fetched by ANOTHER MACHINE, and a URL built from
	// the server's own bind address (`http://127.0.0.1:9999`) resolves on the player's machine
	// to the player's own loopback -- a player that fetches nothing, with no error anywhere.
	ExternalPlayerBaseURL = "external_player.base_url"
)

// externalPlayerEnvAllowlist is the ONLY environment a spawned player receives.
//
// Every key here is something a media player legitimately needs, and none of them is a Stash
// secret. Anything the server has that is not on this list does not reach the child.
var externalPlayerEnvAllowlist = []string{
	"PATH", "HOME", "USER", "DISPLAY", "WAYLAND_DISPLAY", "XAUTHORITY",
	"XDG_RUNTIME_DIR", "XDG_CONFIG_HOME", "XDG_CACHE_HOME", "XDG_DATA_HOME",
	"LANG", "LC_ALL", "TERM",
	"PULSE_SERVER", "PIPEWIRE_REMOTE", "ALSA_CONFIG_PATH",
	"DBUS_SESSION_BUS_ADDRESS",
}

var knownPlayerPlaceholders = []string{"{file}", "{start}", "{end}", "{title}"}

func isKnownPlayerPlaceholder(tok string) bool {
	for _, k := range knownPlayerPlaceholders {
		if tok == k {
			return true
		}
	}
	return false
}

// ExternalPlayerConfig is the resolved, launchable configuration for one scene.
type ExternalPlayerConfig struct {
	// Argv has the placeholders ALREADY SUBSTITUTED and whitespace split into arguments.
	//
	// Splitting here rather than at the exec site is the security property: exec.Command with a
	// []string never involves /bin/sh, so a path containing spaces or a semicolon is an ordinary
	// argument rather than an injection.
	Argv []string

	// BinaryPath is Argv[0], checked absolute and existing.
	BinaryPath string

	// Start and End are the window in seconds; zero means "the whole file".
	//
	// Carried separately from Argv because the caller needs them for the log line and the API
	// response, and recovering them by parsing the substituted command string would be lossy --
	// the template may not even contain {start}.
	Start float64
	End   float64

	// Ranged reports whether this scene is a WINDOW. A scene spanning 0..duration is NOT ranged
	// and must not be reported as one, or the log claims a seek that never happened.
	Ranged bool
}

// playerPlaceholderError names an unrecognised placeholder instead of passing it through.
type playerPlaceholderError struct{ token string }

func (e *playerPlaceholderError) Error() string {
	return "unknown placeholder " + e.token + " in external_player.command " +
		"(known: {file} {start} {end} {title})"
}

// GetExternalPlayerEnabled reports the RAW operator flag.
//
// Deliberately not the resolved readiness: the UI needs to know whether the operator turned the
// feature on -- so it can show the button and grey out unused fields -- which is a different
// question from whether the configuration is complete enough to launch.
func (i *Config) GetExternalPlayerEnabled() bool {
	return i.getBool(ExternalPlayerEnabled)
}

func (i *Config) GetExternalPlayerCommand() string {
	return strings.TrimSpace(i.getString(ExternalPlayerCommand))
}

func (i *Config) GetExternalPlayerURLTemplate() string {
	return strings.TrimSpace(i.getString(ExternalPlayerURLTemplate))
}

// GetExternalPlayerToken is the shared secret a remote player presents to register (#2747).
//
// Trimmed on read, because a token copied out of a config file or a shell often arrives with a
// trailing newline, and comparing it verbatim makes an operator who has done nothing wrong
// unable to connect. The stored value is left alone -- trimming what is stored would silently
// rewrite an operator's secret.
func (i *Config) GetExternalPlayerToken() string {
	return strings.TrimSpace(i.getString(ExternalPlayerToken))
}

// GetExternalPlayerBaseURL is the externally reachable root for dispatched media URLs (#2747).
func (i *Config) GetExternalPlayerBaseURL() string {
	return strings.TrimSpace(i.getString(ExternalPlayerBaseURL))
}

// scrubbedPlayerEnv builds the child's environment from the allowlist.
//
// Reads the SERVER's environment rather than using a literal map, because PATH and DISPLAY come
// from the server's own launch context and a hard-coded PATH would break every player when
// Stash is started by systemd. Everything not on the list is dropped.
func scrubbedPlayerEnv(environ []string) []string {
	keep := make(map[string]bool, len(externalPlayerEnvAllowlist))
	for _, k := range externalPlayerEnvAllowlist {
		keep[k] = true
	}
	out := make([]string, 0, len(environ))
	for _, kv := range environ {
		name, _, ok := strings.Cut(kv, "=")
		if ok && keep[name] {
			out = append(out, kv)
		}
	}
	return out
}

// GetExternalPlayerConfig resolves a launch for one scene.
//
// `environ` is a parameter rather than read from os.Environ inside, so the scrubbing is testable
// without mutating the process environment -- a parallel test calling os.Setenv is a flake
// waiting to happen.
//
// ok=false carries a REASON, because the two failure modes are actionable in different places:
// "you have not configured it" is nothing for the UI to show, while "your command has a typo in
// it" is the single most useful thing the page could say.
func (i *Config) GetExternalPlayerConfig(environ []string, filePath, title string, start, end float64, ranged bool) (cfg ExternalPlayerConfig, ok bool, reason string) {
	if !i.GetExternalPlayerEnabled() {
		return cfg, false, ""
	}

	tmpl := i.GetExternalPlayerCommand()
	if tmpl == "" {
		return cfg, false, "external_player.command is empty"
	}

	argv, err := playerArgv(tmpl, filePath, title, start, end, ranged)
	if err != nil {
		return cfg, false, err.Error()
	}
	if len(argv) == 0 {
		return cfg, false, "external_player.command has no command before its first placeholder"
	}

	bin := argv[0]
	// Absolute, because a bare name would be resolved through the SERVER's PATH rather than the
	// admin's shell -- so which binary runs would depend on how Stash was started.
	if !filepath.IsAbs(bin) {
		return cfg, false, "external_player.command must begin with an absolute path to the " +
			"player, not " + bin + " (a bare name resolves through the server's PATH)"
	}
	// Exists and is a regular file: turns "mpv" or a stale path into a message naming what is
	// not there, instead of an opaque exec failure at launch time.
	if fi, statErr := os.Stat(bin); statErr != nil || !fi.Mode().IsRegular() {
		return cfg, false, "external_player.command: " + bin + " is not an existing file"
	}

	return ExternalPlayerConfig{
		Argv:       argv,
		BinaryPath: bin,
		Start:      start,
		End:        end,
		Ranged:     ranged,
	}, true, ""
}

// Set is a test seam for writing a config key.
//
// Exists because `Config.main` is unexported and every other test in the tree either lives inside
// this package or does not need to set a key. Without it, a test in `internal/api` that exercises
// config-dependent behaviour -- like #2747's launch path -- has no way to configure anything, and
// the only alternative is to move the test into this package and test less.
//
// Deliberately not a general-purpose setter used by production code: nothing outside tests calls
// it, so it cannot become a back door around the typed Get* accessors.
func (i *Config) Set(key string, value interface{}) {
	i.main.Set(key, value)
}

// PlayerEnv returns the environment a spawned player should receive.
func PlayerEnv(environ []string) []string { return scrubbedPlayerEnv(environ) }

// playerArgv substitutes the placeholders and splits the TEMPLATE's arguments, not the
// substituted text.
//
// ## WHY THE SPLIT HAPPENS BEFORE SUBSTITUTION
//
// This is a security property, and the obvious implementation gets it wrong in a way that looks
// correct. Splitting the RESULT with strings.Fields means a substituted value containing spaces
// becomes several argv entries: a scene titled "A; rm -rf /" produces
//
//	[/bin/sh] [/media/a.mp4] [--title=A;] [rm] [-rf] [/]
//
// which is not a title at all -- it is a different invocation. No shell is involved (exec.Command
// with a []string never consults one), so this cannot execute anything by itself, but the player
// is then asked to open five files and a directory where one was meant, and the operator sees a
// player failing for no visible reason.
//
// Splitting the template first means each argument is a self-contained unit and a placeholder is
// substituted INSIDE it, so an embedded space stays inside that one argument.
//
// ## WHY A PLACEHOLDER STANDING ALONE STILL WORKS
//
// `--title={title}` with an empty title yields `--title=`, which is one argument -- not an empty
// string argument, and not four. That is the case strings.Fields gets most wrong, because it
// drops empty results entirely and would silently remove the flag.
func playerArgv(tmpl, filePath, title string, start, end float64, ranged bool) ([]string, error) {
	argv := strings.Fields(tmpl)
	for i, arg := range argv {
		if !strings.Contains(arg, "{") {
			continue
		}
		replaced, err := substituteInArgument(arg, filePath, title, start, end, ranged)
		if err != nil {
			return nil, err
		}
		argv[i] = replaced
	}

	// An argument that substitutes to nothing is dropped. That happens for a placeholder standing
	// ALONE as its own token -- `{start}` with no window -- where there is no seek to make, and
	// an empty argument is worse than none.
	//
	// A flag with an empty VALUE, `--start={start}` -> `--start=`, is KEPT: it is not empty, it
	// is a flag whose value the template supplies as empty. The operator wrote that flag, and
	// dropping it would launch a player that does not do what the config says.
	//
	// This was an `if/else` setting "" and then filtering "" below. The else branch was
	// unreachable-in-effect: the filter drops empty strings either way, so the condition decided
	// nothing. Mutation E4 removed it and every test still passed, which is the signature of code
	// that is not load-bearing rather than of a test that is too weak. Simplified to the filter.
	out := argv[:0]
	for _, a := range argv {
		if a != "" {
			out = append(out, a)
		}
	}
	return out, nil
}

// substituteInArgument replaces placeholders within a single argument.
//
// An unbalanced brace is literal rather than an error: an operator's command may legitimately
// contain a brace that is not a placeholder, and rejecting the launch over that would be a
// surprising way to fail.
func substituteInArgument(arg, filePath, title string, start, end float64, ranged bool) (string, error) {
	var out strings.Builder
	for i := 0; i < len(arg); {
		if arg[i] != '{' {
			out.WriteByte(arg[i])
			i++
			continue
		}
		closing := strings.IndexByte(arg[i:], '}')
		if closing == -1 {
			out.WriteString(arg[i:])
			break
		}
		token := arg[i : i+closing+1]
		if !isKnownPlayerPlaceholder(token) {
			return "", &playerPlaceholderError{token: token}
		}
		switch token {
		case "{file}":
			out.WriteString(filePath)
		case "{start}":
			// Empty when not ranged: see the file comment. An explicit 0 is a different signal
			// to a player than "no seek".
			if ranged {
				out.WriteString(formatSeconds(start))
			}
		case "{end}":
			if ranged {
				out.WriteString(formatSeconds(end))
			}
		case "{title}":
			out.WriteString(title)
		}
		i += closing + 1
	}
	return out.String(), nil
}

// formatSeconds renders a window bound as a plain decimal number.
//
// strconv.FormatFloat with 'f' and -1 precision rather than %g: %g would render 60 as "6e+01",
// which some players parse as 6 and then seek to the wrong place.
func formatSeconds(v float64) string {
	return strconv.FormatFloat(v, 'f', -1, 64)
}
