package collab

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// A LINK IS NOT A COLUMN. This file is the second half of the change that
// TestVocabulary_EveryFieldIsARealColumn forced, and it exists because the first
// attempt -- adding performer_ids to the vocabulary as a TypeInt -- was caught by that
// test and reverted.
//
// THE VOCABULARY IS A COLUMN NAMESPACE, and that is not a convenience: WriteFieldIfChanged
// takes a field name and interpolates it into an UPDATE. So a join table cannot be
// named there, and a link needs its own type rather than a fake column.
//
// THE CONSEQUENCES, which is the whole design:
//
//   - A link is ADDITIVE and only additive. `performer_ids` names the SET; the proposal's
//     value names one member to add. A removal is deliberately NOT expressible, because
//     one reviewer's "yes, that performer is in this scene" must not be able to unlink
//     the other four with the audit trail showing only an add. Making the vocabulary
//     accept a signed value to mean "remove" would put a deletion behind a field named
//     after an addition.
//   - Adding a member that is already there is a NO-OP and reports already-correct,
//     not an error. The join table's own primary key is the concurrency guard, so two
//     workers adding the same member produce one row between them.
//   - A link is still a PROPOSAL. Nothing here writes a shared field; it changes which
//     operations the apply path may perform, and it does so behind the same vote.

// LinkKind is the type of relationship being added, and the closed set of them.
//
// CLOSED for the same reason the vocabulary is: an open set is a deserialisation
// surface, because the name is interpolated into an INSERT against a table this
// package does not own. A kind that is not listed here cannot be applied, and the only
// way to add one is to add the table to links below.
type LinkKind string

const (
	// LinkScenePerformer adds a performer to a scene (scene_performers).
	LinkScenePerformer LinkKind = "performer_ids"
	// LinkSceneTag adds a tag to a scene (scene_tags).
	LinkSceneTag LinkKind = "tag_ids"
	// LinkImagePerformer adds a performer to an image (image_performers).
	LinkImagePerformer LinkKind = "performer_ids"
	// LinkImageTag adds a tag to an image (image_tags).
	LinkImageTag LinkKind = "tag_ids"
	// LinkGalleryPerformer adds a performer to a gallery (performers_galleries).
	LinkGalleryPerformer LinkKind = "performer_ids"
	// LinkGalleryTag adds a tag to a gallery (galleries_tags).
	LinkGalleryTag LinkKind = "tag_ids"
)

// ErrLinkNotProposable is returned for a link that has no entry in links below.
//
// IT IS ErrFieldNotProposable, NOT A NEW SENTINEL, and that is the design rather than a
// shortcut. There are now two namespaces -- columns and links -- and a caller asking
// "may this field be proposed?" must not be able to tell which one refused. A separate
// sentinel would hand every caller a reliable way to probe the schema: try a name, see
// which error came back, learn whether it is a column or a link.
//
// So the question has ONE answer and ONE error, and the message says "field is not
// proposable" for both -- which is true, and reveals nothing. Callers that genuinely
// need to know which namespace a name belongs to ask IsLinkField, which is a positive
// question with a boolean answer rather than an error to interpret.
var ErrLinkNotProposable = ErrFieldNotProposable

// ErrLinkTargetMissing is returned by AddLink when the TARGET row is gone, and it is
// DISTINCT from every other failure on purpose.
//
// The apply path treats a missing target as a normal outcome -- the scene was deleted
// between the vote and the apply, so the proposal is rejected with "target no longer
// exists" rather than left failing forever. Every OTHER AddLink failure is a fault:
// a bad id, an unknown link, a constraint the caller got wrong.
//
// So the distinction has to be made by the STORE, which is the only layer that knows
// whether the row exists. Inferring it from the error TEXT, or lumping it with
// "some error", would make a constraint failure look like a deleted scene -- and the
// proposal would be recorded as rejected on evidence that does not exist.
var ErrLinkTargetMissing = errors.New("collab: the link target no longer exists")

// LinkShape is the closed (target, kind) -> table mapping.
//
// NAMED EXPLICITLY, and this is the load-bearing table in the file. A derived mapping
// -- say, "the kind's name plus an s" -- would be a string convention standing in for a
// schema, which is how studio.url ended up in a spec as though it were a column. The
// table is the honest place: someone adding a relationship writes the table name here,
// and pkg/sqlite's guard already proves each one exists.
type LinkShape struct {
	// Table is the join table, which is what the write actually targets.
	Table string
	// IdColumn is the target's own id column in that table.
	IdColumn string
	// LinkColumn is the entity id column -- the member being added.
	LinkColumn string
}

// links maps a target type and a link kind to the join table they mean.
//
// The SAME kind string appears under two targets -- "performer_ids" for both scene and
// image -- because the field name is the join table's name, and scene_performers and
// image_performers are different tables. So the pair is the key, never the kind alone.
// That is the same reason Policy.MayAutoApply takes (targetType, kind).
//
// AND THE NAMES ARE THE SCHEMA'S, NOT MINE. I first wrote scene_performers,
// image_performers, scene_tags and image_tags -- all four plausible, all four WRONG. The
// real tables are performers_scenes, scenes_tags, performers_images and images_tags:
// two of them are named after the ENTITY first and two after the TARGET first, with no
// convention to derive from. That is precisely the failure TestVocabulary_EveryFieldIsARealColumn
// exists to catch, and I had it in hand one file earlier.
//
// So the names are read off a migrated database by
// TestEveryLinkTableAndColumnIsReal, in pkg/sqlite, the same way the vocabulary's guard
// reads its columns. A name in this map that does not exist is a test failure rather
// than a runtime SQL error on the first approved proposal.
var links = map[string]map[LinkKind]LinkShape{
	"scene": {
		LinkScenePerformer: {Table: "performers_scenes", IdColumn: "scene_id", LinkColumn: "performer_id"},
		LinkSceneTag:       {Table: "scenes_tags", IdColumn: "scene_id", LinkColumn: "tag_id"},
	},
	"image": {
		LinkImagePerformer: {Table: "performers_images", IdColumn: "image_id", LinkColumn: "performer_id"},
		LinkImageTag:       {Table: "images_tags", IdColumn: "image_id", LinkColumn: "tag_id"},
	},
	// THE GALLERY BLOCK WAS MISSING, and it is the same omission as studio_id was on the
	// vocabulary side: autotag has always matched performers and tags against galleries
	// (internal/autotag/gallery.go), and neither the propose side nor the apply side had
	// a shape for it. So a governed autotag -- the DEFAULT for a new instance, whose
	// curation mode is `propose` -- could not file a performer or tag claim for a
	// gallery at all, while the direct path could.
	//
	// Found the same way, and for the same reason: only the end-to-end test that runs
	// the whole chain against a real database notices that a link kind is in the tagger
	// and absent from the namespace. ValidateLink REFUSES an unmapped pair, so this
	// failed loudly rather than silently -- which is the one saving grace, and it is why
	// the failure read as an autotag error rather than as a missing field.
	//
	// The table and column names are again read off the migration rather than derived,
	// and again there is no convention to derive them from: `performers_galleries` is
	// ENTITY-first and `galleries_tags` is TARGET-first, in the same file, one CREATE
	// apart. TestEveryLinkTableAndColumnIsReal checks them against a migrated database.
	"gallery": {
		LinkGalleryPerformer: {Table: "performers_galleries", IdColumn: "gallery_id", LinkColumn: "performer_id"},
		LinkGalleryTag:       {Table: "galleries_tags", IdColumn: "gallery_id", LinkColumn: "tag_id"},
	},
}

// LookupLink resolves a (target, kind) pair to the join table it means.
//
// ok is false for anything not listed, INCLUDING a kind that is a real vocabulary
// column. studio_id is a column and not a link, so asking for it here returns false --
// and that asymmetry is deliberate, because a caller that confuses the two is about to
// write a join table where an UPDATE belongs, or the reverse.
func LookupLink(targetType string, kind LinkKind) (LinkShape, bool) {
	kinds, ok := links[targetType]
	if !ok {
		return LinkShape{}, false
	}
	shape, ok := kinds[kind]
	return shape, ok
}

// ProposableLinks lists the links a target type may add, sorted by kind.
//
// Sorted, and for the same reason ProposableFields sorts: this is what a UI renders a
// form from, and Go's map iteration order would reshuffle it on every render.
func ProposableLinks(targetType string) []LinkKind {
	kinds, ok := links[targetType]
	if !ok {
		return nil
	}
	out := make([]LinkKind, 0, len(kinds))
	for k := range kinds {
		out = append(out, k)
	}
	// Insertion sort over a handful of strings, matching sortStrings in vocabulary.go
	// rather than importing sort for two elements' worth of work.
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j] < out[j-1]; j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}
	return out
}

// ValidateLink checks that a link may be proposed at all, and returns the shape it
// means.
//
// THE ENTITY ID IS NOT VALIDATED HERE, and the omission is on purpose: this function
// answers "is this relationship proposable", and ValidateValue answers "is this value
// right for its field". Collapsing them would put the target's existence question and
// the value's parse question in one function, and the error a caller gets for a bad id
// would depend on which function it happened to call first.
func ValidateLink(targetType string, kind LinkKind) (LinkShape, error) {
	shape, ok := LookupLink(targetType, kind)
	if !ok {
		// THE BARE SENTINEL, with no field name in the message.
		//
		// A pre-existing test caught this: TestProposal_VocabularyIsCheckedBefore
		// StickyRejection asserts that a non-proposable field is refused with
		// ErrFieldNotProposable, and adding the target and field to my message made the
		// link path's error DISTINGUISHABLE from the column path's. That is a schema
		// oracle: a caller that can tell "scene/nope is not a link" from "scene/nope is
		// not a column" learns which fields exist by watching which error comes back.
		//
		// The same reasoning is already recorded next to the vocabulary's
		// unknown-target check, where an unknown target must be indistinguishable from
		// an unknown field for exactly this reason. Two namespaces now, one rule: a
		// refusal says "not proposable" and nothing else.
		return LinkShape{}, ErrLinkNotProposable
	}
	return shape, nil
}

// ValidateLinkValue checks that a value is a well-formed member id for a link.
//
// IT IS NOT ValidateValue, and cannot be: ValidateValue switches on the COLUMN's type
// and a link has no column. The rules that carry over are the int ones -- a member id
// must parse, and it must be positive -- because a link's value is an entity id and a
// 0 or negative one is a row pointing at nothing.
//
// A NIL VALUE MEANS "CLEARING THE LINK", and is REFUSED, which is the difference from
// a column where nil is a legitimate "unset this" edit. Clearing a relationship is a
// REMOVAL, and this file has no removal operation. Refusing it here means the refusal
// is a validation error at propose time rather than a missing method at apply time,
// and the message can say why: there is no way to unlink through a proposal.
func ValidateLinkValue(targetType string, kind LinkKind, value *string) error {
	if _, err := ValidateLink(targetType, kind); err != nil {
		return err
	}
	if value == nil {
		return fmt.Errorf("%w: clearing a link is a removal, and links are additive "+
			"only -- there is no way to unlink through a proposal", ErrLinkNotProposable)
	}
	n, err := strconv.Atoi(strings.TrimSpace(*value))
	if err != nil {
		return ErrValueInvalid
	}
	if n <= 0 {
		return ErrValueInvalid
	}
	return nil
}

// validateProposalField is the ONE place a proposal's field is checked, and it is what
// Create and Supersede both call.
//
// A proposal names either a column or a link, and the two are different operations --
// an UPDATE and an INSERT -- so the field is resolved in whichever namespace it belongs
// to and validated by the rules of that namespace.
//
// THE COLUMN NAMESPACE IS CHECKED FIRST, and that is the safe order: every column is
// the more conservative answer, so a name that somehow appeared in both maps resolves to
// a column, and an UPDATE of a real column cannot do the damage an INSERT into the
// wrong join table could. It is not an `||` of two checks -- a link's value is checked
// by ValidateLinkValue, because ValidateValue switches on a column type that a link does
// not have.
func validateProposalField(targetType, field string, value *string) error {
	if _, ok := LookupField(targetType, field); ok {
		return ValidateValue(targetType, field, value)
	}
	return ValidateLinkValue(targetType, LinkKind(field), value)
}

// IsLinkField reports whether a proposal's field names a link rather than a column.
//
// Exported because the applier needs it to choose between WriteFieldIfChanged and
// AddLink, and because the UI needs it to render a link control rather than a text box.
// It resolves the COLUMN namespace first, for the same conservative reason as
// validateProposalField.
func IsLinkField(targetType, field string) bool {
	if _, ok := LookupField(targetType, field); ok {
		return false
	}
	_, ok := LookupLink(targetType, LinkKind(field))
	return ok
}
