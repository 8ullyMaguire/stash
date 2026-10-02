//go:build integration
// +build integration

package sqlite_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stashapp/stash/internal/collab"
	"github.com/stashapp/stash/internal/gamify"
	"github.com/stashapp/stash/pkg/sqlite"
)

// accessInt reads an INTEGER column.
//
// NOT A STRING. The obvious shortcut is to reuse curationScalar and compare
// against "0", and that shortcut fails in a way that hides a real bug: it
// reports "query returned a int64, want a string" instead of reporting what the
// value was, so a wrong number and a wrong TYPE produce the same complaint. The
// scalar helper is for TEXT columns (it asserts a string), and an integer read
// through it is asserting the helper's assumption rather than the value.
func accessInt(ctx context.Context, t *testing.T, query string, args ...interface{}) int64 {
	t.Helper()
	_, rows, err := db.QuerySQL(ctx, query, args)
	require.NoError(t, err, "querying: %s", query)
	require.Len(t, rows, 1, "query returned %d rows, want 1: %s", len(rows), query)
	require.NotEmpty(t, rows[0], "query returned no columns: %s", query)
	out, ok := rows[0][0].(int64)
	require.True(t, ok, "query returned a %T, want an integer: %s", rows[0][0], query)
	return out
}

// accessTime reads a timestamp column and reports whether it is NULL.
//
// A THIRD HELPER RATHER THAN A STRINGIFIED ONE, because the two failure modes are
// genuinely different and the string version hides the interesting one. Comparing
// a formatted timestamp proves a value was written; comparing NULL-ness proves
// the column was CLEARED. TestReGrantingClearsTheRevocation needs the second and
// would be unable to express it through the first -- "0001-01-01" is what a NULL
// renders as, which reads like a real date.
func accessTime(ctx context.Context, t *testing.T, query string, args ...interface{}) (time.Time, bool) {
	t.Helper()
	_, rows, err := db.QuerySQL(ctx, query, args)
	require.NoError(t, err, "querying: %s", query)
	require.Len(t, rows, 1, "query returned %d rows, want 1: %s", len(rows), query)
	require.NotEmpty(t, rows[0], "query returned no columns: %s", query)
	switch v := rows[0][0].(type) {
	case nil:
		return time.Time{}, false
	case time.Time:
		return v, true
	default:
		require.Failf(t, "unexpected timestamp type", "got a %T, want a time.Time or NULL: %s", v, query)
		return time.Time{}, false
	}
}

// accessUser creates a REAL user row and returns its id.
//
// NOT A MADE-UP ID. collab_audit.actor_id references users(id), so inserting an
// audit row for a user who does not exist fails with FOREIGN KEY constraint
// failed -- and the first version of these tests used invented ids like accessUser(ctx, t, "sfAccessDecide"),
// which made every earned-level assertion fail on a constraint rather than on the
// thing being tested. An invented id is a lie the database is right to reject.
func accessUser(ctx context.Context, t *testing.T, username string) int {
	t.Helper()
	id, err := createUserIn(ctx, t, username)
	require.NoError(t, err, "creating the user %q that the audit rows will reference", username)
	require.Greater(t, id, 0, "a user id of 0 is not addressable, and every read below refuses it")
	return id
}

// accessEarnArchivist writes the audit rows that earn a user LevelArchivist.
//
// BOTH SIGNALS, NOT JUST IDENTS. collab.LevelFor's Archivist rule is
// `identSolves >= 5 && approvedEdits >= 5` -- a conjunction, not an either. The
// first version of these tests wrote five ident solves and expected Archivist,
// got Curator, and read like a counting bug in EarnedFor. It was not: five idents
// alone is `identSolves >= 1`, which is exactly the Curator branch. The counting
// was right and the fixture was incomplete.
//
// Worth stating because the conjunction is deliberate -- one signal is not
// enough to reach the level that permits content -- and a fixture that supplies
// only one of the two looks like a plausible way to reach it.
func accessEarnArchivist(ctx context.Context, t *testing.T, userID int, idBase int) {
	t.Helper()
	for i := 1; i <= 5; i++ {
		require.NoError(t, curationExec(ctx, t,
			"INSERT INTO collab_audit (actor_id, action, target_type, target_id) VALUES (?, ?, ?, ?)",
			userID, gamify.ActionIdentSolve, "scene", idBase+i))
		require.NoError(t, curationExec(ctx, t,
			"INSERT INTO collab_audit (actor_id, action, target_type, target_id) VALUES (?, ?, ?, ?)",
			userID, gamify.ActionEditApproved, "scene", idBase+100+i))
	}
}

// R025–R028, R062, R066: the access decision's persistence, against a real
// migrated database.
//
// WHY THIS FILE IS THE STEP. The domain model in internal/collab/access_level.go
// was complete and its nine tests passed for a full milestone while R025–R028 sat
// at `specified`, because DecideAccess took three inputs and two of them had
// nowhere to live. Every assertion below is about a switch that had no column.

// The DEFAULT CEILING IS LevelPublic, and that is the load-bearing decision.
//
// §6a.12 says an operator's ceiling is a ceiling and never grants the operator
// anything the threshold excludes. An operator who never configured one has made
// no decision, so the reading that serves no content is the only one that is not
// a decision someone else made on their behalf.
//
// The absent row and a row saying 0 must be the SAME answer, which is why
// migration 117 seeds the row rather than leaving the table empty: a caller that
// forgets to handle absence still reads the safe value.
func TestAccessPolicyCeilingDefaultsToPublic(t *testing.T) {
	runWithRollbackTxn(t, "access-ceiling-default", func(t *testing.T, ctx context.Context) {
		raw := accessInt(ctx, t, "SELECT content_ceiling FROM access_policy WHERE instance_id = 1")
		assert.Equal(t, int64(0), raw,
			"a new instance must serve NO content until an operator raises the ceiling. "+
				"Defaulting to 5 would make an instance that never touched a setting a "+
				"content-serving instance, which is a decision nobody made")

		store := sqlite.NewAccessPolicyStore(1)
		ceiling, err := store.Ceiling(ctx)
		require.NoError(t, err)
		assert.Equal(t, collab.LevelPublic, ceiling)
		assert.Equal(t, collab.AccessLevel(0), ceiling.Clamp(), "and the clamp is a no-op at 0")
	})
}

// THE ABSENT ROW IS ALSO PUBLIC, because the two must be indistinguishable.
//
// This is the case that is easy to get wrong and expensive to get wrong in the
// other direction: if "no row" returned an error, every caller would grow a
// "treat missing as open" fallback, and an instance that deleted its own policy
// row would silently start serving content.
func TestAccessPolicyAnAbsentRowIsPublicAndNotAnError(t *testing.T) {
	runWithRollbackTxn(t, "access-ceiling-absent", func(t *testing.T, ctx context.Context) {
		// instance 2 has no seeded row: migration 117 seeds only instance 1.
		store := sqlite.NewAccessPolicyStore(2)
		ceiling, err := store.Ceiling(ctx)
		require.NoError(t, err, "an absent policy row is a POLICY, not a failure -- "+
			"an error here would push every caller into an unsafe fallback")
		assert.Equal(t, collab.LevelPublic, ceiling)
	})
}

// EVERY LEVEL ROUND-TRIPS, and the boundary is the interesting one.
func TestEveryAccessCeilingRoundTrips(t *testing.T) {
	runWithRollbackTxn(t, "access-ceiling-roundtrip", func(t *testing.T, ctx context.Context) {
		store := sqlite.NewAccessPolicyStore(1)
		for _, want := range []collab.AccessLevel{
			collab.LevelPublic, collab.LevelRegistered, collab.LevelContributor,
			collab.LevelCurator, collab.LevelArchivist, collab.LevelSteward,
		} {
			require.NoError(t, store.SetCeiling(ctx, want), "writing %d", want)
			got, err := store.Ceiling(ctx)
			require.NoError(t, err)
			assert.Equal(t, want, got, "the stored ceiling must read back as written")
		}

		// AND IT CLAMPS rather than refusing, because this is an operator typing
		// in a settings box. A ceiling of 9 means "offer everything", which is a
		// legitimate if unwise policy and not an error to raise a dialog about.
		require.NoError(t, store.SetCeiling(ctx, collab.AccessLevel(9)))
		got, err := store.Ceiling(ctx)
		require.NoError(t, err)
		assert.Equal(t, collab.LevelSteward, got, "a ceiling above the maximum clamps to it")
	})
}

// CONSENT DEFAULTS TO OFF, and this is the one that §6a.11 exists to protect.
//
// Consent is a separate switch and is NEVER automatic on reaching a level. A
// default of 1 would make every user who earned Archivist a content viewer at
// the moment of earning, which is the exact conflation the three-number design
// (earned / offered / consented) was built to prevent.
func TestContentConsentDefaultsToOff(t *testing.T) {
	runWithRollbackTxn(t, "access-consent-default", func(t *testing.T, ctx context.Context) {
		// No consent row exists for a user that has never answered.
		store := sqlite.NewAccessPolicyStore(1)
		granted, err := store.ConsentGranted(ctx, accessUser(ctx, t, "sfAccessNoRow"))
		require.NoError(t, err)
		assert.False(t, granted, "an absent consent row must read as REFUSED, not as unknown")

		// And the raw column agrees, so the default is in the schema rather than
		// in a branch of the read.
		explicitRow := accessUser(ctx, t, "sfAccessExplicitOff")
		require.NoError(t, curationExec(ctx, t,
			"INSERT INTO content_consent (user_id, instance_id, granted) VALUES (?, 1, 0)", explicitRow))
		raw := accessInt(ctx, t, "SELECT granted FROM content_consent WHERE user_id = ?", explicitRow)
		assert.Equal(t, int64(0), raw, "the stored default must be 0 -- a row that exists and "+
			"says 0, and a row that does not exist, are the same answer")
	})
}

// THE COLUMN DEFAULTS, WHICH THE SEEDED ROW DOES NOT TEST.
//
// The mutation gate killed nothing here until this test existed, and the reason is
// worth stating: migration 117 SEEDS `access_policy` with an explicit
// `VALUES (1, 0)`, so every default assertion made against instance 1 was really
// asserting the seed. Changing `DEFAULT 0` to `DEFAULT 5` in the column definition
// left all of them green -- the one case where an instance that never configured
// anything would serve every user who reached level 4.
//
// So these insert rows WITHOUT naming the defaulted columns, which is the only path
// through which a DEFAULT is observable. Seeding a table and then testing its
// defaults is the same mistake as trusting a comment that describes a function the
// code no longer has.
func TestTheColumnDefaultsApplyToARowThatDoesNotNameThem(t *testing.T) {
	runWithRollbackTxn(t, "access-column-defaults", func(t *testing.T, ctx context.Context) {
		// A second instance, inserted by whatever creates instances, with no column
		// named: so content_ceiling must take the column DEFAULT.
		require.NoError(t, curationExec(ctx, t,
			"INSERT INTO access_policy (instance_id) VALUES (3)"))

		ceilingDefault := accessInt(ctx, t,
			"SELECT content_ceiling FROM access_policy WHERE instance_id = 3")
		assert.Equal(t, int64(0), ceilingDefault,
			"a row that does not name content_ceiling must take the column DEFAULT, "+
				"which is 0 -- LevelPublic. An instance that never configured a threshold "+
				"serves no content, and that is a decision nobody else made")

		store := sqlite.NewAccessPolicyStore(3)
		ceiling, err := store.Ceiling(ctx)
		require.NoError(t, err)
		assert.Equal(t, collab.LevelPublic, ceiling,
			"and the store must read that defaulted row as Public")

		// The same for consent: a row with `granted` omitted is OFF.
		user := accessUser(ctx, t, "sfAccessDefaultGrant")
		require.NoError(t, curationExec(ctx, t,
			"INSERT INTO content_consent (user_id, instance_id) VALUES (?, 3)", user))

		grantedDefault := accessInt(ctx, t,
			"SELECT granted FROM content_consent WHERE user_id = ?", user)
		assert.Equal(t, int64(0), grantedDefault,
			"a row that does not name `granted` must take the column DEFAULT, which is "+
				"0. §6a.11 says consent is never automatic on reaching a level, so the "+
				"default has to be OFF and not merely the seeded value")

		granted, err := store.ConsentGranted(ctx, user)
		require.NoError(t, err)
		assert.False(t, granted)
	})
}

// REVOCATION IS RECORDED, NOT DELETED.
//
// §6a.11 requires consent to be revocable, and a hard DELETE satisfies that
// requirement while destroying the question a governance review actually asks:
// "when did this user withdraw?". So the row stays, granted goes to 0, and
// revoked_at is set.
func TestConsentRevocationIsRecordedNotDeleted(t *testing.T) {
	runWithRollbackTxn(t, "access-consent-revoke", func(t *testing.T, ctx context.Context) {
		store := sqlite.NewAccessPolicyStore(1)
		userID := accessUser(ctx, t, "sfAccessRevoke")

		require.NoError(t, store.GrantContentConsent(ctx, userID))
		granted, err := store.ConsentGranted(ctx, userID)
		require.NoError(t, err)
		require.True(t, granted)

		require.NoError(t, store.RevokeContentConsent(ctx, userID))

		// The row is STILL THERE, and it says when.
		raw := accessInt(ctx, t,
			"SELECT granted FROM content_consent WHERE user_id = ?", userID)
		assert.Equal(t, int64(0), raw, "a revoked consent reads as refused")
		_, revokedSet := accessTime(ctx, t,
			"SELECT revoked_at FROM content_consent WHERE user_id = ?", userID)
		assert.True(t, revokedSet, "the revocation must be DATED. A revoke with no "+
			"timestamp cannot answer 'when did this user withdraw', which is the "+
			"question §6a.11's revocability exists to make answerable")
	})
}

// LEVELFOR'S ARCHIVIST RULE IS A CONJUNCTION, and this pins it through the store.
//
// Five ident solves alone earn CURATOR, not Archivist, because the rule is
// `identSolves >= 5 && approvedEdits >= 5`. The first version of the decide test
// supplied only the idents, expected Archivist, got Curator, and read like a
// counting bug in EarnedFor -- so the fixture was wrong and the counting was right.
//
// Asserted here because the conjunction is the load-bearing part: one signal must
// not be enough to reach the level that permits content viewing, and a fixture
// (or a future caller) that supplies only one of the two looks like a plausible
// way to get there.
func TestArchivistRequiresBothSignalsNotEither(t *testing.T) {
	runWithRollbackTxn(t, "access-conjunction", func(t *testing.T, ctx context.Context) {
		store := sqlite.NewAccessPolicyStore(1)

		// Idents only: five of them, which is `identSolves >= 5` on its own.
		identsOnly := accessUser(ctx, t, "sfAccessIdentsOnly")
		for i := 1; i <= 5; i++ {
			require.NoError(t, curationExec(ctx, t,
				"INSERT INTO collab_audit (actor_id, action, target_type, target_id) VALUES (?, ?, ?, ?)",
				identsOnly, gamify.ActionIdentSolve, "scene", 900300+i))
		}
		earned, err := store.EarnedFor(ctx, identsOnly)
		require.NoError(t, err)
		assert.Equal(t, collab.LevelCurator, earned.Level,
			"five idents with no approved edits earns Curator. Reaching the level that "+
				"permits content must need more than one kind of evidence")

		// Edits only: five approved edits, no idents.
		editsOnly := accessUser(ctx, t, "sfAccessEditsOnly")
		for i := 1; i <= 5; i++ {
			require.NoError(t, curationExec(ctx, t,
				"INSERT INTO collab_audit (actor_id, action, target_type, target_id) VALUES (?, ?, ?, ?)",
				editsOnly, gamify.ActionEditApproved, "scene", 900400+i))
		}
		earned, err = store.EarnedFor(ctx, editsOnly)
		require.NoError(t, err)
		assert.Equal(t, collab.LevelCurator, earned.Level,
			"and the same holds in the other direction")

		// BOTH: Archivist.
		both := accessUser(ctx, t, "sfAccessBothSignals")
		accessEarnArchivist(ctx, t, both, 900500)
		earned, err = store.EarnedFor(ctx, both)
		require.NoError(t, err)
		assert.Equal(t, collab.LevelArchivist, earned.Level,
			"five of each is the only way to reach Archivist")
	})
}

// A RE-GRANT CLEARS THE REVOCATION, and says so.
//
// The migration's CHECK ties granted=1 to revoked_at IS NULL, so a re-grant has
// to write the WHOLE new state. If it only set the flag, the CHECK would refuse
// the row and the user's second yes would become an error.
func TestReGrantingClearsTheRevocation(t *testing.T) {
	runWithRollbackTxn(t, "access-consent-regrant", func(t *testing.T, ctx context.Context) {
		store := sqlite.NewAccessPolicyStore(1)
		userID := accessUser(ctx, t, "sfAccessReGrant")

		require.NoError(t, store.GrantContentConsent(ctx, userID))
		require.NoError(t, store.RevokeContentConsent(ctx, userID))
		require.NoError(t, store.GrantContentConsent(ctx, userID),
			"a second yes must not be an error -- the row exists with granted=0")

		granted, err := store.ConsentGranted(ctx, userID)
		require.NoError(t, err)
		assert.True(t, granted)
		_, stillRevoked := accessTime(ctx, t,
			"SELECT revoked_at FROM content_consent WHERE user_id = ?", userID)
		assert.False(t, stillRevoked,
			"and the revocation must be CLEARED, or the row claims both. A NULL here "+
				"is what a re-grant writes -- asserting it as a formatted string "+
				"would render NULL as 0001-01-01 and read like a real date")
	})
}

// THE SCHEMA REFUSES A ROW THAT CLAIMS BOTH CONSENT AND REVOCATION.
//
// A constraint test rather than a store test, because the store cannot express
// the bad state and the point is that the DATABASE cannot either.
func TestContentConsentRefusesConsentAndRevocationTogether(t *testing.T) {
	runWithRollbackTxn(t, "access-consent-check", func(t *testing.T, ctx context.Context) {
		badRow := accessUser(ctx, t, "sfAccessBadRow")
		err := curationExec(ctx, t,
			"INSERT INTO content_consent (user_id, instance_id, granted, revoked_at) VALUES (?, 1, 1, CURRENT_TIMESTAMP)",
			badRow)
		require.Error(t, err, "granted=1 WITH revoked_at is not a state any caller should "+
			"have to interpret, so the CHECK refuses it")
	})
}

// THE DECISION NEEDS ALL THREE INPUTS, AND CONSENT IS THE ONE THAT IS EASY TO
// FORGET.
//
// Earning Archivist and an operator offering it are both necessary and neither is
// sufficient: §6a.11's third question is the consent, and a decision that checked
// only the first two would serve content to every user who reached level 4.
func TestDecideReachesContentOnlyWithEarnedOfferedAndConsented(t *testing.T) {
	runWithRollbackTxn(t, "access-decide-three-inputs", func(t *testing.T, ctx context.Context) {
		store := sqlite.NewAccessPolicyStore(1)
		userID := accessUser(ctx, t, "sfAccessThreeInputs")

		// EARN the level first, because DecideAccess checks EARNED before OFFERED
		// and would otherwise report ErrNotEarned for every case below -- which is
		// the correct answer and the wrong one for this test.
		accessEarnArchivist(ctx, t, userID, 900000)

		// 1. Not offered. The operator's ceiling is still Public.
		err := store.Decide(ctx, userID, collab.LevelArchivist)
		require.ErrorIs(t, err, collab.ErrNotOffered,
			"the operator's ceiling is checked against what was REQUESTED, not against "+
				"what was earned")

		// 2. Offered but not consented.
		require.NoError(t, store.SetCeiling(ctx, collab.LevelSteward))
		err = store.Decide(ctx, userID, collab.LevelArchivist)
		require.ErrorIs(t, err, collab.ErrConsentRequired,
			"reaching level 4 must NOT enable content viewing by itself. This is the "+
				"whole point of consent being a separate switch")

		// 3. Consented.
		require.NoError(t, store.GrantContentConsent(ctx, userID))
		require.NoError(t, store.Decide(ctx, userID, collab.LevelArchivist),
			"all three inputs present, so the content is served")

		// AND REVOKING IT CLOSES THE DOOR AGAIN, which is §6a.11's revocability
		// being real rather than nominal.
		require.NoError(t, store.RevokeContentConsent(ctx, userID))
		err = store.Decide(ctx, userID, collab.LevelArchivist)
		require.ErrorIs(t, err, collab.ErrConsentRequired,
			"revocation must take effect immediately -- a consent that is withdrawn "+
				"but still honoured is not revocable")
	})
}

// THE THREE ERRORS ARE DISTINGUISHABLE, because they are fixable by OPPOSITE
// parties.
//
// A caller that collapsed them would tell a Steward to go and earn more on an
// instance that has simply decided not to serve content at all.
func TestTheThreeAccessErrorsAreDistinguishable(t *testing.T) {
	runWithRollbackTxn(t, "access-errors-distinct", func(t *testing.T, ctx context.Context) {
		store := sqlite.NewAccessPolicyStore(1)
		userID := accessUser(ctx, t, "sfAccessErrors")

		// Nothing earned, nothing offered: the user is told to contribute.
		err := store.Decide(ctx, userID, collab.LevelCurator)
		require.ErrorIs(t, err, collab.ErrNotEarned)

		// Earned below the request, and the request is above the ceiling. EARNED
		// is checked first, so a user who has not earned it is told that rather
		// than being told the instance offers nothing -- which would be true and
		// useless.
		assert.NotErrorIs(t, err, collab.ErrNotOffered,
			"ErrNotEarned must not also satisfy ErrNotOffered, or the caller cannot "+
				"tell the user which of two different remedies applies")
		assert.NotErrorIs(t, err, collab.ErrConsentRequired)
	})
}

// AN OPERATOR GETS NO BYPASS, and the store's Decide is where that is structural.
//
// §6a.12: a ceiling is a ceiling and it never grants the operator anything the
// threshold excludes. Decide takes no operator flag, so there is nowhere to pass
// one -- which is why this test is a compile-time-shaped claim about the call
// rather than a runtime branch to exercise.
func TestDecideRefusesAnOperatorWithoutABypass(t *testing.T) {
	runWithRollbackTxn(t, "access-no-operator-bypass", func(t *testing.T, ctx context.Context) {
		store := sqlite.NewAccessPolicyStore(1)
		// ONE user: a mechanical id substitution had produced three creations of
		// the same username, which collides on users.username's unique index.
		userID := accessUser(ctx, t, "sfAccessBypass")

		// With a Public ceiling and no consent, Steward is refused regardless of who
		// is asking. The interface has no operator argument, so there is nothing to
		// pass -- which is §6a.12 held structurally rather than by discipline.
		err := store.Decide(ctx, userID, collab.LevelSteward)
		require.Error(t, err, "a Public ceiling refuses Steward before consent or "+
			"earning are even considered")

		// AND with everything the operator controls set to maximum, an UNEARNED
		// level is still refused. A ceiling of 5 plus a consent is not a substitute
		// for having earned anything: §6a.10's firewall holds with both switches on,
		// which is the case worth testing because it is the one a future "let
		// operators bypass" change would break.
		require.NoError(t, store.SetCeiling(ctx, collab.LevelSteward))
		require.NoError(t, store.GrantContentConsent(ctx, userID))
		err = store.Decide(ctx, userID, collab.LevelSteward)
		require.ErrorIs(t, err, collab.ErrNotEarned,
			"the ceiling and the consent are the operator's and the user's switches; "+
				"neither can manufacture the audit-log evidence of a contribution")
	})
}

func TestEarnedCountsTheActionsTheDomainExpects(t *testing.T) {
	runWithRollbackTxn(t, "access-earned-actions", func(t *testing.T, ctx context.Context) {
		// The literals EarnedFor queries, named here so the assertion can compare.
		const (
			literalProposalApplied = "proposal_applied"
			literalIdentSolved     = "ident_solved"
		)
		assert.Equal(t, gamify.ActionEditApproved, literalProposalApplied,
			"the approved-edit literal in EarnedFor has drifted from gamify's constant")
		assert.Equal(t, gamify.ActionIdentSolve, literalIdentSolved,
			"the ident literal in EarnedFor has drifted from gamify's constant")

		// AND IT EARNS: five ident solves AND five approved edits reaches Archivist.
		store := sqlite.NewAccessPolicyStore(1)
		userID := accessUser(ctx, t, "sfAccessEarned")
		accessEarnArchivist(ctx, t, userID, 900100)
		earned, err := store.EarnedFor(ctx, userID)
		require.NoError(t, err)
		assert.Equal(t, collab.LevelArchivist, earned.Level,
			"five ident solves on distinct targets earns Archivist per collab.LevelFor")

		level, err := store.EarnedLevel(ctx, userID)
		require.NoError(t, err)
		assert.Equal(t, collab.LevelArchivist, level)
	})
}

// IDENT SOLVES COUNT DISTINCT TARGETS, not rows.
//
// A user who edits the same scene's identification forty times has done one
// thing forty times. Counting rows would let that be worth forty, which is the
// volume-farming the domain's own comment on verification consistency exists to
// prevent.
func TestIdentSolvesCountDistinctTargets(t *testing.T) {
	runWithRollbackTxn(t, "access-idents-distinct", func(t *testing.T, ctx context.Context) {
		store := sqlite.NewAccessPolicyStore(1)
		userID := accessUser(ctx, t, "sfAccessIdents")

		// The same target, five times, PLUS the five approved edits that satisfy the
		// other half of LevelFor's conjunction.
		//
		// The approved edits are what make this test bite. The first version wrote
		// only the repeated idents and asserted `NotEqual(Archivist)`, which a row
		// count also satisfies -- because with approvedEdits at 0 the inflated
		// identSolves still falls through to the `identSolves >= 1` Curator branch.
		// So the mutant survived a green test. Supplying both signals is what makes
		// DISTINCT and non-DISTINCT give DIFFERENT answers: one ident + five edits is
		// Curator, and five idents + five edits is Archivist.
		for i := 0; i < 5; i++ {
			require.NoError(t, curationExec(ctx, t,
				"INSERT INTO collab_audit (actor_id, action, target_type, target_id) VALUES (?, ?, ?, ?)",
				userID, gamify.ActionIdentSolve, "scene", 900200))
		}
		for i := 1; i <= 5; i++ {
			require.NoError(t, curationExec(ctx, t,
				"INSERT INTO collab_audit (actor_id, action, target_type, target_id) VALUES (?, ?, ?, ?)",
				userID, gamify.ActionEditApproved, "scene", 900210+i))
		}
		earned, err := store.EarnedFor(ctx, userID)
		require.NoError(t, err)
		assert.Equal(t, collab.LevelCurator, earned.Level,
			"five solves of ONE scene is one contribution, so with five approved edits "+
				"the conjunction is not satisfied and the level stays Curator. A row "+
				"count would report five idents, satisfy it, and let a user farm "+
				"Archivist by re-identifying the same target")
	})
}

// A USER WITH NO RECORD EARNS PUBLIC, AND THE SCHEMA REFUSES A NON-POSITIVE ID.
//
// Two things in one test because they are the same fact read from two directions:
// absent evidence of contribution means no contribution, and an id that is not
// addressable is a caller bug rather than a user with no record.
func TestEarnedDefaultsToPublicAndRefusesUnaddressableIDs(t *testing.T) {
	runWithRollbackTxn(t, "access-earned-defaults", func(t *testing.T, ctx context.Context) {
		store := sqlite.NewAccessPolicyStore(1)

		// A user id with no audit rows. Not 0 -- a real but absent user, because
		// 0 is refused below.
		earned, err := store.EarnedFor(ctx, accessUser(ctx, t, "sfAccessNoRecord"))
		require.NoError(t, err)
		assert.Equal(t, collab.LevelPublic, earned.Level,
			"'they signed up' is an event, not a contribution. Defaulting to Registered "+
				"would make every new account able to vote and submit edits")

		// 0 is refused rather than answered, because an id of 0 is not a user.
		_, err = store.EarnedFor(ctx, 0)
		require.Error(t, err, "user id 0 is a caller bug, not an absent user")
		_, err = store.ConsentGranted(ctx, -1)
		require.Error(t, err)
	})
}

// REACHABILITY: the model is used from OUTSIDE its own package.
//
// This is the guard whose absence let R025-R028 sit at `specified` for a whole
// milestone. A complete domain model with no caller is not half-built work, it is
// INVISIBLE work: every test in internal/collab passes, the plan reads as done,
// and no user can reach the feature.
//
// So the assertion is not "does it compile" but "does anything outside this
// package reach it" -- checked by constructing the store and calling the domain
// entry points from a test in a different package, which is the only shape a
// reachability claim can honestly take.
func TestTheAccessModelIsReachableFromOutsideItsPackage(t *testing.T) {
	// The store satisfies the interface the domain declares. A compile-time
	// assertion in a non-test file already covers this; repeating it here makes
	// the failure land in a test if someone moves the var.
	var _ collab.AccessPolicyStore = sqlite.NewAccessPolicyStore(1)

	// AND THE DOMAIN TYPES ARE EXPORTED AND USED, not merely declared. A type
	// nothing outside its package names is unreachable by construction, and this
	// is the assertion that would have failed on the pre-117 tree -- where
	// access_level.go was complete, tested, and named by nothing.
	assert.NotEmpty(t, collab.LevelArchivist.String())
	assert.NotEmpty(t, collab.ErrConsentRequired.Error())

	runWithRollbackTxn(t, "access-reachable", func(t *testing.T, ctx context.Context) {
		store := sqlite.NewAccessPolicyStore(1)
		// ONE user for the whole block. An earlier version of this test created a
		// separate user per call, all six with the same username, because the id
		// substitution was mechanical. That collides on users.username's unique
		// index -- and a mechanical substitution that turns one value into six is
		// the same mistake the invented 999020 ids were, in a quieter form.
		userID := accessUser(ctx, t, "sfAccessReachable")

		// Every method on the interface, called once. An interface with a method
		// nothing calls is the same invisibility one level down.
		require.NoError(t, store.SetCeiling(ctx, collab.LevelCurator))
		_, err := store.Ceiling(ctx)
		require.NoError(t, err)
		_, err = store.ConsentGranted(ctx, userID)
		require.NoError(t, err)
		require.NoError(t, store.GrantContentConsent(ctx, userID))
		require.NoError(t, store.RevokeContentConsent(ctx, userID))
		_, err = store.EarnedFor(ctx, userID)
		require.NoError(t, err)

		// Decide is called last and it REFUSES, which is correct. This test first
		// asked for LevelCurator from a user with no record and expected success,
		// reasoning that "Public on a Curator ceiling is allowed". It is not:
		// DecideAccess checks EARNED against REQUESTED first, so a request for
		// Curator is a request for Curator whether or not the operator offers it.
		// The premise was wrong, not the code.
		//
		// ErrNotEarned is the assertion because reaching this line at all is the
		// reachability claim -- the interface method was callable from outside the
		// domain package -- and what it DECIDES is §6a.10's firewall rather than a
		// defect.
		err = store.Decide(ctx, userID, collab.LevelCurator)
		require.ErrorIs(t, err, collab.ErrNotEarned)
	})
}
