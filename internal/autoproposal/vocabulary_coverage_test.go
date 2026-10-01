package autoproposal

import (
	"testing"

	"github.com/stashapp/stash/internal/collab"
)

// R086, step 8.2's SECOND RECORDED GAP: "internal/collab/vocabulary.go declares no
// relationship fields, so a link add cannot yet pass ValidateValue -- a proposal whose
// field the vocabulary does not know can never be applied."
//
// The gap is real and it is NOT a missing map entry. Adding the entry is the wrong fix,
// and I proved that by doing it.
//
// WHAT MAKES IT HARD: a vocabulary field is a COLUMN NAME. TargetStore.WriteFieldIfChanged
// takes (targetType, targetID, field, current, value) and the apply path interpolates
// `field` straight into an UPDATE, so pkg/sqlite's TestVocabulary_EveryFieldIsARealColumn
// reads the real columns off a migrated database and fails any field that is not one of
// them. I added performer_ids and tag_ids to the scene and image vocabularies, the
// coverage test went green, and the integration suite went red with the right reason:
// those are not columns of scenes or images, they are JOIN TABLES. The change would have
// validated, filed, approved, and then failed as a SQL error on the first proposal a
// user touched -- the identical defect spec §4.1's studio.url was, and the identical
// thing that test was written to catch. So it is reverted.
//
// TWO THINGS BLOCK THE FIX, and neither is a map entry:
//
//  1. LIST SEMANTICS. `performer_ids` names a SET (the join table's contents) while one
//     proposal carries ONE entity id. Additive reading, that is honest -- and it means
//     the field cannot express a removal, which is the conservative direction. But the
//     apply path has no operation for "insert into a join table"; it has UPDATE. There
//     is no SQL that UPDATEs a set.
//
//  2. A TARGET STORE THAT KNOWS ABOUT LINKS. TargetStore is four methods and every one
//     of them is a column read or write. Adding AddLink(ctx, targetType, targetID,
//     linkKind, entityID) is a real change to a security boundary -- the surface where
//     a vote becomes a write on shared content -- and it needs its own review, not a
//     patch smuggled in beside a vocabulary map.
//
// So this test is INVERTED on purpose, like TestAutotagStillWritesDirectlyAndThatIsThe
// KnownGap, and it asserts the CURRENT state: the link kinds this package can file are
// still unproposable. It fails the day either blocker is resolved, and the failure names
// what to do then. A test that documented a known gap and passed when the gap CLOSED
// would be worse than no test, because it would then be lying about the product.
func TestLinkKindsCannotBeProposedYetAndThatIsTheKnownGap(t *testing.T) {
	// The kinds that name a JOIN TABLE rather than a column. studio_id is deliberately
	// NOT here: it is a real column of scenes and validates today, which is the control
	// that proves this is about link semantics and not about the test being unable to
	// reach the vocabulary at all.
	linkKinds := []struct {
		target string
		kind   string
		name   string
	}{
		{"scene", KindScenePerformer, "KindScenePerformer"},
		{"scene", KindSceneTag, "KindSceneTag"},
		{"image", KindImagePerformer, "KindImagePerformer"},
		{"image", KindImageTag, "KindImageTag"},
	}

	for _, c := range linkKinds {
		entity := "42"
		err := collab.ValidateValue(c.target, c.kind, &entity)
		if err == nil {
			t.Errorf("%s/%s (%s) is now PROPOSABLE, which means the gap closed. "+
				"GOOD -- now remove it from this list. And check the two things that "+
				"had to happen for it, because either one missing is a worse state "+
				"than the gap:\n"+
				"  1. the apply path has an operation for a JOIN TABLE, not an UPDATE\n"+
				"  2. pkg/sqlite's TestVocabulary_EveryFieldIsARealColumn was taught "+
				"that this field is not a column and that is intentional\n"+
				"Both, or the proposal validates and dies in SQL.",
				c.target, c.kind, c.name)
		}
	}

	// The control, asserted so this cannot pass by the vocabulary being unreachable.
	value := "42"
	if err := collab.ValidateValue("scene", "studio_id", &value); err != nil {
		t.Errorf("scene/studio_id must still be proposable, and it is not (%v). If the "+
			"vocabulary has become unreachable, every assertion above passes "+
			"vacuously -- which is the failure mode this test exists to avoid, the same "+
			"one that made an earlier write-path guard report clean while matching "+
			"nothing.", err)
	}
}
