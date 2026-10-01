package autoproposal

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stashapp/stash/internal/collab"
)

// R086, step 8.2's SECOND RECORDED GAP: "internal/collab/vocabulary.go declares no
// relationship fields, so a link add cannot yet pass ValidateValue -- a proposal whose
// field the vocabulary does not know can never be applied."
//

// EVERY LINK KIND THE AUTOTAG PATH NEEDS NOW FILES, and this is what closed the gap.
//
// HISTORY, because the shape of this test is the record of why the change was built the
// way it was. Plan step 8.2 recorded a gap: "internal/collab/vocabulary.go declares no
// relationship fields, so a link add cannot yet pass ValidateValue". I read that as a
// missing map entry and added performer_ids and tag_ids to the vocabulary as TypeInt
// fields. The unit suite went green.
//
// pkg/sqlite's TestVocabulary_EveryFieldIsARealColumn caught it, correctly: a vocabulary
// field is a COLUMN NAME that WriteFieldIfChanged interpolates into an UPDATE, and those
// two are JOIN TABLES. So the change would have validated, been filed, been approved,
// and then died as a SQL error on the first proposal a user touched -- the identical
// defect spec §4.1's studio.url was, and the identical thing that guard was written to
// catch. Reverted.
//
// The real fix is a second namespace (internal/collab/link.go) and a second operation
// (TargetStore.AddLink), because a relationship is an INSERT and a field is an UPDATE
// and there is no SQL that UPDATEs a set. The table names in that map are read off a
// migrated database by TestEveryLinkTableAndColumnIsReal -- after I first wrote
// scene_performers, image_performers, scene_tags and image_tags, all four plausible and
// all four wrong: the schema says performers_scenes, scenes_tags, performers_images,
// images_tags, with no convention to derive them from.
//
// So THIS test asserts the closure through the real proposer, which is the path a
// machine's claim actually takes. It is the inversion of the tripwire that stood here
// while the gap was open, and it fails the day a kind regresses.
func TestEveryKindAutotagCanFileIsNowAcceptedByTheSystem(t *testing.T) {
	// A proposer that runs the REAL validation, so this is the question "would the
	// system accept this proposal" and not "does a stub say yes". The stub version of
	// this test passed while four of the five kinds were refused by the vocabulary,
	// which is the whole reason this one does not use a stub.
	p := &validatingProposer{}

	// The kinds autoproposal declares, and the pairs they are used in. `name` is the
	// CONSTANT rather than the string, because a kind string is shared across targets
	// and keying the message on it would point a fix at the wrong line.
	cases := []struct {
		target string
		kind   string
		name   string
	}{
		{"scene", KindScenePerformer, "KindScenePerformer"},
		{"scene", KindSceneStudio, "KindSceneStudio"},
		{"scene", KindSceneTag, "KindSceneTag"},
		{"image", KindImagePerformer, "KindImagePerformer"},
		{"image", KindImageTag, "KindImageTag"},
	}

	for _, c := range cases {
		err := collab.ValidateLinkValue(c.target, collab.LinkKind(c.kind), ptr("42"))
		if err != nil {
			// A COLUMN kind is not a link, and that is not a failure -- studio_id is
			// a real column of scenes. So the check is "accepted by one namespace or
			// the other", which is exactly the question the applier asks.
			if vErr := collab.ValidateValue(c.target, c.kind, ptr("42")); vErr != nil {
				t.Errorf("%s/%s (%s) is accepted by NEITHER namespace: link says %v, "+
					"column says %v. A kind the machine can file but nothing can apply "+
					"is a claim that sits in the audit trail forever while looking "+
					"like governance working.",
					c.target, c.kind, c.name, err, vErr)
			}
		}

		// And end to end, through the curator, because a kind that validates is not
		// the same as a kind that can be FILED.
		curator, err := NewCurator(p, Attribution{
			Author: Author{UserID: 1, Name: "owner"}, Source: "autotag",
		}, Policy{})
		require.NoError(t, err)

		out, err := curator.Curate(context.Background(), Suggestion{
			TargetType: c.target, TargetID: 7, Kind: c.kind,
			EntityID: 42, EntityName: "x",
		})
		if err != nil {
			t.Errorf("%s/%s (%s) could not be curated: %v", c.target, c.kind, c.name, err)
			continue
		}
		if !out.Applied {
			assert.NotZero(t, out.ProposalID,
				"%s/%s was neither applied nor filed: %s", c.target, c.kind, c.name, out.Reason)
		}
	}
}

// validatingProposer runs the system's real validation, so a test using it asks what
// the system would do rather than what a stub says.
//
// The stub version of this file recorded every kind as filed while four of the five
// were refused by the vocabulary -- a test that passed for the wrong reason, which is
// the failure mode this one exists to rule out.
type validatingProposer struct{}

func (validatingProposer) Create(_ context.Context, req collab.Proposal) (*collab.Proposal, error) {
	if err := collab.ValidateLinkValue(req.TargetType, collab.LinkKind(req.Field), req.NewValue); err == nil {
		return &collab.Proposal{ID: 1}, nil
	}
	if err := collab.ValidateValue(req.TargetType, req.Field, req.NewValue); err != nil {
		return nil, err
	}
	return &collab.Proposal{ID: 1}, nil
}

func ptr(s string) *string { return &s }
