package config

// stash#2747 -- the external player's configuration layer.
//
// ## WHAT THESE TESTS ARE FOR
//
// Three of the rules here are SECURITY rules that produce no visible failure when broken:
//
//   - a bare binary name resolves through the SERVER's PATH, so which player runs depends on how
//     Stash was started rather than on the operator's config;
//   - a typo in a placeholder is passed through literally, and the player then refuses the file,
//     which reads as a MEDIA problem and sends the operator to the wrong place;
//   - the spawned process inherits the server's environment, which holds the API key.
//
// Each has a test that fails if the rule is removed. Each is a one-line mutation to check, so
// they are the mutations most worth running -- and what they have in common is that the code
// still "works" when broken.
//
// ## WHY THE WINDOW IS PASSED AS A FLAG TO THE TESTS
//
// The interesting cases are the whole-file versus window distinction, and a window's presence is
// exactly what `ranged` carries. Testing only the numbers would let an implementation substitute
// `0` for an absent start and pass every numeric assertion while sending the wrong signal to a
// player that distinguishes "seek to zero" from "no seek".

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// stubConfig builds a Config carrying only the external_player keys.
//
// GetExternalPlayerConfig reads through getString/getBool, so the existing InitializeEmpty
// fixture is the honest one -- a hand-rolled struct would test a shape that does not exist.
func stubConfig(t *testing.T, enabled bool, command string) *Config {
	t.Helper()
	i := InitializeEmpty()
	i.main.Set(ExternalPlayerEnabled, enabled)
	if command != "" {
		i.main.Set(ExternalPlayerCommand, command)
	}
	return i
}

// realBinary returns an absolute path to a file that exists, for the binary-must-exist rule.
func realBinary(t *testing.T) string {
	t.Helper()
	// A shell or a sleep exists on any machine this runs on; the test never executes it.
	for _, candidate := range []string{"/bin/sh", "/usr/bin/sh", "/bin/sleep"} {
		if fi, err := os.Stat(candidate); err == nil && fi.Mode().IsRegular() {
			return candidate
		}
	}
	t.Skip("no known regular file to use as a stand-in binary")
	return ""
}

func TestExternalPlayerIsRefusedWhenNotEnabled(t *testing.T) {
	bin := realBinary(t)
	i := stubConfig(t, false, bin+" {file}")

	_, ok, reason := i.GetExternalPlayerConfig(nil, "/media/a.mp4", "A", 0, 0, false)
	if ok {
		t.Fatal("a disabled external player must not resolve to a launchable config")
	}
	if reason != "" {
		// A disabled feature is not an error to report; the UI needs to know it is off, which is
		// why an empty reason is load-bearing rather than a forgotten message.
		t.Errorf("a disabled player should carry no reason to display, got %q", reason)
	}
}

func TestExternalPlayerCommandMustBeAnAbsolutePath(t *testing.T) {
	i := stubConfig(t, true, "mpv {file}")

	_, ok, reason := i.GetExternalPlayerConfig(nil, "/media/a.mp4", "A", 0, 0, false)
	if ok {
		t.Fatal("a bare binary name must be refused: it would resolve through the SERVER's PATH")
	}
	if !strings.Contains(reason, "absolute") {
		t.Errorf("the reason must say the path must be absolute, got %q", reason)
	}
}

func TestExternalPlayerCommandMustExist(t *testing.T) {
	i := stubConfig(t, true, "/nonexistent/player-binary {file}")

	_, ok, reason := i.GetExternalPlayerConfig(nil, "/media/a.mp4", "A", 0, 0, false)
	if ok {
		t.Fatal("a command pointing at a file that does not exist must be refused")
	}
	if !strings.Contains(reason, "/nonexistent/player-binary") {
		t.Errorf("the reason must name the file that is not there, got %q", reason)
	}
}

func TestExternalPlayerSubstitutesTheWindow(t *testing.T) {
	bin := realBinary(t)
	i := stubConfig(t, true, bin+" {file} --start={start} --end={end}")

	cfg, ok, reason := i.GetExternalPlayerConfig(nil, "/media/a.mp4", "A", 120, 480, true)
	if !ok {
		t.Fatalf("expected a launchable config, got reason %q", reason)
	}

	joined := strings.Join(cfg.Argv, " ")
	if !strings.Contains(joined, "--start=120") {
		t.Errorf("the start must be substituted in SECONDS, got %q", joined)
	}
	if !strings.Contains(joined, "--end=480") {
		t.Errorf("the end must be substituted in seconds, got %q", joined)
	}
	if !cfg.Ranged {
		t.Error("a scene with a window must be reported as ranged")
	}
}

func TestExternalPlayerLeavesTheWindowEmptyForAWholeFileScene(t *testing.T) {
	bin := realBinary(t)
	i := stubConfig(t, true, bin+" {file} --start={start} --end={end}")

	// ranged=false with start=0 end=1800: numerically a valid window, but semantically the whole
	// file. The placeholders must come out EMPTY rather than as 0 and 1800.
	cfg, ok, reason := i.GetExternalPlayerConfig(nil, "/media/a.mp4", "A", 0, 1800, false)
	if !ok {
		t.Fatalf("expected a launchable config, got reason %q", reason)
	}

	// Assert on the ARGV, not a joined string. The distinction this file has to keep is between
	// three states that a joined string cannot tell apart:
	//
	//	--start=0     an explicit seek to zero -- WRONG, a different signal to a player
	//	--start=      the flag present with an empty value -- what the template asked for
	//	(no flag)     the template's placeholder stood alone, so nothing to substitute
	//
	// My first version only checked that "--start=0" was absent, which passed whether the flag
	// was dropped entirely or kept with an empty value -- and a mutant that drops it survived.
	// Counting occurrences of the flag is what separates the three.
	startCount, endCount := 0, 0
	for _, a := range cfg.Argv {
		if strings.HasPrefix(a, "--start") {
			startCount++
			if a != "--start=" {
				t.Errorf("a whole-file scene must not send a start value; got %q", a)
			}
		}
		if strings.HasPrefix(a, "--end") {
			endCount++
			if a != "--end=" {
				t.Errorf("a whole-file scene must not send an end value; got %q", a)
			}
		}
	}
	if startCount != 1 {
		t.Errorf("expected the --start flag to survive with an empty value, found %d in %q",
			startCount, cfg.Argv)
	}
	if endCount != 1 {
		t.Errorf("expected the --end flag to survive with an empty value, found %d in %q",
			endCount, cfg.Argv)
	}
	if cfg.Ranged {
		t.Error("a whole-file scene must not be reported as ranged")
	}
}

// TestExternalPlayerKeepsAnEmptyValuedFlag is the other half of the drop rule, and the case E4
// lives or dies on.
//
// `--start={start}` is a flag whose VALUE becomes empty. Dropping it would launch a player that
// does not do what the config says -- the operator wrote the flag, and quietly omitting it is a
// different invocation. So the rule is asymmetric on purpose:
//
//	`--start=`     KEPT   -- a flag with an empty value
//	`{start}`      DROPPED -- an argument that is nothing but an absent value
//
// and a single assertion cannot cover both, which is why they are two tests. My first version
// checked only that "--start=0" was absent from the joined argv, which passed under BOTH
// behaviours -- and a mutant that drops every empty argument survived it.
func TestExternalPlayerKeepsAnEmptyValuedFlag(t *testing.T) {
	bin := realBinary(t)
	i := stubConfig(t, true, bin+" --start={start} --title={title}")

	// No window and no title: both flags are present with empty values.
	cfg, ok, reason := i.GetExternalPlayerConfig(nil, "/media/a.mp4", "", 0, 0, false)
	if !ok {
		t.Fatalf("expected a launchable config, got reason %q", reason)
	}
	if len(cfg.Argv) != 3 {
		t.Fatalf("both flags must survive with empty values, got %q", cfg.Argv)
	}
	if cfg.Argv[1] != "--start=" {
		t.Errorf("Argv[1] = %q, want \"--start=\" -- dropping the flag would change the invocation", cfg.Argv[1])
	}
	if cfg.Argv[2] != "--title=" {
		t.Errorf("Argv[2] = %q, want \"--title=\"", cfg.Argv[2])
	}
}

// TestExternalPlayerDropsAPlaceholderThatIsAWholeArgument is the case E4 lives or dies on.
//
// `{start}` as its own token with no window substitutes to nothing at all, so there is no argument
// left to pass -- the flag is not "empty", it is absent. Keeping it would mean asking a player to
// seek to the empty string.
func TestExternalPlayerDropsAPlaceholderThatIsAWholeArgument(t *testing.T) {
	bin := realBinary(t)
	i := stubConfig(t, true, bin+" {file} {start}")

	// A ranged scene: the placeholder resolves, so the flag is present.
	cfg, ok, reason := i.GetExternalPlayerConfig(nil, "/media/a.mp4", "A", 30, 90, true)
	if !ok {
		t.Fatalf("expected a launchable config, got reason %q", reason)
	}
	if len(cfg.Argv) != 3 {
		t.Errorf("a ranged scene must pass the start, got %q", cfg.Argv)
	}
	if cfg.Argv[2] != "30" {
		t.Errorf("Argv[2] = %q, want \"30\"", cfg.Argv[2])
	}

	// The same scene with no window: nothing to say, so no argument.
	cfg, ok, reason = i.GetExternalPlayerConfig(nil, "/media/a.mp4", "A", 0, 1800, false)
	if !ok {
		t.Fatalf("expected a launchable config, got reason %q", reason)
	}
	if len(cfg.Argv) != 2 {
		t.Errorf("a whole-file scene must drop the empty start argument, got %q", cfg.Argv)
	}
}

// TestExternalPlayerRejectsAnUnknownPlaceholder is the one that looks most like a media bug.
//
// A replacer passes `{startt}` through untouched. The player then receives a literal `{startt}`
// as an argument, fails to open the file, and the operator concludes the FILE is broken.
func TestExternalPlayerRejectsAnUnknownPlaceholder(t *testing.T) {
	bin := realBinary(t)
	i := stubConfig(t, true, bin+" {file} --startt={startt}")

	_, ok, reason := i.GetExternalPlayerConfig(nil, "/media/a.mp4", "A", 10, 20, true)
	if ok {
		t.Fatal("an unknown placeholder must be an error, not something passed to the player")
	}
	if !strings.Contains(reason, "{startt}") {
		t.Errorf("the reason must quote the offending placeholder, got %q", reason)
	}
	if !strings.Contains(reason, "{file}") {
		t.Errorf("the reason must list the valid placeholders, got %q", reason)
	}
}

func TestExternalPlayerLeavesAnUnbalancedBraceAlone(t *testing.T) {
	bin := realBinary(t)
	// A brace that is not a placeholder must not fail the launch: an operator's command may
	// legitimately contain one.
	i := stubConfig(t, true, bin+" {file} --x={not-a-placeholder-closed")

	cfg, ok, reason := i.GetExternalPlayerConfig(nil, "/media/a.mp4", "A", 0, 0, false)
	if !ok {
		t.Fatalf("an unbalanced brace should not be rejected, got reason %q", reason)
	}
	if !strings.Contains(strings.Join(cfg.Argv, " "), "--x=") {
		t.Error("the literal text after the unbalanced brace must be preserved")
	}
}

// TestExternalPlayerFormatSecondsIsPlainDecimal pins the rendering a player will parse.
//
// I wrote this believing %g renders 60 as "6e+01", which is FALSE -- %g only uses exponent
// notation at extreme magnitudes, and 1e21 seconds is not a video duration. So there was never a
// bug here, and the "no e or E anywhere" assertion I first wrote would have FAILED on the
// legitimate 1e21 case.
//
// What is worth pinning is narrower and still real: the output must be a plain decimal a player's
// argument parser reads the same way Go does, with no trailing ".0" noise and no loss of
// fractional seconds. A whole-second boundary is where a rounding or truncation bug would show.
func TestExternalPlayerFormatSecondsIsPlainDecimal(t *testing.T) {
	cases := []struct {
		in   float64
		want string
	}{
		{0, "0"},
		{6, "6"},
		{60, "60"},
		{3600, "3600"},
		{0.5, "0.5"},
		{120.25, "120.25"},
		// A window ending mid-second must keep its fraction. Rounding it would move the end of
		// the scene by up to half a second, which a user notices as the clip cutting early.
		{1799.75, "1799.75"},
	}
	for _, c := range cases {
		if got := formatSeconds(c.in); got != c.want {
			t.Errorf("formatSeconds(%v) = %q, want %q", c.in, got, c.want)
		}
	}
}

// TestExternalPlayerFormatSecondsKeepsAFraction is separated out because it is the one case where
// a plausible "tidy up the formatting" edit would break a behaviour rather than a spelling.
func TestExternalPlayerFormatSecondsKeepsAFraction(t *testing.T) {
	if got := formatSeconds(1799.999); got != "1799.999" {
		t.Errorf("formatSeconds(1799.999) = %q; a truncated fraction moves the end of the scene", got)
	}
}

// TestExternalPlayerScrubsTheEnvironment is the secret-leak test.
//
// STASH_API_KEY and a database DSN are exactly what a server's environment holds. A player that
// inherits them is a long-lived process holding the key that can rewrite the whole database.
func TestExternalPlayerScrubsTheEnvironment(t *testing.T) {
	environ := []string{
		"PATH=/usr/bin",
		"DISPLAY=:0",
		"HOME=/home/op",
		"STASH_API_KEY=super-secret",
		"DATABASE_URL=postgres://user:hunter2@localhost/stash",
		"AWS_SECRET_ACCESS_KEY=nope",
	}

	got := PlayerEnv(environ)
	joined := strings.Join(got, " ")

	for _, allowed := range []string{"PATH=/usr/bin", "DISPLAY=:0", "HOME=/home/op"} {
		if !strings.Contains(joined, allowed) {
			t.Errorf("%s must reach the player; it is on the allowlist", allowed)
		}
	}
	for _, secret := range []string{"super-secret", "hunter2", "nope"} {
		if strings.Contains(joined, secret) {
			t.Errorf("the child's environment leaked %q", secret)
		}
	}
}

func TestExternalPlayerScrubKeepsAllowlistedEntriesOnly(t *testing.T) {
	// A name that is a PREFIX of an allowed key must not slip through, and neither must an empty
	// name or a malformed entry.
	got := PlayerEnv([]string{
		"DISPLAY=:0",
		"DISPLAY_EXTRA=leak",
		"=noname",
		"noequalssign",
		"PATHEXTRA=/evil",
	})

	for _, bad := range []string{"DISPLAY_EXTRA", "=noname", "noequalssign", "PATHEXTRA"} {
		if strings.Contains(strings.Join(got, " "), bad) {
			t.Errorf("%q must not survive the scrub", bad)
		}
	}
}

func TestExternalPlayerSplitsArgumentsWithoutAShell(t *testing.T) {
	bin := realBinary(t)
	// A title with spaces and a semicolon. Splitting with strings.Fields keeps them as ONE
	// argument, and exec.Command with a []string never consults a shell -- so the semicolon is
	// data, not a command separator.
	i := stubConfig(t, true, bin+" {file} --title={title}")

	cfg, ok, reason := i.GetExternalPlayerConfig(nil, "/media/a.mp4", "A; rm -rf /", 0, 0, false)
	if !ok {
		t.Fatalf("expected a launchable config, got reason %q", reason)
	}

	joined := strings.Join(cfg.Argv, "|")
	if !strings.Contains(joined, "--title=A; rm -rf /") {
		t.Errorf("the title must arrive as a single literal argument, got %q", cfg.Argv)
	}
	// If it had been passed through a shell this would have become separate argv entries.
	if len(cfg.Argv) != 3 {
		t.Errorf("expected exactly 3 argv entries (binary, file, title), got %d: %q",
			len(cfg.Argv), cfg.Argv)
	}
}

func TestExternalPlayerUsesAnAbsoluteBinaryPath(t *testing.T) {
	bin := realBinary(t)
	i := stubConfig(t, true, bin+" {file}")

	cfg, ok, _ := i.GetExternalPlayerConfig(nil, "/media/a.mp4", "A", 0, 0, false)
	if !ok {
		t.Fatal("expected a launchable config")
	}
	if !filepath.IsAbs(cfg.BinaryPath) {
		t.Errorf("BinaryPath = %q, want an absolute path", cfg.BinaryPath)
	}
	if cfg.Argv[0] != cfg.BinaryPath {
		t.Errorf("Argv[0] %q and BinaryPath %q disagree; the binary is exec'd from Argv[0]",
			cfg.Argv[0], cfg.BinaryPath)
	}
}
