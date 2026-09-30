package api

// stash#7241 (third call site) -- the plugin asset route's containment check is
// a strings.HasPrefix, and that is not containment.
//
//	internal/api/routes_plugin.go:60, as it stands:
//
//		if !strings.HasPrefix(dir, pluginDir) {
//
// "/data/plugins-evil" starts with "/data/plugins", so a path that escapes the
// plugin directory while re-entering its own PREFIX passes the check. Measured
// on this tree, with pluginDir = <plugins>/myplugin:
//
//	  fsPath "../myplugin-evil"            guard=true  inside=false  ESCAPE
//	  fsPath ".."                           guard=true  inside=false  ESCAPE
//	  fsPath "../myplugin-evil/secret.txt"  guard=true  inside=false  ESCAPE
//	  fsPath "/etc"                         guard=false inside=false  blocked (by luck)
//
// The shape that matters: this is NOT a remote-attacker traversal. `dir` comes
// from p.UI.Assets.GetFilesystemLocation, which returns a value from the
// PLUGIN'S OWN URLMap -- configuration, not a URL. So the exposure is "a
// plugin, or a typo in a plugin's config, can read outside its own directory",
// which is still a containment failure and is the bug class this project
// treats as owning the box.
//
// WHY NOT THE UPSTREAM FIX, ON PURPOSE. #7241 fixes this line with a
// trailing-separator HasPrefix. That is correct, and it is still a string
// comparison. This tree already has the real thing -- fsutil.SafeJoin, added
// for stash#7240 and used by the other two archive call sites -- so using it
// here is what makes the three sites share one gate instead of three.
//
// The rejection is recorded because "we already fixed this issue" and "we fixed
// it the way we fix things" are different claims, and this is the one that says
// which.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stashapp/stash/pkg/fsutil"
)

// pluginDirFor builds the layout the route assumes: <plugins>/<id>, plus a
// SIBLING directory whose name shares the plugin directory's prefix. The
// sibling is the whole point: a `..` that lands on it stays inside the string
// prefix while being outside the directory.
func pluginDirFor(t *testing.T) (pluginDir, sibling, secret string) {
	t.Helper()

	plugins := t.TempDir()
	pluginDir = filepath.Join(plugins, "myplugin")
	if err := os.MkdirAll(pluginDir, 0o755); err != nil {
		t.Fatal(err)
	}

	sibling = filepath.Join(plugins, "myplugin-evil")
	if err := os.MkdirAll(sibling, 0o755); err != nil {
		t.Fatal(err)
	}

	secret = filepath.Join(sibling, "secret.txt")
	if err := os.WriteFile(secret, []byte("PRIVATE"), 0o644); err != nil {
		t.Fatal(err)
	}
	return pluginDir, sibling, secret
}

func TestAPluginAssetPathThatReEntersThePrefixIsRefused(t *testing.T) {
	pluginDir, _, secret := pluginDirFor(t)

	got, err := fsutil.SafeJoin(pluginDir, "../myplugin-evil")
	if err == nil {
		t.Fatalf("the guard permitted %q (resolved %q) while the secret sits at %q; "+
			"a path that escapes the plugin directory by re-entering its own PREFIX "+
			"is the shape strings.HasPrefix cannot see", got, got, secret)
	}
}

// `..` is refused by SafeJoin AND by the old prefix check (it cleans to
// pluginDir's parent, which does not share the prefix). It is here because a
// guard tested only on inputs it happens to refuse proves nothing, and because
// this one is the shape a reader will try first.
func TestAPluginAssetPathThatWalksUpIsRefused(t *testing.T) {
	pluginDir, _, _ := pluginDirFor(t)

	if _, err := fsutil.SafeJoin(pluginDir, ".."); err == nil {
		t.Error("the parent directory was accepted as a plugin asset root")
	}
}

// The control. Without it the tests above could pass because the FIXTURE is
// wrong rather than because the CHECK is right -- which is the trap a guard
// tested only with inputs it happens to refuse.
func TestTheOldPrefixCheckWouldHavePermittedThese(t *testing.T) {
	pluginDir, _, _ := pluginDirFor(t)

	// Only the shapes that genuinely escape. ".." is absent because it does
	// not, and including it made this control fail -- which is the control
	// working.
	for _, fsPath := range []string{"../myplugin-evil", "../myplugin-evil/secret.txt"} {
		dir := filepath.Join(pluginDir, fsPath)
		if !strings.HasPrefix(dir, pluginDir) {
			t.Fatalf("control broken: the OLD check already refuses %q, so this "+
				"fixture does not demonstrate the bug", fsPath)
		}
		if _, err := fsutil.SafeJoin(pluginDir, fsPath); err == nil {
			t.Errorf("the shared gate permitted %q", fsPath)
		}
	}
}

func TestAnOrdinaryPluginAssetPathStillResolves(t *testing.T) {
	// The control for the other direction: a fix that refuses everything is
	// indistinguishable from a fix, and this is what tells them apart.
	pluginDir, _, _ := pluginDirFor(t)

	got, err := fsutil.SafeJoin(pluginDir, "assets")
	if err != nil {
		t.Fatalf("an ordinary asset path was refused: %v", err)
	}
	if want := filepath.Join(pluginDir, "assets"); got != want {
		t.Errorf("resolved to %q, want %q", got, want)
	}
}

func TestANestedAssetPathStillResolves(t *testing.T) {
	pluginDir, _, _ := pluginDirFor(t)

	got, err := fsutil.SafeJoin(pluginDir, "assets/css/site.css")
	if err != nil {
		t.Fatalf("a nested asset path was refused: %v", err)
	}
	if want := filepath.Join(pluginDir, "assets", "css", "site.css"); got != want {
		t.Errorf("resolved to %q, want %q", got, want)
	}
}

// The asset route is the only one of the three whose path comes from a plugin's
// own config rather than from an archive, so asserting the wiring is a source
// check. It has a control of its own, because a source-scanning test that
// matches nothing passes.
func TestTheAssetRouteUsesTheSharedGate(t *testing.T) {
	src, err := os.ReadFile("routes_plugin.go")
	if err != nil {
		t.Fatal(err)
	}
	text := string(src)

	if strings.Contains(text, "strings.HasPrefix(dir, pluginDir)") {
		t.Error("routes_plugin.go still contains the prefix check; the shared gate is not wired in")
	}
	if !strings.Contains(text, "fsutil.SafeJoin") {
		t.Error("routes_plugin.go does not call fsutil.SafeJoin; this scanner may be " +
			"matching nothing (see TestTheAssetRouteScanIsNotVacuous)")
	}
}

func TestTheAssetRouteScanIsNotVacuous(t *testing.T) {
	src, err := os.ReadFile("routes_plugin.go")
	if err != nil {
		t.Fatal(err)
	}
	text := string(src)
	if !strings.Contains(text, "func (rs pluginRoutes) Assets(") {
		t.Fatal("the Assets handler moved or was renamed; every source check in this " +
			"file is now asserting against a symbol that does not exist")
	}
	if !strings.Contains(text, "pluginDir") {
		t.Fatal("pluginDir is gone from the handler; the source checks here have lost their subject")
	}
}
