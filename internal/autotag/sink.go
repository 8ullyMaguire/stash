package autotag

import (
	"context"
	"fmt"

	"github.com/stashapp/stash/internal/autoproposal"
	"strconv"

	"github.com/stashapp/stash/internal/collab"
)

// THE SINK IS WHERE THE WIRING HAPPENS, and it is one interface rather than eight
// changed signatures.
//
// internal/autotag has eight exported entry points (ScenePerformers, SceneStudios,
// SceneTags, ImagePerformers, ImageTags, GalleryPerformers, and the Tagger methods)
// that all end in the same shape: match a name against a path, then add the match
// through one of three operations -- scene.AddPerformer, scene.AddTag, or
// UpdatePartial for a studio. The plan's gap was that those three operations write
// directly.
//
// THE CHOICE WAS TO CHANGE THE EIGHT SIGNATURES OR TO CHANGE WHAT THEY CALL. Changing
// the signatures means every caller and every test in internal/manager and
// internal/autotag moves, for a difference that is one indirection deep. So the tagger
// keeps its shape and the WRITE becomes an interface: a Sink either applies the match
// (the old behaviour) or files it as a proposal (§6b.2, #5).
//
// WHICH IS WHY A SINK IS NOT OPTIONAL. autoproposal.Curator is a struct with a
// required Attribution, and NewCurator refuses an unattributed one -- so "no curator
// configured" is a runtime refusal, not a nil check. DirectSink is the direct-writer
// and exists for exactly one reason: it is what the tests exercise, and it is what an
// instance with curation DISABLED would use. §6b.2's claim is that an automatic write
// is a machine laundering a claim past governance, so the direct sink is the thing
// governance has to be able to turn off, and a default that quietly selects it would
// put the unsafe path on the happy path.

// Sink is where an autotag match goes.
//
// ONE METHOD, and it is a link-add rather than a field write, because that is what
// every one of the eighteen sites actually is: `AddPerformer`, `AddTag`, and the
// studio assignment, which is a column but is filed the same way. The kind is passed
// explicitly rather than inferred, because "which relationship" is the caller's
// knowledge and inferring it from a name is how a scene's performer ends up in the
// image table.
type Sink interface {
	// AddMatch files or applies a LINK: a performer or a tag on a scene, image or
	// gallery. `already` is true when the relationship was already present, so the
	// caller can skip a log line for a change that did not happen.
	AddMatch(ctx context.Context, targetType string, targetID int, kind collab.LinkKind, entityID int, entityName string) (already bool, err error)

	// SetStudio sets a target's studio, which is a COLUMN and not a link.
	//
	// IT IS A SEPARATE METHOD AND NOT A LinkKind, and that separation is the design
	// rather than a convenience. A studio lives in scene.studio_id; a performer lives
	// in the performers_scenes join table. They are different kinds of fact about a
	// target -- one is a field, one is a relationship -- and the vote counts differently
	// for each: a studio is a single-writer field where the last machine's guess is
	// simply overwritten, while a link is additive and nobody loses anything when two
	// machines both find the same performer.
	//
	// The first version of this code passed a studio through AddMatch as the kind
	// "studio_id", and the tests caught it: 18 of them failed on an unmet
	// UpdatePartial, because a recording sink that faithfully implements AddMatch has no
	// way to perform a column write. That failure is the design note. A single method
	// carrying both meanings makes the field case unrepresentable, and it pushes the
	// decision about which store to write into a string comparison far from the write.
	//
	// A SINGLE WRITER IS NOT SINGLE-VALUED-FOR-ALL-TIME, and `already` here means only
	// "the field was empty", matching the caller's own precondition: a scene that
	// already names a studio is skipped by the caller before it ever gets here, so
	// this method is never asked to overwrite a deliberate choice.
	SetStudio(ctx context.Context, targetType string, targetID int, studioID int) (already bool, err error)
}

// DirectSink writes the match, which is what autotag did before this file existed.
//
// IT EXISTS AND IT IS NOT THE DEFAULT, and that ordering is the point. §6b.2 says an
// automatic direct write is a machine laundering a claim past governance, so the
// direct path must be reachable and must be chosen deliberately. If the zero value of
// a Tagger's sink were DirectSink, then a caller who forgot to configure curation
// would launder claims by omission -- and a test asserting "a Tagger with no sink
// files proposals" would be asserting the safe outcome of the unsafe default.
//
// IT DELEGATES TO A collab.TargetStore, and that is the design rather than a
// convenience. AddLink is the code that INSERTs into a join table and the applier runs
// it after a vote; running it here too means the difference between applying and
// proposing is GOVERNANCE and nothing else. Both paths write the same row through the
// same statement, so they cannot disagree about what a link IS -- and a direct write
// cannot drift into doing something the approved path would not, which is exactly what
// a second implementation invites.
type DirectSink struct {
	// Targets is the write surface, and it is the SAME store the applier uses. So this
	// struct is a governance decision rather than a second mechanism.
	Targets collab.TargetStore
}

func (d DirectSink) AddMatch(ctx context.Context, targetType string, targetID int, kind collab.LinkKind, entityID int, _ string) (bool, error) {
	if d.Targets == nil {
		// REFUSED, not a no-op. A no-op here reads as "nothing matched" to every
		// caller above, so a misconfigured sink would look like a scan that found
		// nothing -- which is the one conclusion an operator must never draw from a
		// configuration error.
		return false, fmt.Errorf("autotag: a DirectSink with no TargetStore would " +
			"silently drop matches. Refusing rather than returning a no-op, because " +
			"a no-op reads as 'nothing matched' to every caller above")
	}

	added, err := d.Targets.AddLink(ctx, targetType, targetID, kind, entityID)
	if err != nil {
		return false, err
	}

	// ALREADY THERE, which AddLink reports as added=false. `already` means "nothing
	// changed", so the caller skips its log line -- the same signal the pre-sink code
	// produced by reading the existing ids first. A studio is a COLUMN rather than a
	// join-table row, and it arrives here as kind "studio_id", which AddLink refuses
	// because a column is not a link. So a studio on the direct path is a caller
	// wiring mistake rather than something to paper over.
	return !added, nil
}

// ProposalSink files each match as a proposal instead of applying it.
//
// §6b.2 and non-negotiable #5: an automatic tag/performer/studio suggestion is a
// collab.Proposer proposal, so a machine's claim lands in the same audit trail a
// human's does. The claim is the load-bearing part -- governance that applies a
// machine's match directly is governance that has already decided the machine is
// right, which is the thing it exists to check.
type ProposalSink struct {
	Curator *autoproposal.Curator
}

func (p ProposalSink) AddMatch(ctx context.Context, targetType string, targetID int, kind collab.LinkKind, entityID int, entityName string) (bool, error) {
	if p.Curator == nil {
		return false, fmt.Errorf("autotag: a ProposalSink with no Curator would file nothing. " +
			"Refusing rather than returning a no-op, because a caller above reads a " +
			"no-op as 'nothing matched' and the operator concludes autotag is broken " +
			"rather than that governance is misconfigured")
	}

	// The outcome's Reason is deliberately DISCARDED rather than logged here. It is
	// the proposal id and "filed as proposal N", and the caller already logs a line per
	// match with the entity's name -- so logging it here would produce two lines per
	// match, one of which says "proposal 4" with no indication of what it was about.
	// A sink that logs is a sink whose output nobody can read.
	_, err := p.Curator.Curate(ctx, autoproposal.Suggestion{
		TargetType: targetType,
		TargetID:   targetID,
		Kind:       string(kind),
		EntityID:   entityID,
		EntityName: entityName,
	})
	if err != nil {
		// A FILING FAILURE IS AN ERROR, and it propagates. It does not fall back to
		// a direct write: that is precisely how an automatic write sneaks past a
		// broken audit trail, and it would look like success because the field would
		// hold the right value. The Curator already refuses to fall back; this is the
		// same rule at this layer, for the failures the Curator cannot see.
		return false, fmt.Errorf("filing %s on %s %d as a proposal: %w", kind, targetType, targetID, err)
	}

	// A filed proposal is NOT `already`. Nothing was applied, so there is no honest
	// way to report "nothing changed" -- and reporting already=true would make the
	// caller skip its log line, so an operator would see a match silently vanish.
	return false, nil
}

// SetStudio writes a target's studio column, which is a field and not a link.
//
// IT GOES THROUGH THE SAME TargetStore AS AddMatch, which is the point of the split.
// The direct path and the proposed path differ in GOVERNANCE -- whether a vote
// happened -- and must not differ in MECHANICS, or the two would disagree about what
// a studio assignment IS. WriteFieldIfChanged is deliberately the store's
// change-conditional write, so calling it here means a re-run of autotag over a scene
// that already names this studio writes nothing and reports already, which is what
// keeps a scheduled scan from filling the log with no-op lines.
//
// The caller's precondition (skip when the field is already set) is NOT reimplemented
// here. It cannot be: this method does not know whether the existing value was a
// deliberate choice or another machine's guess, and overwriting a deliberate choice
// because a scan re-ran is a governance failure, not a duplicate. So the guard belongs
// to the caller, which is where the "don't set if already set" comment in studio.go
// lives, and this method is the write.
func (d DirectSink) SetStudio(ctx context.Context, targetType string, targetID int, studioID int) (bool, error) {
	if d.Targets == nil {
		return false, fmt.Errorf("autotag: a DirectSink with no TargetStore would " +
			"silently drop studio matches. Refusing rather than returning a no-op, " +
			"because a no-op reads as 'nothing matched' to every caller above")
	}

	// expected IS NIL, and that nil is the precondition rather than a shrug.
	//
	// WriteFieldIfChanged writes only if the field currently holds `expected`, and a
	// NULL expected means "only if it is currently NULL" -- which is precisely the
	// rule §6b.2 needs for a single-writer field: fill it if empty, never overwrite.
	//
	// So the single-writer guarantee is enforced AT THE WRITE, atomically, rather than
	// by a caller's read-then-write. That matters because the caller in studio.go does
	// check `o.StudioID != nil` first, and between that check and this write another
	// worker can commit a different studio. A read-then-write would let whichever scan
	// finished last win; passing nil as expected makes the store refuse, so the field
	// keeps the value the first writer chose and this scan reports already. A lost
	// update on a single-writer field is the bug, and this is what prevents it.
	value := strconv.Itoa(studioID)
	wrote, err := d.Targets.WriteFieldIfChanged(ctx, targetType, targetID,
		autoproposal.KindSceneStudio, nil, &value)
	if err != nil {
		return false, err
	}

	// wrote is false in two cases that the caller treats the same: the field already
	// held a studio, or it already held THIS one. Both mean "nothing changed", and
	// distinguishing them here would need a read this method deliberately does not do.
	return !wrote, nil
}

// SetStudio FILES A PROPOSAL for a studio, and reports it as a change.
//
// A FILED PROPOSAL IS NOT `already`, and the reason is the same as AddMatch's: nothing
// was applied, so reporting already would tell the caller "this field was already
// right" and suppress a log line for a claim that is genuinely pending. The pending
// state is the one the operator needs to see -- it is the whole output of the governed
// path.
//
// IT IS FILED AS A SINGLE-WRITER CLAIM, not as a link, and the Kind is the existing
// autoproposal.KindSceneStudio. That distinction is what §4.2's weighted vote needs: a
// studio is a field where the last accepted writer wins, so a second claim to the same
// field has to be visible as a COMPETING claim rather than a second member of a set.
func (p ProposalSink) SetStudio(ctx context.Context, targetType string, targetID int, studioID int) (bool, error) {
	if p.Curator == nil {
		return false, fmt.Errorf("autotag: a ProposalSink with no Curator would file " +
			"nothing. Refusing rather than returning a no-op, because a caller above " +
			"reads a no-op as 'nothing matched' and the operator concludes autotag is " +
			"broken rather than that governance is misconfigured")
	}

	// The name is read rather than passed because SetStudio's signature is the column
	// write's shape, and a studio's name is available from the store the write goes to.
	// So the proposal is filed WITHOUT a name rather than with a fabricated one: a
	// proposal card reading "studio_id" is honest, and a card reading "" is worse.
	_, err := p.Curator.Curate(ctx, autoproposal.Suggestion{
		TargetType: targetType,
		TargetID:   targetID,
		Kind:       autoproposal.KindSceneStudio,
		EntityID:   studioID,
		EntityName: "",
	})
	if err != nil {
		// NO FALLBACK TO A DIRECT WRITE. A studio field is the one field an operator is
		// most likely to notice being wrong, and a scan that fills it when the proposal
		// store is down would produce a plausible-looking library with no audit trail.
		return false, fmt.Errorf("filing a studio claim on %s %d as a proposal: %w",
			targetType, targetID, err)
	}

	return false, nil
}
