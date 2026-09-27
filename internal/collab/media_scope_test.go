package collab

import (
	"context"
	"errors"
	"testing"
)

// The scope resolver is the first thing in this project where the ORDER of
// refusals is itself a security property, so the fixtures here are built around
// what must NOT be reachable rather than around the case that is.
//
// One rule governs the whole file: a store that fails is a store whose answer is
// unknown, and an unknown answer is a refusal. Every test that plants an error
// asserts the refusal, because a resolver that propagated the error and a
// resolver that treated it as "no access" would both look correct at the call
// site -- and one of them fails open.

type fakeScopeStore struct {
	libraryID    int64
	libraryErr   error
	ownerID      int64
	ownerErr     error
	defaultID    int64
	defaultErr   error
	hasAccess    bool
	hasAccessErr error
	mode         Mode
	modeErr      error

	// calls records what was asked, in order, so a test can assert the
	// resolver stopped early rather than continuing past a refusal.
	calls []string
}

func (f *fakeScopeStore) LibraryOfTarget(_ context.Context, targetType string, targetID int64) (int64, error) {
	f.calls = append(f.calls, "LibraryOfTarget:"+targetType)
	return f.libraryID, f.libraryErr
}

func (f *fakeScopeStore) LibraryOwner(_ context.Context, _ int64) (int64, error) {
	f.calls = append(f.calls, "LibraryOwner")
	return f.ownerID, f.ownerErr
}

func (f *fakeScopeStore) DefaultLibraryID(_ context.Context) (int64, error) {
	f.calls = append(f.calls, "DefaultLibraryID")
	return f.defaultID, f.defaultErr
}

func (f *fakeScopeStore) HasAccess(_ context.Context, _, _ int64) (bool, error) {
	f.calls = append(f.calls, "HasAccess")
	return f.hasAccess, f.hasAccessErr
}

func (f *fakeScopeStore) Mode(_ context.Context) (Mode, error) {
	f.calls = append(f.calls, "Mode")
	return f.mode, f.modeErr
}

func ownerScope() *fakeScopeStore {
	return &fakeScopeStore{libraryID: 7, ownerID: 1, defaultID: 7, mode: ModePublic}
}

func TestResolveScope_OwnerNeedsNoGrantRow(t *testing.T) {
	// The owner's access comes from libraries.user_id, NOT from a
	// user_library_access row. Without this, every install that upgraded
	// would come back to a public instance where its own owner's media
	// 404s until they grant themselves access -- and the pressure to "fix"
	// that by treating ownership as a grant is exactly how ownership and
	// granting stop being distinguishable in an audit log.
	store := ownerScope()
	store.ownerID = 1

	d, err := ResolveScope(context.Background(), store, ModePublic, 1, TargetScene, 42)
	if err != nil {
		t.Fatalf("ResolveScope for the owner: %v", err)
	}
	if !d.IsOwner {
		t.Error("IsOwner is false for the user who owns the library; the " +
			"owner must not need a grant row to reach their own media")
	}
	if d.HasGrant {
		t.Error("HasGrant is true for the owner, but no grant was consulted. " +
			"HasGrant is the grant's ANSWER, and writing ownership into it " +
			"makes a later audit unable to say which rule allowed the request")
	}
	if !d.Allowed() {
		t.Errorf("the owner was refused their own media: %v", d.Decide())
	}
}

func TestResolveScope_NonOwnerWithoutGrantIsRefused(t *testing.T) {
	store := ownerScope()
	store.ownerID = 1
	store.hasAccess = false

	d, err := ResolveScope(context.Background(), store, ModePublic, 2, TargetScene, 42)
	if err != nil {
		t.Fatalf("ResolveScope: %v", err)
	}
	if d.Allowed() {
		t.Error("a user with no grant was allowed the media; §6.4 says media " +
			"is served only to a user holding a user_library_access row")
	}
	if !IsNoLibraryAccess(d.Decide()) {
		t.Errorf("the refusal is %v, want the single not-found sentinel. A "+
			"distinct error here becomes a 403, and a 403 confirms a file "+
			"exists to somebody who can already see it in metadata", d.Decide())
	}
}

func TestResolveScope_NonOwnerWithGrantIsAllowed(t *testing.T) {
	store := ownerScope()
	store.ownerID = 1
	store.hasAccess = true

	d, err := ResolveScope(context.Background(), store, ModePublic, 2, TargetScene, 42)
	if err != nil {
		t.Fatalf("ResolveScope: %v", err)
	}
	if !d.Allowed() {
		t.Errorf("a granted user was refused: %v", d.Decide())
	}
	if d.IsOwner {
		t.Error("IsOwner is true for a user who does not own the library; " +
			"ownership and a grant are different answers and collapsing " +
			"them is how a grant is granted to somebody by accident")
	}
}

// TestResolveScope_RowInNoLibraryBecomesTheDefaultLibrary is the fail-closed
// property, and it is the line most likely to be "simplified" later.
//
// A row with no library is NOT public. The scanner writes rows through a path
// that knows nothing about libraries, so this is the COMMON case, not an edge
// case -- and reading it as "unrestricted" is a fail-open on exactly the rows
// nobody is watching.
func TestResolveScope_RowInNoLibraryBecomesTheDefaultLibrary(t *testing.T) {
	store := &fakeScopeStore{
		libraryID: 0, // the row is in no library
		defaultID: 9, // the instance has a default library
		ownerID:   1,
		mode:      ModePublic,
		hasAccess: false,
	}

	d, err := ResolveScope(context.Background(), store, ModePublic, 2, TargetScene, 42)
	if err != nil {
		t.Fatalf("ResolveScope: %v", err)
	}
	if d.LibraryID != 9 {
		t.Errorf("a row in no library resolved to library %d, want the default 9. "+
			"Resolving to 0 would mean the refusal below is happening by "+
			"accident rather than by the rule", d.LibraryID)
	}
	if d.Allowed() {
		t.Error("an ungranted user was allowed an unscoped row. A row in no " +
			"library belongs to the default library, which still requires a " +
			"grant from anybody who is not its owner")
	}

	// And the owner of the DEFAULT library is its owner, not everyone.
	d2, err := ResolveScope(context.Background(), store, ModePublic, 1, TargetScene, 42)
	if err != nil {
		t.Fatalf("ResolveScope for the default library's owner: %v", err)
	}
	if !d2.Allowed() {
		t.Errorf("the owner was refused an unscoped row: %v", d2.Decide())
	}
}

// TestResolveScope_NoDefaultLibraryIsARefusal covers the instance with no
// users. There is no owner to own a default library, so there is nothing to
// grant access to, and the honest answer is the refusal rather than "allow".
func TestResolveScope_NoDefaultLibraryIsARefusal(t *testing.T) {
	store := &fakeScopeStore{libraryID: 0, defaultID: 0, mode: ModePublic}

	d, err := ResolveScope(context.Background(), store, ModePublic, 1, TargetScene, 42)
	if err != nil {
		t.Fatalf("ResolveScope: %v", err)
	}
	if d.Allowed() {
		t.Error("an unscoped row was allowed on an instance with no default " +
			"library; with no owner there is no library to be a member of, " +
			"and allowing is the fail-open this whole file refuses")
	}
}

func TestResolveScope_PrivateModeRefusesEveryone(t *testing.T) {
	for _, mode := range []Mode{ModePrivate, ModeContribute} {
		for _, isOwner := range []bool{true, false} {
			store := &fakeScopeStore{
				libraryID: 7, ownerID: 1, defaultID: 7, mode: mode,
				hasAccess: !isOwner, // grant the non-owner, to isolate the mode
			}
			userID := int64(2)
			if isOwner {
				userID = 1
			}

			d, err := ResolveScope(context.Background(), store, mode, userID, TargetScene, 42)
			if err != nil {
				t.Fatalf("ResolveScope: %v", err)
			}
			if d.Allowed() {
				t.Errorf("mode %q served media to %v. The mode is the OUTER "+
					"boundary and a grant does not punch through it -- a "+
					"contribute instance that serves files is a public file "+
					"host that thinks it is private", mode, map[bool]string{
					true: "the owner", false: "a granted user",
				}[isOwner])
			}
		}
	}
}

// TestResolveScope_FailsClosedOnEveryStoreError. The refusal set, walked
// exhaustively because the temptation is to handle the ones that are easy and
// the ones that are easy are rarely the ones that matter.
//
// Each case is arranged so that IGNORING the error would ALLOW: the fixture
// user is granted where a grant is involved, and is the owner everywhere else.
// A resolver that shrugged an error off would therefore serve the file in every
// case, so a pass here means something.
//
// The first version of this test used the owner for all four cases, and the
// grant case passed vacuously: ownership short-circuits BEFORE the grant is
// consulted, so the grant read never happened and the planted error was never
// reached. A fixture whose subject never visits the code it names is a fixture
// that reports coverage.
func TestResolveScope_FailsClosedOnEveryStoreError(t *testing.T) {
	boom := errors.New("store is down")

	// ownerID 1; the fixture user is 2 (NOT the owner) wherever a grant is
	// the thing being read, and 1 (the owner) everywhere else.
	cases := []struct {
		name    string
		userID  int64
		breakIt func(*fakeScopeStore)
	}{
		{
			name:   "library lookup fails",
			userID: 1,
			breakIt: func(s *fakeScopeStore) {
				s.libraryErr = boom
			},
		},
		{
			name:   "default lookup fails",
			userID: 1,
			breakIt: func(s *fakeScopeStore) {
				s.libraryID = 0
				s.defaultErr = boom
			},
		},
		{
			name:   "owner lookup fails",
			userID: 1,
			breakIt: func(s *fakeScopeStore) {
				s.ownerErr = boom
			},
		},
		{
			// The non-owner path, so the grant read is actually reached.
			// With ownerID == userID this case is unreachable and passes
			// without testing anything.
			name:   "grant lookup fails",
			userID: 2,
			breakIt: func(s *fakeScopeStore) {
				s.hasAccess = true // ignoring the error would ALLOW
				s.hasAccessErr = boom
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			store := ownerScope()
			store.ownerID = 1
			store.hasAccess = true
			tc.breakIt(store)

			d, _ := ResolveScope(context.Background(), store, ModePublic, tc.userID, TargetScene, 42)
			if d.Allowed() {
				t.Errorf("a store error resulted in ALLOW. An unreadable " +
					"store is not a store with no grants -- it is a store " +
					"whose answer is unknown, and unknown is a refusal")
			}
		})
	}
}

// TestResolveScope_AnInvalidTargetTypeIsRefusedNotLookedUp: the target type
// reaches a table name, so a value outside the closed set must be a refusal
// rather than a lookup.
func TestResolveScope_AnInvalidTargetTypeIsRefusedNotLookedUp(t *testing.T) {
	for _, bad := range []string{"", "scenes", "Scene", "scenes; DROP TABLE tags", "movies"} {
		store := ownerScope()

		d, err := ResolveScope(context.Background(), store, ModePublic, 1, bad, 42)
		if err != nil {
			t.Fatalf("ResolveScope(%q): %v", bad, err)
		}
		if d.Allowed() {
			t.Errorf("target type %q was allowed. It is outside the closed set, "+
				"so it must be a refusal before any query is built", bad)
		}
		if len(store.calls) != 0 {
			t.Errorf("target type %q reached the store (%v). The refusal must "+
				"happen BEFORE the lookup, because the lookup is where the "+
				"untrusted value becomes a table name", bad, store.calls)
		}
	}
}

// TestResolveScope_NilStoreIsRefused: the typed-nil trap, tested on the
// interface where it can be expressed. docs/HANDOFF.md #9 records that a nil
// *sqlite pointer in an interface is a NON-nil interface, so this test is about
// the genuinely-absent case only -- and the comment says so rather than letting
// it look like the concrete case is covered.
func TestResolveScope_NilStoreIsRefused(t *testing.T) {
	d, err := ResolveScope(context.Background(), nil, ModePublic, 1, TargetScene, 42)
	if err != nil {
		t.Fatalf("ResolveScope: %v", err)
	}
	if d.Allowed() {
		t.Error("a nil store served media. NOTE this covers the genuinely " +
			"absent interface only; a nil concrete pointer inside a non-nil " +
			"interface is a different case and the CALLER has to test its own " +
			"concrete pointer for it (HANDOFF.md #9)")
	}
}

// TestResolveScope_ARefusalIsNotAnError pins the distinction the signature
// makes, because a caller that gets it wrong cannot tell the two apart.
//
// A refusal is the system working: 404, and nothing is wrong. A store error is
// the system NOT working: 500, and the operator needs to know. Returning an
// error for both makes a database outage look exactly like a user being
// refused -- and the operator's first conclusion would be that somebody is
// probing their instance, which is the one conclusion that sends them hunting
// for an attack that did not happen.
func TestResolveScope_ARefusalIsNotAnError(t *testing.T) {
	boom := errors.New("store is down")

	t.Run("refusal carries no error", func(t *testing.T) {
		store := &fakeScopeStore{libraryID: 0, defaultID: 0, mode: ModePublic}
		d, err := ResolveScope(context.Background(), store, ModePublic, 1, TargetScene, 42)
		if err != nil {
			t.Errorf("a refusal returned err = %v; the refusal is the system "+
				"working and must arrive as a decision, not an error", err)
		}
		if d.Allowed() {
			t.Error("the refusal case allowed the request")
		}
	})

	t.Run("store failure carries an error", func(t *testing.T) {
		store := &fakeScopeStore{libraryID: 7, ownerID: 1, defaultID: 7, mode: ModePublic, libraryErr: boom}
		_, err := ResolveScope(context.Background(), store, ModePublic, 1, TargetScene, 42)
		if !errors.Is(err, boom) {
			t.Errorf("a store failure returned err = %v, want the underlying "+
				"error. A 500 that does not name its cause is an outage "+
				"somebody has to reproduce before they can fix it", err)
		}
	})
}

func TestValidTargetType(t *testing.T) {
	for _, good := range TargetTypes {
		if !ValidTargetType(good) {
			t.Errorf("ValidTargetType(%q) = false, want true; a target type "+
				"this project scopes must be accepted", good)
		}
	}
	// `movies` is the interesting one: it was renamed to `groups` in
	// migration 65 and the table list in three documents still said movies.
	// A closed set is what makes that rename a test failure rather than a
	// runtime error.
	for _, bad := range []string{"", "movies", "Movies", "scenes", "scene "} {
		if ValidTargetType(bad) {
			t.Errorf("ValidTargetType(%q) = true, want false", bad)
		}
	}
}
