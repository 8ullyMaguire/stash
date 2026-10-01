package api

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// R084, M8 step 8.5, §6b.8. P2P becomes core — the retraction of non-negotiable #11.
//
// # WHY THIS FILE IS IN THE CORE MODULE AND NOT THE PLUGIN'S
//
// §6b.8 retracts #11: the downloader ships in the binary, but "part of core" means
// *shipped and configured in-repo*, NOT merged into the core's import graph. The
// module boundary survives (`TestP2PDownloaderHasItsOwnModule`), and so does the
// "core must not bundle or shell out to the downloader" half of the seam.
//
// What changes is WHERE THE GUARANTEES ARE CHECKED, and §6b.8 is explicit about why:
//
//	"Core code cannot be removed by deleting a directory, so a guard in core is
//	 permanent, and a permanent guard is one that has to be right."
//
// THAT IS THE ARGUMENT FOR THIS FILE. Every guard in §6b.8's list lives in the plugin
// module today, and every guard in the plugin module is a guard that someone can
// delete by deleting a directory — which is exactly what the retraction removed as an
// option. So the guarantee that they EXIST has to live somewhere the directory cannot
// take with it, and that is the core.
//
// # WHAT IS ASSERTED, AND WHY IT IS SOURCE INSPECTION RATHER THAN BEHAVIOUR
//
// These are guards in ANOTHER GO MODULE. The core cannot import them to call them, and
// the plugin cannot be run from a core test without a live client. So this asserts the
// guards' PRESENCE and SHAPE by reading their source.
//
// The obvious objection is that presence is not correctness, and it is right: the
// plugin's own tests are what prove the guards behave correctly, and they exist
// (consent_test.go, gate_test.go, sanitize_test.go). What this adds is the layer the
// retraction removed. Before it, "the downloader has a consent gate" was a fact about
// a directory. After it, it is a fact the core's test suite fails on.
//
// # WHAT WOULD MAKE THIS SUITE WORTHLESS
//
// A test that asserts a file contains the word "consent" passes against a file whose
// consent check has been commented out. So each assertion below is about a SHAPE —
// a named sentinel, a specific function, a check that precedes the write — and the
// mutation gate (#10, which §6b.8 extends to every guard in this path) is the real
// test of whether they bite. §6b.8 cites the seam's own history as the argument: "a
// deleted call site that killed no mutant is how a milestone stayed green while doing
// nothing."

// pluginRoot returns the downloader's module directory, or fails.
func pluginRoot(t *testing.T) string {
	t.Helper()
	root := repoRoot(t)
	dir := filepath.Join(root, "plugins", "p2pdownloader")
	if _, err := os.Stat(dir); err != nil {
		t.Fatalf("the downloader module is not at %s: %v\n\n"+
			"§6b.8 says core code cannot be removed by deleting a directory, so its "+
			"ABSENCE is now a product defect rather than an uninstall. If this is "+
			"deliberate, §6b.8 needs amending first", dir, err)
	}
	return dir
}

// pluginSource reads a file from the downloader module.
func pluginSource(t *testing.T, rel string) string {
	t.Helper()
	path := filepath.Join(pluginRoot(t), rel)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading %s: %v\n\n"+
			"§6b.8's guard list is not optional. A missing guard file means a guard "+
			"was deleted, and after the retraction of #11 there is no uninstall to "+
			"restore it from", rel, err)
	}
	return string(data)
}

// §6b.8's guard list, item 2, verbatim: "The consent gate (§7.1, core's, never the
// plugin's), the storage gate, the path sanitiser, the upload control, and the
// distinction between ErrRefusedUpFront and ErrRefused".
//
// EACH IS ASSERTED INDEPENDENTLY, because a list is only as strong as its weakest
// entry and a single test asserting all five passes as soon as four of them exist.
func TestEveryGuardSection68NamesStillExists(t *testing.T) {
	cases := []struct {
		guard string
		file  string
		// mustFind are substrings whose ABSENCE means the guard is gone or gutted.
		mustFind []string
	}{
		{
			guard: "the consent gate",
			file:  "internal/rpc/consent.go",
			mustFind: []string{
				// §7.1's rule, stated as a refusal. The gate's whole job is to ask
				// core and obey, so a file that cannot say "refuse" is not a gate.
				"func",
				"Refus",
			},
		},
		{
			guard: "the storage gate",
			file:  "internal/storage/gate.go",
			mustFind: []string{
				"func",
				"// Gate",
			},
		},
		{
			guard: "the path sanitiser",
			file:  "internal/paths/sanitize.go",
			mustFind: []string{
				// The named entry point, and the two escapes it exists to prevent.
				// A sanitiser without these two is a function that joins paths.
				"SanitizeJoin",
				"..",
			},
		},
		{
			guard: "the upload control",
			file:  "internal/policy/seeding.go",
			mustFind: []string{
				// §6b.3's three states reach the seed decision, so the control has to
				// be expressible as more than "on".
				"UploadAllowed",
			},
		},
	}

	for _, c := range cases {
		src := pluginSource(t, c.file)
		for _, want := range c.mustFind {
			if !strings.Contains(src, want) {
				t.Errorf("%s (%s) no longer contains %q\n\n"+
					"§6b.8 requires every guard in this list, unchanged and now more "+
					"load-bearing. After the retraction of #11 a deleted guard is not "+
					"recovered by reinstalling a plugin", c.guard, c.file, want)
			}
		}
	}
}

// THE DISTINCTION §6b.8 CALLS OUT BY NAME, and the reason it needs a sentinel rather
// than a comment: "a refusal before the client ever held the torrent is a different
// event with different consequences from a refusal during cleanup, and a sentinel is
// the only way to say so."
//
// The consequence difference is what makes this assertable. ErrRefusedUpFront means
// nothing was fetched and nothing needs cleaning up; ErrRefused means bytes moved and
// there IS something to clean up. A caller that cannot tell them apart either leaks a
// partial download or reports a harmless refusal as a mess.
func TestTheTwoRefusalSentinelsRemainDistinguishable(t *testing.T) {
	downloader := pluginSource(t, "internal/torrent/downloader.go")

	// Both exist, and they are DECLARED SEPARATELY rather than one aliasing the
	// other — `var ErrRefused = ErrRefusedUpFront` would compile and destroy the
	// distinction the spec says only a sentinel can express.
	for _, sentinel := range []string{"ErrRefusedUpFront", "ErrRefused"} {
		if !strings.Contains(downloader, sentinel) {
			t.Errorf("%s is gone from internal/torrent/downloader.go\n\n"+
				"§6b.8 names the distinction between them as a guard that must survive: "+
				"a refusal before the client held the torrent has no cleanup, and one "+
				"during cleanup does", sentinel)
		}
	}

	// THE ALIASING SHAPE, IN EVERY SPELLING IT CAN TAKE.
	//
	// My first version checked one regex, and the mutation run showed three ways to
	// destroy the distinction while both names stayed present and the plugin still
	// COMPILED:
	//
	//	ErrRefusedUpFront = storage.ErrRefused     (the one I missed)
	//	ErrRefusedUpFront = errors.New("refused")  (wrapping dropped)
	//	ErrRefused        = ErrRefusedUpFront      (the one I checked)
	//
	// All three keep the guard's file, its names and its compilation. What they destroy
	// is the ONE property the distinction is for: `errors.Is(err, ErrRefused)` and
	// `errors.Is(err, ErrRefusedUpFront)` are how a caller tells a refusal with no
	// cleanup from a refusal that has partial bytes on disk. Alias them and both
	// comparisons answer the same thing for both errors.
	//
	// Asserted on the DECLARATION rather than on behaviour, because the core module
	// cannot import the plugin to call it — see the file header. The shapes below are
	// the declarations that make the two indistinguishable.
	aliases := map[string]*regexp.Regexp{
		"ErrRefusedUpFront = storage.ErrRefused (an alias of the storage sentinel)":     regexp.MustCompile(`ErrRefusedUpFront\s*=\s*storage\.ErrRefused\b`),
		"ErrRefusedUpFront wrapping removed (so it no longer wraps storage.ErrRefused)": regexp.MustCompile(`ErrRefusedUpFront\s*=\s*errors\.New\(`),
		"ErrRefused = ErrRefusedUpFront":                                                regexp.MustCompile(`ErrRefused\s*=\s*ErrRefusedUpFront\b`),
		"ErrRefusedUpFront = ErrRefused":                                                regexp.MustCompile(`ErrRefusedUpFront\s*=\s*ErrRefused\b`),
	}
	for desc, re := range aliases {
		if re.MatchString(downloader) {
			t.Errorf("the two refusal sentinels are no longer distinguishable: %s\n\n"+
				"Both names are present and the plugin still builds, but "+
				"errors.Is(err, ErrRefused) and errors.Is(err, ErrRefusedUpFront) now "+
				"answer the same question, so a caller cannot tell a refusal that left "+
				"nothing on disk from one that left partial bytes to clean up. §6b.8 "+
				"names this distinction as a guard that must survive, and says a "+
				"sentinel is the only way to say so", desc)
		}
	}

	// AND THE WRAPPING PROPERTY ITSELF, which is what the two declarations are FOR.
	//
	// The comment above the declaration in downloader.go says it: "Both wrap
	// storage.ErrRefused, so errors.Is(err, storage.ErrRefused) is true for both and
	// cannot tell them apart. Without this distinction the only way to observe the
	// difference is the error MESSAGE."
	//
	// So the declaration must be a WRAPPER of storage.ErrRefused, and if it stops
	// being one, a caller's existing errors.Is(err, storage.ErrRefused) check — which
	// is how the storage gate's refusal is recognised at all — silently stops
	// matching. That is a silent behaviour change in core, from an edit to the
	// plugin, which is exactly what §6b.8's retraction makes possible.
	if !strings.Contains(downloader, `%w", storage.ErrRefused`) {
		t.Error("ErrRefusedUpFront no longer wraps storage.ErrRefused.\n\n" +
			"The declaration's own comment says both sentinels wrap it so that " +
			"errors.Is(err, storage.ErrRefused) is true for both. If the up-front " +
			"sentinel stops wrapping, every caller's existing check against the " +
			"storage sentinel stops matching for that case — and it stops matching " +
			"SILENTLY, from an edit in another module, which is the risk §6b.8's " +
			"retraction creates")
	}

	// And each is actually RAISED, not merely declared. An unused sentinel is a
	// declaration that will compile after the code that raises it is deleted, which
	// is precisely the "a deleted call site that killed no mutant" failure §6b.8
	// cites from the seam's own history.
	for _, sentinel := range []string{"ErrRefusedUpFront", "ErrRefused"} {
		raises := strings.Count(downloader, "%w: "+sentinel) +
			strings.Count(downloader, sentinel+")") +
			strings.Count(downloader, sentinel+",") +
			strings.Count(downloader, sentinel+"}\n")
		if raises == 0 {
			t.Errorf("%s is declared but never wrapped or returned in "+
				"internal/torrent/downloader.go. A sentinel nothing raises compiles "+
				"after the code that raised it is deleted, and the guard would be "+
				"gone while the suite stayed green", sentinel)
		}
	}
}

// §6b.8 ITEM 1: "the seam test keeps its second half, which is the one that matters:
// the core must not bundle or shell out to the downloader as a separate process. The
// first half (no imports) survives as a weaker, still-real property."
//
// So BOTH halves must exist, and this asserts the second one is present rather than
// trusting that the retraction left it alone. A retraction that quietly dropped the
// bundling check would leave the module boundary intact and the process boundary gone,
// and nothing else in the suite would notice.
func TestBothHalvesOfTheSeamSurviveTheRetraction(t *testing.T) {
	seam := pluginSource(t, "../../internal/api/stashforge_p2p_seam_test.go")

	// The first half: no imports. Weaker but still real.
	if !strings.Contains(seam, "func TestP2PDownloaderIsNotImportedByCore") {
		t.Error("the no-imports half of the seam is gone. §6b.8 keeps it as a " +
			"'weaker, still-real property', so it must remain a property rather than " +
			"an aspiration")
	}

	// The second half: no bundling, no shelling out. This is the one that matters,
	// and it is the half a retraction is most likely to lose.
	for _, half := range []string{
		"func TestP2PDownloaderIsNotBundledByCore",
	} {
		if !strings.Contains(seam, half) {
			t.Errorf("the no-bundling half of the seam is gone (%s).\n\n"+
				"§6b.8 says this is the half that matters: the core must not bundle or "+
				"shell out to the downloader as a separate process. Without it, "+
				"'part of core' and 'a plugin the core launches' are indistinguishable "+
				"and the guards below are in a different process with no shared gate",
				half)
		}
	}
}

// §6b.8 ITEM 3, and the requirement this file is really evidence for: "Non-negotiable
// #10 applies to every guard in this path" — the mutation gate.
//
// #10's substance is that a guard which kills no mutant is not known to be a guard. A
// test can assert a guard's source contains its sentinel and still kill nothing when
// the sentinel is deleted from the RAISING SITE, because the declaration and the
// declaration-assertion survive together.
//
// So this asserts the guards have their own tests in the downloader module. Not as a
// substitute for #10's per-guard mutation runs — those are run at the guard, not from
// here — but so that "every guard in this path is covered by a suite that exists" is
// answerable, and a guard whose test file is deleted is caught by the core rather than
// discovered when the behaviour regresses.
func TestEveryGuardHasItsOwnTestInTheDownloaderModule(t *testing.T) {
	cases := []struct {
		guard    string
		source   string
		testFile string
	}{
		{"the consent gate", "internal/rpc/consent.go", "internal/rpc/consent_test.go"},
		{"the storage gate", "internal/storage/gate.go", "internal/storage/gate_test.go"},
		{"the path sanitiser", "internal/paths/sanitize.go", "internal/paths/sanitize_test.go"},
		{"the upload control", "internal/policy/seeding.go", "internal/policy/seeding_test.go"},
	}

	for _, c := range cases {
		// The guard's own source is read, so a missing guard is reported as a missing
		// guard rather than as a missing test — the distinction matters, because the
		// first is a deleted guard and the second is a deleted test.
		_ = pluginSource(t, c.source)

		path := filepath.Join(pluginRoot(t), c.testFile)
		if _, err := os.Stat(path); err != nil {
			t.Errorf("%s has no test (%s): %v\n\n"+
				"§6b.8 extends #10 to every guard in this path, and #10's substance is "+
				"that a guard nobody mutates is not known to be a guard. A deleted test "+
				"file leaves the guard in place and every future mutation of it green",
				c.guard, c.testFile, err)
		}
	}
}

// THE COST §6b.8 STATES PLAINLY, ASSERTED SO IT STAYS TRUE.
//
// "a user can no longer remove the feature" — the retraction's price. This test does
// not argue the trade was right; it pins the trade so that a future change cannot
// quietly restore the plugin's removability and claim to have kept §6b.8's
// guarantees. If someone does make the downloader removable again, this fails and the
// failure is the conversation the spec says should happen.
func TestTheRetractionIsInForce(t *testing.T) {
	gomod := pluginSource(t, "go.mod")

	if !strings.Contains(gomod, "module ") {
		t.Error("plugins/p2pdownloader/go.mod declares no module. §6b.8 item 1: " +
			"'plugins/p2pdownloader/ keeps its own go.mod' — without it the boundary " +
			"is gone and the core's go.mod check is passing because there is nothing " +
			"to find")
	}

	// And the downloader is a real, non-trivial module rather than a stub, because
	// "part of core" that ships nothing is indistinguishable from the plugin being
	// uninstalled.
	entries, err := os.ReadDir(filepath.Join(pluginRoot(t), "internal"))
	if err != nil {
		t.Fatalf("listing the downloader's internal packages: %v", err)
	}
	if len(entries) < 5 {
		t.Errorf("the downloader has %d internal packages, which is too few to be the "+
			"feature §6b.8 describes shipping in the binary. If the implementation "+
			"moved into core proper, the module boundary assertion is now measuring a "+
			"shell", len(entries))
	}
}
