package collab

import (
	"errors"
	"testing"
)

// Library access tests. M4 step 4.3.
//
// The plan's two, by name:
//	TestAccess_UngrantedUserGets404Not403
//	TestAccess_MetadataVisibleWhileMediaIsNot
//
// The second is the more important of the two and it is the one that would catch
// a well-meaning "simplification": collapsing metadata access and media access
// into one flag. The first is about a response that must not exist.

// TestAccess_UngrantedUserGets404Not403 is §6.4's headline. The refusal is a
// 404, and the reason is that the user can already see the scene in metadata --
// so a 403 would confirm a file exists.
func TestAccess_UngrantedUserGets404Not403(t *testing.T) {
	// Public mode, so the mode is not the reason for the refusal. Only the grant
	// is missing.
	err := AuthorizeLibraryAccess(AccessDecision{Mode: ModePublic, HasGrant: false})
	if err == nil {
		t.Fatal("an ungranted user was allowed a file in public mode")
	}
	if !IsNoLibraryAccess(err) {
		t.Errorf("returned %v, want the not-found refusal", err)
	}
	// The message is "not found", and nothing else. A 403 needs a reason a
	// client can display, which is exactly what would tell a prober the file is
	// there.
	if err.Error() != "not found" {
		t.Errorf("refusal message = %q, want \"not found\": the reason must not be disclosed", err)
	}

	// And the three refusals are indistinguishable, so a caller cannot tell
	// "you have no grant" from "this mode serves no media" from "no such file".
	noGrant := AuthorizeLibraryAccess(AccessDecision{Mode: ModePublic, HasGrant: false})
	wrongMode := AuthorizeLibraryAccess(AccessDecision{Mode: ModeContribute, HasGrant: true})
	if noGrant.Error() != wrongMode.Error() {
		t.Errorf("refusals differ: %q vs %q -- a client can distinguish 'no grant' from 'wrong mode', which discloses that the file exists",
			noGrant, wrongMode)
	}

	// This is also the only error in the file that must NOT name a library or a
	// user, so assert it carries no identifier.
	for _, leak := range []string{"library", "user", "grant", "mode"} {
		if contains2(err.Error(), leak) {
			t.Errorf("refusal %q mentions %q, which tells a caller why", err, leak)
		}
	}
}

// TestAccess_MetadataVisibleWhileMediaIsNot is the arrangement §6.4 exists to
// permit, and the reason the two grants are separate.
func TestAccess_MetadataVisibleWhileMediaIsNot(t *testing.T) {
	// Public mode, ungranted user: sees the scene, cannot have the file.
	d := AccessDecision{Mode: ModePublic, HasGrant: false}
	if !d.Mode.MetadataVisible() {
		t.Error("metadata must be visible in public mode: this is the whole premise of the commons")
	}
	if d.Allowed() {
		t.Error("media must NOT be reachable for the same user: the two grants are separate")
	}

	// The same holds in contribute, and is the more common case: a user curates
	// against an instance whose owner has never granted anyone file access.
	c := AccessDecision{Mode: ModeContribute, HasGrant: true}
	if !c.Mode.MetadataVisible() {
		t.Error("contribute shares metadata by design")
	}
	if c.Allowed() {
		t.Error("contribute must not serve media even to a grantee: §6.4's default is metadata flows, media does not")
	}

	// Private shares nothing, and serves nothing.
	p := AccessDecision{Mode: ModePrivate, HasGrant: true}
	if p.Mode.MetadataVisible() {
		t.Error("private mode shares no metadata")
	}
	if p.Allowed() {
		t.Error("private mode serves no media, grant or not")
	}

	// The only combination that reaches a file: public AND granted.
	yes := AccessDecision{Mode: ModePublic, HasGrant: true}
	if !yes.Allowed() {
		t.Error("public + granted must be allowed: that is the one case §6.4 permits")
	}

	// And the four combinations, as a table, because "private serves nobody" and
	// "contribute serves nobody" are separate rules and either could be dropped.
	for _, tc := range []struct {
		mode      Mode
		grant     bool
		wantMedia bool
		wantMeta  bool
	}{
		{ModePrivate, false, false, false},
		{ModePrivate, true, false, false},
		{ModeContribute, false, false, true},
		{ModeContribute, true, false, true},
		{ModePublic, false, false, true},
		{ModePublic, true, true, true},
	} {
		d := AccessDecision{Mode: tc.mode, HasGrant: tc.grant}
		if got := d.Allowed(); got != tc.wantMedia {
			t.Errorf("%q grant=%v: media allowed = %v, want %v", tc.mode, tc.grant, got, tc.wantMedia)
		}
		if got := d.Mode.MetadataVisible(); got != tc.wantMeta {
			t.Errorf("%q: metadata visible = %v, want %v", tc.mode, got, tc.wantMeta)
		}
	}
}

// TestAccess_AnInvalidModeIsAnErrorNotADenial: an unrecognised mode must not
// resolve to "denied", because a caller that treats any error as a 404 will
// serve 404s forever and nobody will diagnose it.
func TestAccess_AnInvalidModeIsAnErrorNotADenial(t *testing.T) {
	err := AuthorizeLibraryAccess(AccessDecision{Mode: Mode("open"), HasGrant: true})
	if !errors.Is(err, ErrModeInvalid) {
		t.Errorf("an invalid mode returned %v, want ErrModeInvalid", err)
	}
	if IsNoLibraryAccess(err) {
		t.Error("an invalid mode was reported as a not-found refusal: that hides a misconfiguration behind a 404")
	}
}

// TestSortUserIDs: the owner-facing list must be stable, and must not reorder or
// alias the caller's slice.
func TestSortUserIDs(t *testing.T) {
	in := []int64{5, 1, 3}
	out := SortUserIDs(in)
	if len(out) != 3 || out[0] != 1 || out[1] != 3 || out[2] != 5 {
		t.Errorf("SortUserIDs = %v, want [1 3 5]", out)
	}
	if in[0] != 5 {
		t.Error("SortUserIDs mutated its argument")
	}
	// nil in, nil out: an owner with no grantees gets an empty list, not a panic
	// and not a one-element slice containing zero.
	if got := SortUserIDs(nil); len(got) != 0 {
		t.Errorf("SortUserIDs(nil) = %v, want empty", got)
	}
}
