package autoproposal

import (
	"errors"
	"fmt"
)

// CurationMode is how automatic matches reach shared content. Three states, for the
// same reason R083's auto_acquire has three: a single bool cannot express the middle
// one, and a PAIR of bools admits `file=false, apply=true`, which is an instance
// applying a machine's claims without a vote.
//
//	off    -- autotag does not run. The scan is a user action with no governance
//	          behind it, so "off" means the whole feature is absent rather than
//	          present-and-ignored.
//	propose-- matches are FILED as proposals. The default, and the state the plan's
//	          §6b.2 describes: a machine's claim lands in the same audit trail a
//	          human's does.
//	apply  -- matches are applied directly. §6b.2's "machine laundering a claim past
//	          governance", chosen deliberately by an operator who wants it.
//
// THE ZERO VALUE IS `propose`, and a string constant cannot be a zero value -- "" is
// not "propose". So "" is DEFINED AS propose, which is the state a new install reads
// and the state a config struct that forgot the field gets. That is the safe default
// in the direction that matters: a caller who forgets to configure curation files
// proposals rather than writing fields.
//
// This mirrors R083's resolution of the same apparent conflict ("the plan says the
// zero value must be off" for auto_acquire, where off is the safe state; here the safe
// state is propose, because a missing configuration should not launder claims).
type CurationMode string

const (
	// CurationOff is the feature being switched off entirely.
	CurationOff CurationMode = "off"

	// CurationPropose files automatic matches as proposals. The DEFAULT.
	CurationPropose CurationMode = "propose"

	// CurationApply applies automatic matches directly, unasked.
	CurationApply CurationMode = "apply"

	// curationUnset is what a string field holds before anything writes it, and it
	// MEANS propose. It is a named constant rather than a literal so the three states
	// and the unset case are all visible in one place.
	curationUnset CurationMode = ""
)

// CurationMayPropose reports whether matches are filed as proposals.
func (m CurationMode) CurationMayPropose() bool {
	switch m {
	case CurationOff:
		return false
	default:
		// CurationPropose, CurationApply, and curationUnset all propose. The unset case
		// proposes for the reason the type's doc gives: a caller who forgot to
		// configure curation must not end up writing fields.
		return true
	}
}

// CurationMayApplyDirectly reports whether matches may be applied without a vote.
func (m CurationMode) CurationMayApplyDirectly() bool {
	// EXACTLY the apply state, and not "anything that is not off". The middle state
	// is `propose`, and a rule written as a negation would give it the direct-write
	// permission -- which is the entire bug a three-state type exists to prevent.
	return m == CurationApply
}

// Valid reports whether the mode is one this build knows.
func (m CurationMode) Valid() bool {
	switch m {
	case CurationOff, CurationPropose, CurationApply, curationUnset:
		return true
	default:
		return false
	}
}

// ParseCurationMode reads a mode, defaulting the unset value to propose.
//
// AN UNRECOGNISED VALUE IS REFUSED rather than defaulted, and the asymmetry with the
// unset case is the point: "" means "nobody has chosen yet", while "propoes" means
// somebody chose something this build cannot read. Defaulting the first is a default;
// defaulting the second silently turns a typo into a governance decision.
func ParseCurationMode(s string) (CurationMode, error) {
	switch CurationMode(s) {
	case curationUnset:
		return CurationPropose, nil
	case CurationOff:
		return CurationOff, nil
	case CurationPropose:
		return CurationPropose, nil
	case CurationApply:
		return CurationApply, nil
	default:
		return "", fmt.Errorf("%w: %q is not a curation mode. The modes are %q, %q "+
			"and %q", ErrCurationModeInvalid, s, CurationOff, CurationPropose, CurationApply)
	}
}

// ErrCurationModeInvalid is returned for a mode this build does not know.
var ErrCurationModeInvalid = errors.New("autoproposal: not a curation mode")
