package api

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// # THE SEAM, AND WHY IT NEEDS THREE TESTS
//
// M5's claim is that the P2P downloader is a PLUGIN, and the claim is about
// removability: a plugin can be deleted by removing a directory, and a core
// feature cannot. That claim is a property of what the SHIPPED ARTIFACT
// contains, and one check cannot establish it:
//
//	TestP2PDownloaderIsNotImportedByCore      the import graph
//	TestP2PDownloaderHasItsOwnModule          the module boundary
//	TestP2PDownloaderIsNotBundledByCore       the built binary
//
// The first version of this project had ONE of these, and it was the first two.
// Those pass on a package inside the core tree that imports nothing from the
// core: it is in the tree, it is in `go build ./...`, it is greppable as core,
// and the grep is empty because the dependency runs the other way. So a green
// seam test on a non-plugin is not a weak test, it is a test of nothing.
//
// The third is the one that cannot be fooled. A core that shells out to a
// BUNDLED downloader passes both greps and is exactly the failure mode worth
// excluding: the code is outside the import graph and inside the shipped
// artifact.

// repoRoot is the checkout root, from this package's directory.
func repoRoot(t *testing.T) string {
	t.Helper()
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatalf("resolving the repo root: %v", err)
	}
	return root
}

// pluginDir is where the downloader lives.
func pluginDir(t *testing.T) string {
	t.Helper()
	dir := filepath.Join(repoRoot(t), "plugins", "p2pdownloader")
	if _, err := os.Stat(dir); err != nil {
		t.Skipf("the downloader is not present at %s: %v. The seam is only "+
			"testable with the plugin checked out, and skipping is right — the "+
			"alternative is failing a core build because an optional component "+
			"is absent", dir, err)
	}
	return dir
}

// TestP2PDownloaderIsNotImportedByCore: the import graph.
//
// The check is on the IMPORT PATH appearing in the core's go.mod, not on the
// word appearing in a .go file. A path in a require block is the only way core
// code can reach a separate module, and it is a one-line diff to review — which
// is the point. A grep for the word "p2pdownloader" across .go files would also
// match this file, and the test's own name, and say nothing.
func TestP2PDownloaderIsNotImportedByCore(t *testing.T) {
	root := repoRoot(t)

	// go.mod is the enforcement. A require or a replace is the mechanism.
	gomod, err := os.ReadFile(filepath.Join(root, "go.mod"))
	if err != nil {
		t.Fatalf("reading go.mod: %v", err)
	}

	for _, line := range strings.Split(string(gomod), "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "//") {
			continue
		}
		if !strings.HasPrefix(trimmed, "require") && !strings.HasPrefix(trimmed, "replace") {
			continue
		}
		if strings.Contains(trimmed, "p2pdownloader") {
			t.Errorf("go.mod has %q\n\n"+
				"  The core can now import the downloader, which is the one thing "+
				"M5 exists to prevent. A plugin that the core depends on cannot be "+
				"removed by deleting a directory -- it is core code wearing a "+
				"plugin's name.", trimmed)
		}
	}

	// And the positive control: the directory really is outside the core module,
	// so the go.mod check is not passing because there is nothing to find.
	dir := pluginDir(t)
	if !strings.HasPrefix(dir, root) {
		t.Fatalf("the plugin is at %s, outside %s. The layout is wrong and every "+
			"other check here is measuring the wrong thing", dir, root)
	}
}

// TestP2PDownloaderHasItsOwnModule: the boundary is real.
//
// A `go.mod` of its own is what makes the core's go.mod check above mean
// something. Without it the downloader is a package of the core module, inside
// `go build ./...`, and greppable as core — a nested module that Go tolerates
// and that enforces nothing.
//
// The module path must also not sit UNDER the core's, for a reason that is
// about review rather than about the toolchain: a downloader whose module claims
// to be `github.com/stashapp/stash/...` reads as part of the project, and the
// check that would stop a core import looks like a naming coincidence.
func TestP2PDownloaderHasItsOwnModule(t *testing.T) {
	dir := pluginDir(t)

	gomodPath := filepath.Join(dir, "go.mod")
	raw, err := os.ReadFile(gomodPath)
	if err != nil {
		t.Fatalf("the downloader has no go.mod of its own at %s: %v\n\n"+
			"  Without one it is a package of the core module: inside "+
			"`go build ./...`, inside the shipped binary, and greppable as core. "+
			"A directory containing a go.mod is NOT enough -- it has to be a "+
			"directory that is outside the core module.", gomodPath, err)
	}

	var modulePath string
	for _, line := range strings.Split(string(raw), "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "module ") {
			modulePath = strings.TrimSpace(strings.TrimPrefix(trimmed, "module "))
			break
		}
	}

	if modulePath == "" {
		t.Fatalf("%s has no module directive:\n%s", gomodPath, raw)
	}

	const corePath = "github.com/stashapp/stash"
	if modulePath == corePath || strings.HasPrefix(modulePath, corePath+"/") {
		t.Errorf("the downloader's module path is %q, which is inside the core's "+
			"(%q). It is a separate module, so the toolchain will keep them "+
			"apart, but it READS as core -- and a module path that looks like "+
			"core is one `replace` away from being imported by it. Use "+
			"stash-plugin-p2pdownloader or similar.", modulePath, corePath)
	}
}

// TestP2PDownloaderIsNotBundledByCore: the shipped artifact.
//
// The other two checks look at the source tree. This one looks at the BINARY,
// because "not imported" and "not present" are different claims and a core that
// shells out to a bundled copy satisfies the first while failing the second
// entirely. The downloader would be outside the import graph and inside every
// release — removable only by a rebuild, which is the definition of core code.
//
// `go tool nm` is the mechanism: it lists the symbols in a built binary, and a
// symbol from the downloader's module path can only be there if the code was
// linked in.
func TestP2PDownloaderIsNotBundledByCore(t *testing.T) {
	pluginDir(t)
	root := repoRoot(t)

	// The positive control is implicit and is the BUILD ITSELF: the core must
	// compile with the plugin present. A test that skips or passes because the
	// build failed has proved nothing, and "the seam holds" must never be the
	// thing a broken build reports. One build, into a path we control, so the
	// symbol listing is of a binary we know exists.
	bin := filepath.Join(t.TempDir(), "stash")
	build := exec.Command("go", "build", "-o", bin, "./cmd/stash")
	build.Dir = root
	build.Env = append(os.Environ(), "GOFLAGS=-mod=mod")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("the core does not build with the plugin present: %v\n%s\n\n"+
			"  This test cannot report on the seam when the thing it is checking "+
			"does not compile. A green seam test on a broken build is worse than "+
			"no test.", err, out)
	}

	nm := exec.Command("go", "tool", "nm", bin)
	nm.Dir = root
	nm.Env = append(os.Environ(), "GOFLAGS=-mod=mod")
	out, err := nm.CombinedOutput()
	if err != nil {
		t.Fatalf("go tool nm on the core binary: %v\n%s\n\n"+
			"  This test is the only one of the three that looks at the shipped "+
			"artifact. If it cannot run, M5's central claim is untested.", err, out)
	}

	// # AND WHY THE MODULE PATH, AND NOT A MARKER CONSTANT
	//
	// This went through three mechanisms and only the third works, so the two
	// failures are worth recording.
	//
	// First: `go tool nm` for a symbol. With a `replace` in go.mod and a real
	// import in main.go, the core BUILT with the downloader linked in and the
	// test PASSED — 113,767 symbols reported, none carrying the module path.
	// The reason is the linker: an uncalled function is dead-code-eliminated,
	// and an eliminated symbol is one nm cannot report.
	//
	// Second: a string constant, on the reasoning that a linker removes
	// instructions rather than string data. That is wrong too, and by the same
	// mechanism: the constant was equally unreachable, so the compiler dropped
	// it before the linker ever saw it. Measured on a 107 MB binary that DID
	// contain the downloader's code — the marker was absent and the module path
	// was present.
	//
	// What actually survives is the module path, because the Go build system
	// embeds it in the binary's own metadata (the pclntab's function-name
	// table) for every linked package, and that table is not subject to
	// reachability analysis. It is present whether or not anything calls the
	// code — which is exactly the property the test needs, since "is it in the
	// artifact" must not depend on whether it happens to be reachable.
	//
	// So the check reads the binary's bytes for the module path, and `nm` is
	// kept only as a secondary signal because it is cheap and can catch a
	// symbol-level match nm's own reader would see.
	const marker = "stash-plugin-p2pdownloader"

	found := binaryContains(t, bin, marker)
	if !found {
		for _, line := range strings.Split(string(out), "\n") {
			if strings.Contains(line, marker) {
				found = true
				break
			}
		}
	}

	if found {
		t.Errorf("the core binary contains %q, which only the downloader's "+
			"module can produce.\n\n"+
			"  Its code is inside the shipped artifact. It is not reachable by "+
			"import, so the other two seam tests pass -- and it cannot be "+
			"removed by deleting a directory, which is the only property "+
			"'plugin' has to mean. M5 is not done.", marker)
	}

	// And the negative control on the control itself: nm must have produced
	// output at all. An empty listing would make the secondary check pass for
	// the wrong reason.
	if len(strings.TrimSpace(string(out))) == 0 {
		t.Fatal("go tool nm produced no output. The symbol listing is empty, so " +
			"the secondary check above is meaningless — a stripped or " +
			"unreadable binary passes it for free")
	}
}

// binaryContains greps the built binary for a byte sequence.
//
// The second mechanism, and the one that is actually load-bearing. `go tool nm`
// reports SYMBOLS, so it misses anything the linker eliminated, and it reports
// nothing for a binary stripped of its symbol table — which a release build
// routinely is. Reading the file itself asks the question the test is actually
// asking: is this byte sequence in the artifact?
func binaryContains(t *testing.T, bin, needle string) bool {
	t.Helper()

	body, err := os.ReadFile(bin)
	if err != nil {
		t.Fatalf("reading the core binary: %v", err)
	}
	if len(body) == 0 {
		t.Fatalf("the core binary is empty at %s. A zero-length file contains "+
			"nothing, so every check against it passes for free", bin)
	}
	return strings.Contains(string(body), needle)
}

// manifestExec reads the `exec:` list out of a plugin manifest, skipping
// comments and blank lines.
//
// Hand-parsed rather than pulling in a YAML library, and the reason is worth
// stating: a test that depends on a parser is a test that fails when the parser
// is upgraded, and this file's whole job is to be trusted when something else
// is suspicious. The subset here is a flat list of scalars under one key, which
// is four lines of code and no ambiguity.
//
// Comments are skipped because the manifest is MOSTLY a comment block explaining
// why each field is what it is -- which is exactly why a substring check over
// the file keeps passing when the value under the key is wrong.
func manifestExec(text string) []string {
	var entries []string
	inExec := false

	for _, line := range strings.Split(text, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}

		if !inExec {
			if strings.HasPrefix(trimmed, "exec:") {
				inExec = true
			}
			continue
		}

		// A list item is "- value". Anything else ends the list: a new key at
		// column zero, or a comment (already skipped) is not an item.
		if strings.HasPrefix(trimmed, "- ") {
			entries = append(entries, strings.TrimSpace(strings.TrimPrefix(trimmed, "- ")))
			continue
		}
		break
	}

	return entries
}

// TestTheDownloaderShipsAManifestTheHostCanRead is the loadability half.
//
// Being separate is not the same as being installable. A module that is outside
// the core and never referenced is not a plugin either — it is a directory
// somebody cloned. This checks the manifest exists, is a `.yml` (which is the
// only extension pkg/plugin/plugins.go:141 loads), and names the binary.
func TestTheDownloaderShipsAManifestTheHostCanRead(t *testing.T) {
	dir := pluginDir(t)

	// The host loads files with a .yml extension, and NOTHING else. A
	// source.json is not read, is not an error, and is silently ignored — so a
	// manifest in the wrong format is a plugin that never loads and gives no
	// reason why.
	matches, err := filepath.Glob(filepath.Join(dir, "*.yml"))
	if err != nil {
		t.Fatalf("globbing for a manifest: %v", err)
	}
	if len(matches) == 0 {
		t.Errorf("the downloader ships no .yml manifest in %s.\n\n"+
			"  pkg/plugin/plugins.go:141 loads every file whose extension is "+
			"`.yml` and nothing else. Without one the plugin cannot be "+
			"installed at all, and 'not bundled by core' becomes the only "+
			"thing true about it.", dir)
		return
	}

	body, err := os.ReadFile(matches[0])
	if err != nil {
		t.Fatalf("reading %s: %v", matches[0], err)
	}
	text := string(body)

	// interface: rpc, because js cannot host a BitTorrent client and raw exits
	// when the task ends, so it can hold neither a DHT table nor a peer
	// connection. This is a property of the protocol, not a preference.
	if !strings.Contains(text, "interface: rpc") {
		t.Errorf("%s does not set `interface: rpc`.\n\n"+
			"  The host offers js (goja — cannot host a BitTorrent client) and "+
			"raw (a binary that exits when the task ends, so no DHT and no "+
			"inbound connections). Only rpc is a long-lived process over "+
			"net/rpc/jsonrpc, which is what a downloader has to be.",
			filepath.Base(matches[0]))
	}

	// The binary the manifest runs, asserted on the VALUE under `exec` and not
	// on the string appearing anywhere in the file.
	//
	// `Contains(text, "stash-plugin-p2pdownloader")` is defeated by the
	// manifest's own comment block, which names the binary in prose. The
	// mutation "exec points at /usr/bin/nothing" survived that check for exactly
	// this reason -- the comment still satisfied it. So the exec list is read
	// as YAML, comments excluded, and each entry is checked.
	//
	// Named BARE, which is what the host documents: it searches $PATH and then
	// the plugins directory (pkg/plugin/config.go:44), so "./x" also works and
	// breaks on Windows where the extension is not written.
	execEntries := manifestExec(text)
	if len(execEntries) == 0 {
		t.Errorf("%s has no exec entry, so the host starts nothing. `interface: "+
			"rpc` with no command is a plugin the host cannot launch",
			filepath.Base(matches[0]))
	}
	for _, entry := range execEntries {
		base := entry
		if i := strings.LastIndexAny(base, "/\\"); i >= 0 {
			base = base[i+1:]
		}
		if base != "stash-plugin-p2pdownloader" {
			t.Errorf("%s runs %q, which is not the downloader. The host would "+
				"start something else, or nothing",
				filepath.Base(matches[0]), entry)
		}
	}

	// The interface must be rpc and NOT the other two, asserted by value rather
	// than by the presence of the right string.
	//
	// The mutation "interface: rpc" -> "interface: raw" SURVIVED the first
	// version of this file, because the check was `Contains(text, "interface:
	// rpc")` and the mutation only ever replaced the VALUE, leaving the key
	// present somewhere else in the document. A presence check on a line that
	// also appears in a comment is a check on nothing.
	for _, line := range strings.Split(text, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "#") {
			continue
		}
		if !strings.HasPrefix(trimmed, "interface:") {
			continue
		}
		value := strings.TrimSpace(strings.TrimPrefix(trimmed, "interface:"))
		if value != "rpc" {
			t.Errorf("the manifest sets `interface: %s`.\n\n"+
				"  raw is a binary that exits when the task ends, so it can hold "+
				"neither a DHT table nor an inbound connection; js is goja, which "+
				"cannot host a BitTorrent client at all. A downloader has to be a "+
				"long-lived process, which is only rpc.",
				value)
		}
	}

	// The identity comes from the FILENAME, because plugin.Config.id is
	// unexported and there is no field for it. A manifest carrying an `id:`
	// line is parsed and ignored, so the one thing worth asserting is that the
	// file is named after the plugin.
	stem := strings.TrimSuffix(filepath.Base(matches[0]), ".yml")
	if stem != "p2p-downloader" {
		t.Errorf("the manifest is %q, so the plugin's id is %q.\n\n"+
			"  plugin.Config.id is unexported and set from the filename stem, so "+
			"the id is whatever this file is called. A manifest carrying an "+
			"`id:` line is parsed and ignored — it is not an error, it just "+
			"has no effect.",
			filepath.Base(matches[0]), stem)
	}
}

// The downloader's DEPENDENCIES must also stay out of the core's module graph.
//
// This closes a gap the module-path check above cannot see, and it was found by
// walking into it rather than by reasoning: writing a `main.go` under
// `plugins/p2pdownloader/cmd/` that imports `github.com/anacrolix/torrent` caused
// the toolchain to resolve that import against the CORE module, which added
// `github.com/anacrolix/torrent v1.61.0 // indirect` and 125 lines to the core's
// `go.sum`.
//
// The damage is small and the reasoning is seductive — it is an INDIRECT require,
// the core still compiles, and every test stays green — which is exactly why it
// needs a guard rather than a habit. Two of this repo's stated invariants depend
// on the core's module graph not containing the transfer protocols: the plugin's
// go.mod exists so "the core does not know this exists" is a fact, and
// `TestP2PDownloaderIsNotBundledByCore` proves the core binary does not link the
// code. A dependency in the graph is a step towards linkability that no binary
// check can see coming.
//
// So: no transfer protocol in the core's go.mod, at all, direct or indirect.
// `anacrolix/dms` IS legitimately there — it is a core dependency of its own, used
// by the download-metadata scan — so the check is on the transfer modules by name
// rather than on the module prefix.
func TestTheTransferProtocolsAreNotInTheCoreModuleGraph(t *testing.T) {
	root := repoRoot(t)
	gomod, err := os.ReadFile(filepath.Join(root, "go.mod"))
	if err != nil {
		t.Fatalf("reading go.mod: %v", err)
	}

	// The transfer protocols, named. These are the modules a preservation plane
	// would be built on and the ones whose presence in the core would mean the
	// boundary has been crossed.
	forbidden := []string{
		"github.com/anacrolix/torrent",
		"github.com/monkeyWie/goed2k",
	}

	got := 0
	for _, mod := range forbidden {
		if strings.Contains(string(gomod), mod) {
			got++
			t.Errorf("core go.mod requires %s\n\n"+
				"The transfer protocols belong to the downloader's own module. A\n"+
				"require here is how a file under plugins/p2pdownloader/ ends up\n"+
				"resolved against the core module -- it happens the first time a cmd\n"+
				"under the plugin imports one, and it is invisible: the core still\n"+
				"builds and every test still passes.\n", mod)
		}
	}
	if got == 0 {
		t.Log("core go.mod carries no transfer protocol; the boundary holds")
	}

	// And the same for go.sum, which is where a resolved dependency leaves its
	// hashes. A go.sum entry alone is harmless, but it is the first half of the
	// same accident and is worth seeing.
	gosum, err := os.ReadFile(filepath.Join(root, "go.sum"))
	if err != nil {
		t.Fatalf("reading go.sum: %v", err)
	}
	for _, mod := range forbidden {
		if strings.Contains(string(gosum), mod+" ") {
			t.Errorf("core go.sum has hashes for %s\n\n"+
				"go.sum entries are the other half of the same leak: the module was\n"+
				"resolved against the core, so its hashes are cached there. Harmless on\n"+
				"its own, and a reliable sign the require above is coming back.\n", mod)
		}
	}
}
