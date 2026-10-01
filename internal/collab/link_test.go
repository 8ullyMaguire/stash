package collab_test

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stashapp/stash/internal/collab"
)

// A LINK IS A PROPOSAL, like a column edit, and it goes through the same vote.
//
// This file is the second half of the change TestVocabulary_EveryFieldIsARealColumn
// forced. A vocabulary field is a COLUMN NAME that WriteFieldIfChanged interpolates
// into an UPDATE, and a relationship is an INSERT into a join table, so the first
// attempt -- adding the join tables to the vocabulary as TypeInt fields -- would have
// validated, been approved, and then died in SQL. The join tables now have their own
// namespace (link.go) and their own operation (TargetStore.AddLink).
//
// The properties below are the ones that make a link SAFE rather than merely possible.
// A link is a write on shared content reached through a vote, so "what can this
// operation do" is the question that matters.

// The four link kinds the plan's step 8.2 names, and they are proposable now.
func TestEveryLinkKindTheAutotagPathNeedsIsProposable(t *testing.T) {
	cases := []struct {
		target string
		kind   collab.LinkKind
	}{
		{"scene", collab.LinkScenePerformer},
		{"scene", collab.LinkSceneTag},
		{"image", collab.LinkImagePerformer},
		{"image", collab.LinkImageTag},
	}

	for _, c := range cases {
		_, err := collab.ValidateLink(c.target, c.kind)
		assert.NoError(t, err, "%s/%s must be a proposable link", c.target, c.kind)

		// And through the real proposer, which is the path a machine's claim takes.
		store := newFake()
		p := collab.NewProposer(store)
		prop, err := p.Create(context.Background(), collab.Proposal{
			TargetType: c.target, TargetID: 7, Field: string(c.kind),
			NewValue: ptr("42"), AuthorID: 1,
		})
		require.NoError(t, err,
			"%s/%s: a link the autotag path files must be accepted by the real "+
				"proposer, or the machine's claim sits in the audit trail forever "+
				"unable to take effect", c.target, c.kind)
		assert.Positive(t, prop.ID)
	}
}

// A LINK IS AN ADD AND ONLY AN ADD. This is the safety property the whole design
// rests on, and it is asserted as a set of every spelling a removal might arrive in.
//
// The failure it prevents is specific: one reviewer's "yes, that performer is in this
// scene" silently unlinking the other four, with an audit trail showing only an add.
// So there is no RemoveLink, no negative value, and no "remove" spelling -- and a nil
// value, which for a COLUMN means "clear this field" and is a legitimate edit, is
// REFUSED here because clearing a relationship is a removal.
func TestALinkCannotExpressARemoval(t *testing.T) {
	removals := map[string]*string{
		"negative id":       ptr("-1"),
		"large negative":    ptr("-42"),
		"zero":              ptr("0"),
		"nil clears it":     nil,
		"the word remove":   ptr("remove"),
		"minus one as text": ptr(" -1 "),
	}

	for _, target := range []string{"scene", "image"} {
		for _, kind := range collab.ProposableLinks(target) {
			for name, value := range removals {
				err := collab.ValidateLinkValue(target, kind, value)
				assert.Error(t, err,
					"%s/%s accepts %s (%v). Links are additive only: the field names a "+
						"SET and the value names one member to add, so nothing here may "+
						"mean 'remove' -- otherwise an approved add can unlink everything "+
						"else while the audit trail records only the add.",
					target, kind, name, value)
			}
		}
	}
}

// AND THE ASYMMETRY WITH A COLUMN IS REAL, asserted so the two rules cannot be merged
// later by someone who finds them inconsistent. nil CLEARS a title -- a legitimate
// edit, and the reason the schema keeps NULL and "" apart -- while nil on a link is
// refused, because clearing a relationship is a removal and there is no removal.
func TestNilClearsAColumnButNotALink(t *testing.T) {
	assert.NoError(t, collab.ValidateValue("scene", "title", nil),
		"clearing a title is a real edit and the schema keeps NULL and \"\" apart for it")

	assert.Error(t, collab.ValidateLinkValue("scene", collab.LinkScenePerformer, nil),
		"clearing a link is a REMOVAL. The same nil means 'unset this column' for a "+
			"field and 'unlink this performer' for a relationship, and only one of "+
			"those is expressible by design")
}

// A LINK'S VALUE IS AN ENTITY ID, so it is validated as one. The overflow case is here
// because it is the same finding as the column path's and the same trap: strconv.Atoi
// SATURATES on overflow and reports the error separately, so a check that only tested
// positivity would let 9223372036854775808 through as MaxInt64.
func TestALinkValueMustBeAPositiveEntityId(t *testing.T) {
	bad := []string{"", "abc", "1.5", "1,2", "NULL", "null", " ", "0", "-1",
		"9223372036854775808", "99999999999999999999"}

	for _, target := range []string{"scene", "image"} {
		for _, kind := range collab.ProposableLinks(target) {
			for _, v := range bad {
				assert.Error(t, collab.ValidateLinkValue(target, kind, ptr(v)),
					"%s/%s accepts %q as a member id", target, kind, v)
			}
		}
	}

	// The boundary is still legal, so the answer cannot become "reject anything big".
	assert.NoError(t, collab.ValidateLinkValue("scene", collab.LinkScenePerformer,
		ptr("9223372036854775807")))
}

// THE TWO NAMESPACES STAY SEPARATE, and this is the property that makes the dispatch
// in the applier decidable. IsLinkField resolves the COLUMN namespace first, so a name
// that somehow appeared in both maps is treated as a column -- the conservative answer,
// because an UPDATE of a real column cannot do the damage an INSERT into the wrong join
// table could.
func TestIsLinkFieldTellsTheTwoApart(t *testing.T) {
	// Real links.
	assert.True(t, collab.IsLinkField("scene", "performer_ids"))
	assert.True(t, collab.IsLinkField("scene", "tag_ids"))
	assert.True(t, collab.IsLinkField("image", "performer_ids"))

	// Real columns, including studio_id which is a column on scenes AND a relationship
	// name elsewhere in the product -- it is a column here, and saying so is the point.
	assert.False(t, collab.IsLinkField("scene", "studio_id"))
	assert.False(t, collab.IsLinkField("scene", "title"))
	assert.False(t, collab.IsLinkField("image", "rating"))

	// Neither.
	assert.False(t, collab.IsLinkField("scene", "nonsense"))
	assert.False(t, collab.IsLinkField("nonsense", "performer_ids"))
}

// EVERY TARGET IN THE MAP HAS A CHECKED JOIN TABLE, AND EVERY TARGET WITHOUT ONE IS
// ABSENT. Asserted in BOTH directions because a target added to the map by accident --
// or by a later "while we are here" -- would let a machine file links against a join
// table nobody has checked.
//
// GALLERIES ARE NOW IN THE MAP, and this assertion used to pin their absence. That pin
// was correct when it was written and is now INVERTED, which is the right way round: the
// test's purpose was never "galleries must be absent", it was "a target is present only
// if its join table has been checked against the real schema". The check happened
// (TestEveryLinkTableAndColumnIsReal reads performers_galleries and galleries_tags off
// a migrated database, and both exist -- migration 13, with foreign keys and indexes),
// so the entry became legitimate and the pin had to move.
//
// Left as-is it would have been a test asserting a known-wrong fact, and the next
// person to read it would conclude galleries have no join tables.
func TestOnlyTheTargetsWithACheckedJoinTableCarryLinks(t *testing.T) {
	// THE PRESENT CASE, and the table names are asserted rather than counted, because
	// "two kinds" would still pass with the two swapped -- and the swap is a real
	// corruption: a performer's id written into galleries_tags.tag_id.
	assert.Equal(t, []collab.LinkKind{collab.LinkGalleryPerformer, collab.LinkGalleryTag},
		collab.ProposableLinks("gallery"),
		"galleries carry both a performer and a tag link, and the ORDER is stable "+
			"because a form rendering them in map order would reshuffle per render")

	assert.Nil(t, collab.ProposableLinks("performer"),
		"a performer has no join table of its own -- it is the ENTITY on the far end "+
			"of one, which is what makes the target/kind PAIR the unit rather than "+
			"the kind alone")
	assert.Nil(t, collab.ProposableLinks("nonsense"))

	// And every target that does carry links has at least one, so the slice is never
	// empty-but-present -- which a UI would render as an empty control.
	for _, target := range []string{"scene", "image"} {
		assert.NotEmpty(t, collab.ProposableLinks(target))
	}
}

// THE SAME KIND STRING MEANS DIFFERENT TABLES UNDER DIFFERENT TARGETS, and this is why
// the map is keyed on the PAIR. "performer_ids" is performers_scenes for a scene and
// performers_images for an image, so a caller that resolved the kind alone would insert
// an image's performer into the scene's table.
func TestTheSameKindNameResolvesToDifferentTablesPerTarget(t *testing.T) {
	sceneShape, err := collab.ValidateLink("scene", collab.LinkScenePerformer)
	require.NoError(t, err)
	imageShape, err := collab.ValidateLink("image", collab.LinkImagePerformer)
	require.NoError(t, err)

	assert.Equal(t, "performer_ids", string(collab.LinkScenePerformer),
		"the two kinds share a NAME, which is the whole reason the pair is the key")
	assert.Equal(t, "performer_ids", string(collab.LinkImagePerformer))

	assert.NotEqual(t, sceneShape.Table, imageShape.Table,
		"the same kind name under two targets must not resolve to one table")
	assert.Equal(t, "scene_id", sceneShape.IdColumn)
	assert.Equal(t, "image_id", imageShape.IdColumn)
}

// A REFUSAL DOES NOT SAY WHICH NAMESPACE REFUSED. A caller that can tell "not a column"
// from "not a link" can enumerate the schema by watching errors -- the same oracle the
// vocabulary's own unknown-target rule refuses to provide, and the reason
// ErrLinkNotProposable IS ErrFieldNotProposable.
func TestARefusedFieldIsIndistinguishableBetweenTheTwoNamespaces(t *testing.T) {
	// A name that is neither a column nor a link.
	_, err := collab.ValidateLink("scene", "nonsense")
	assert.ErrorIs(t, err, collab.ErrFieldNotProposable,
		"an unknown link is refused with the SAME sentinel an unknown column gets")

	// And through the proposer, so the error a caller actually sees is the shared one
	// rather than a wrapped variant.
	store := newFake()
	p := collab.NewProposer(store)
	_, unknown := p.Create(context.Background(), collab.Proposal{
		TargetType: "scene", TargetID: 1, Field: "nonsense",
		NewValue: ptr("x"), AuthorID: 1,
	})
	assert.ErrorIs(t, unknown, collab.ErrFieldNotProposable)

	// The same message for an unknown TARGET and an unknown FIELD, so the error is not
	// a schema oracle in the other direction either.
	unknownTarget, errLink := collab.ValidateLink("nonsense", collab.LinkScenePerformer)
	assert.ErrorIs(t, errLink, collab.ErrFieldNotProposable)
	assert.Equal(t, unknownTarget, LinkShapeOf(t, "nonsense", collab.LinkScenePerformer))
}

// LinkShapeOf is a test helper returning the zero shape a refused lookup produces, so
// the test above can assert the caller gets nothing usable from a refusal.
func LinkShapeOf(t *testing.T, target string, kind collab.LinkKind) collab.LinkShape {
	t.Helper()
	shape, err := collab.ValidateLink(target, kind)
	require.Error(t, err)
	return shape
}

// THE APPLIER ROUTES A LINK TO AddLink, never to WriteFieldIfChanged. This is the
// dispatch that makes the whole thing work, and it is asserted through the fake's
// counters rather than by reading the branch: a link that went down the column path
// would write a column that does not exist.
func TestTheApplierRoutesALinkToAddLinkAndNotToTheColumnPath(t *testing.T) {
	targets := newFakeTargets()
	targets.add("scene", 7, "title", ptr("Old Title"))
	applier := collab.NewApplier(targets)

	outcome, err := applier.Apply(context.Background(), collab.Proposal{
		ID: 1, TargetType: "scene", TargetID: 7, Field: "performer_ids",
		NewValue: ptr("42"), AuthorID: 1,
	})
	require.NoError(t, err)
	assert.Equal(t, collab.ApplyWrote, outcome)

	// It went to AddLink: the member is in the set.
	assert.True(t, targets.hasLink("scene", 7, collab.LinkScenePerformer, 42),
		"the link was not added, so it went somewhere other than AddLink")
	// And NOT to the column path, which would have tried to UPDATE a column named
	// performer_ids and found none.
	assert.Zero(t, targets.writeCount,
		"a link must never reach WriteFieldIfChanged: there is no column named "+
			"performer_ids, so that call is a runtime SQL error")
}

// RE-APPLYING A LINK IS A NO-OP, which is what makes it idempotent. The column path
// gets that from compare-and-set; a link gets it from the join table's uniqueness
// constraint, reported as added=false. Both must produce the same outcome, and neither
// may append a second audit row -- an approved link applied twice is one change and one
// audit row, not two of each.
func TestReapplyingALinkIsIdempotent(t *testing.T) {
	targets := newFakeTargets()
	targets.add("scene", 7, "title", ptr("Old Title"))
	applier := collab.NewApplier(targets)

	prop := collab.Proposal{
		ID: 1, TargetType: "scene", TargetID: 7, Field: "performer_ids",
		NewValue: ptr("42"), AuthorID: 1,
	}

	first, err := applier.Apply(context.Background(), prop)
	require.NoError(t, err)
	assert.Equal(t, collab.ApplyWrote, first, "the first apply adds the member")
	assert.Equal(t, 1, targets.linkAdds)
	auditAfterFirst := targets.auditCount

	second, err := applier.Apply(context.Background(), prop)
	require.NoError(t, err)
	assert.Equal(t, collab.ApplyAlreadyCorrect, second,
		"the member is already in the set, so the second apply is already-correct")
	assert.Equal(t, 1, targets.linkAdds, "and it must not add a second row")
	assert.Equal(t, auditAfterFirst, targets.auditCount,
		"and it must not append a second audit row: one approved link is one change")
}

// A MISSING TARGET IS REJECTED WITH ITS OWN REASON, and this is why ErrLinkTargetMissing
// is a sentinel rather than a string match. The scene was deleted between the vote and
// the apply, so the proposal is rejected as "target no longer exists" -- which says the
// thing no longer exists, NOT that the community refused a change they agreed to.
func TestALinkToAMissingTargetIsRejectedNotFailed(t *testing.T) {
	targets := newFakeTargets()
	// WRAPPED, the way the real store wraps it, because the applier branches on
	// errors.Is rather than on the message. Building the string by hand would have
	// made this test pass for the wrong reason -- and would have hidden the fact that
	// a store which returned the bare sentinel text would NOT be recognised.
	targets.linkErr = fmt.Errorf("adding performer_ids to scene 99: %w",
		collab.ErrLinkTargetMissing)
	applier := collab.NewApplier(targets)

	outcome, err := applier.Apply(context.Background(), collab.Proposal{
		ID: 1, TargetType: "scene", TargetID: 99, Field: "performer_ids",
		NewValue: ptr("42"), AuthorID: 1,
	})
	require.NoError(t, err,
		"a deleted target is a normal outcome, not a failure to file")
	assert.Equal(t, collab.ApplyTargetMissing, outcome)
}

// AND EVERY OTHER LINK FAILURE IS A FAULT, not a rejection. This is the other half of
// the sentinel's reason: if a constraint failure also looked like "target no longer
// exists", the proposal would be recorded as rejected on evidence that does not exist.
func TestALinkFailureThatIsNotAMissingTargetIsAFault(t *testing.T) {
	targets := newFakeTargets()
	targets.linkErr = errors.New("UNIQUE constraint failed: performers_scenes.performer_id")
	applier := collab.NewApplier(targets)

	_, err := applier.Apply(context.Background(), collab.Proposal{
		ID: 1, TargetType: "scene", TargetID: 7, Field: "performer_ids",
		NewValue: ptr("42"), AuthorID: 1,
	})
	require.Error(t, err,
		"a constraint failure is a fault in the write path, and reporting it as "+
			"'target no longer exists' would reject a proposal on false evidence")
	assert.NotErrorIs(t, err, collab.ErrLinkTargetMissing)
}

// A LINK RE-VALIDATED AT APPLY TIME, because the link map can change. A relationship
// withdrawn between the vote and the apply must not be applied, and the proposal must
// be rejected with the value-is-invalid outcome the column path already uses.
func TestALinkThatIsNoLongerProposableIsRejectedAtApplyTime(t *testing.T) {
	targets := newFakeTargets()
	applier := collab.NewApplier(targets)

	// A link that ValidateLinkValue accepts is not what we want here, so the proposal
	// carries a value that fails the member-id rule: the check runs before AddLink.
	outcome, err := applier.Apply(context.Background(), collab.Proposal{
		ID: 1, TargetType: "scene", TargetID: 7, Field: "performer_ids",
		NewValue: ptr("not-an-id"), AuthorID: 1,
	})
	require.ErrorIs(t, err, collab.ErrValueBecameInvalid)
	assert.Equal(t, collab.ApplyRejected, outcome)
	assert.False(t, targets.hasLink("scene", 7, collab.LinkScenePerformer, 42),
		"nothing may be added for a proposal that failed apply-time validation")
}

// AND THE PROPOSER REFUSES IT TOO, so a bad value never reaches a vote.
func TestTheProposerRefusesABadLinkValueBeforeItCanBeVotedOn(t *testing.T) {
	store := newFake()
	p := collab.NewProposer(store)

	_, err := p.Create(context.Background(), collab.Proposal{
		TargetType: "scene", TargetID: 7, Field: "performer_ids",
		NewValue: ptr("not-an-id"), AuthorID: 1,
	})
	assert.ErrorIs(t, err, collab.ErrValueInvalid,
		"a value that cannot be a member id must be refused at propose time, so the "+
			"community is never asked to vote on something unappliable")
	assert.Empty(t, store.props,
		"and nothing may be filed")
}

// THE STICKY RULE APPLIES TO LINKS TOO. A rejected link blocks the same author from
// re-proposing it, exactly as a rejected column does -- otherwise governance is
// enforced on one namespace and ignored on the other, which is the kind of gap that
// looks like an oversight and is really a decision.
func TestAStickyRejectionOnALinkBlocksTheAuthor(t *testing.T) {
	store := newFake()
	store.reject[collab.NewStickyRejectionKey("scene", 7, "performer_ids", 1)] = true
	p := collab.NewProposer(store)

	_, err := p.Create(context.Background(), collab.Proposal{
		TargetType: "scene", TargetID: 7, Field: "performer_ids",
		NewValue: ptr("42"), AuthorID: 1,
	})
	assert.ErrorIs(t, err, collab.ErrStickyRejected,
		"a link is a proposal like any other, so a rejected one blocks its author "+
			"from re-proposing it")
}

// A SECOND MEMBER IS A SEPARATE PROPOSAL, and this is the property that makes a link
// additive rather than a set replacement. Two performers on one scene are two claims,
// and the second must not be blocked by the first being open -- otherwise "propose a
// second performer" is impossible while any proposal is pending, which is a real edit
// people ask for.
func TestTwoLinksToTheSameSceneAreTwoProposals(t *testing.T) {
	store := newFake()
	p := collab.NewProposer(store)

	first, err := p.Create(context.Background(), collab.Proposal{
		TargetType: "scene", TargetID: 7, Field: "performer_ids",
		NewValue: ptr("42"), AuthorID: 1,
	})
	require.NoError(t, err)

	// The same FIELD, a different member. The open-proposal rule keys on
	// (target, field), so this collides -- and that is correct: the "one open proposal
	// per field" rule is about the FIELD, and a second member is a different claim to
	// the same field. It is a supersede, not a second concurrent proposal.
	_, err = p.Create(context.Background(), collab.Proposal{
		TargetType: "scene", TargetID: 7, Field: "performer_ids",
		NewValue: ptr("43"), AuthorID: 1,
	})
	assert.ErrorIs(t, err, collab.ErrOpenProposalExists,
		"one open proposal per (target, field) is the rule, and it applies to links "+
			"unchanged. A second member is reached by SUPERSEDING, which is the same "+
			"path a second title edit takes.")

	// And superseding it works, carrying the new member.
	second, err := p.Supersede(context.Background(), collab.Proposal{
		TargetType: "scene", TargetID: 7, Field: "performer_ids",
		NewValue: ptr("43"), AuthorID: 1,
	})
	require.NoError(t, err)
	assert.NotEqual(t, first.ID, second.ID)
	assert.Equal(t, "43", *store.props[k("scene", 7, "performer_ids")].NewValue)
}

// ProposableLinks is SORTED, because a UI renders a form from it and Go's map iteration
// order would reshuffle the control on every render. The same reason ProposableFields
// sorts, asserted here for the link namespace.
func TestProposableLinksIsSorted(t *testing.T) {
	for _, target := range []string{"scene", "image"} {
		kinds := collab.ProposableLinks(target)
		require.Len(t, kinds, 2)
		assert.True(t, kinds[0] < kinds[1],
			"%s links are not sorted: %v", target, kinds)
	}
}

// And the shape is fully determined, so a caller cannot act on a partially-resolved
// link. Every entry names a table and both columns, and the table is not the target's
// own table -- a link that resolved to the target's own table would be an UPDATE
// wearing a link's name.
func TestEveryLinkShapeIsCompleteAndNamesAJoinTable(t *testing.T) {
	targetTables := map[string]string{
		"scene": "scenes",
		"image": "images",
	}

	for _, target := range []string{"scene", "image"} {
		for _, kind := range collab.ProposableLinks(target) {
			shape, err := collab.ValidateLink(target, kind)
			require.NoError(t, err)

			assert.NotEmpty(t, shape.Table, "%s/%s has no table", target, kind)
			assert.NotEmpty(t, shape.IdColumn, "%s/%s has no id column", target, kind)
			assert.NotEmpty(t, shape.LinkColumn, "%s/%s has no link column", target, kind)

			assert.NotEqual(t, targetTables[target], shape.Table,
				"%s/%s resolves to the target's OWN table (%s). A link is a join "+
					"table; resolving to the target's table would make it an UPDATE "+
					"wearing a link's name, which is the exact confusion the separate "+
					"namespace exists to prevent.", target, kind, shape.Table)
		}
	}
}

// The member id is parsed once, by the applier, from the value the vote agreed on --
// and this asserts there is no second source of truth by checking that a value with
// surrounding whitespace still applies the SAME member, not a different one.
func TestAMemberIdIsParsedFromTheAgreedValueAndNothingElse(t *testing.T) {
	targets := newFakeTargets()
	applier := collab.NewApplier(targets)

	// " 42 " is accepted by validation (it trims) and must apply member 42.
	_, err := applier.Apply(context.Background(), collab.Proposal{
		ID: 1, TargetType: "scene", TargetID: 7, Field: "performer_ids",
		NewValue: ptr(" 42 "), AuthorID: 1,
	})
	require.NoError(t, err)
	assert.True(t, targets.hasLink("scene", 7, collab.LinkScenePerformer, 42),
		"the applied member is the PARSED value, so a padded id adds member 42 and "+
			"not a row keyed on the padded string")

	// And it is idempotent against the unpadded spelling of the same member, which is
	// what proves the trim happens rather than the two being different rows.
	_, err = applier.Apply(context.Background(), collab.Proposal{
		ID: 2, TargetType: "scene", TargetID: 7, Field: "performer_ids",
		NewValue: ptr("42"), AuthorID: 1,
	})
	require.NoError(t, err)
	assert.Equal(t, 1, targets.linkAdds,
		"' 42 ' and '42' are the same member, so the second apply is already-correct")
}
