package collab_test

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/stashapp/stash/internal/collab"
)

// AN OVERFLOWING ID IS REFUSED, and this test exists because a mutation run said it
// was missing.
//
// The mutant that DELETED the Atoi error check in ValidateValue's TypeInt branch
// SURVIVED the whole suite. My first reading was that the check was redundant with the
// `n <= 0` test below it, since "abc" parses to 0 and is caught by positivity anyway.
// That reading was wrong, and a probe of strconv.Atoi showed why:
//
//	"abc"                  -> n=0,         err != nil
//	"9223372036854775808" -> n=MaxInt64,  err != nil
//
// Atoi SATURATES on overflow and REPORTS the error separately. Drop the error check and
// an id larger than int64 becomes MaxInt64: positive, plausible, and pointing at a row
// that does not exist. So the error check is the only thing standing between an
// oversized value and the apply path, and until this test the suite could not tell.
//
// The distinction worth keeping: positivity refuses a value that is not a valid id, and
// the PARSE check refuses a valid id written in a form the parse rejects. Different
// questions, and only one of them looks redundant.
//
// It lives in the collab package rather than autoproposal because it is a property of
// ValidateValue itself, not of any particular kind a caller files.
func TestAnOverflowingIdIsRefusedRatherThanSaturated(t *testing.T) {
	// One past MaxInt64, and the shapes a caller is most likely to produce by accident:
	// a concatenation, and a value that lost precision in JSON.
	overflowing := []string{
		"9223372036854775808",  // MaxInt64 + 1
		"92233720368547758070", // MaxInt64 * 10
		"99999999999999999999", // 20 digits
		"18446744073709551616", // MaxUint64
	}

	// EVERY TypeInt field, derived from the vocabulary itself via ProposableFields (the
	// same accessor the UI renders a form from, so a new int-typed field is covered
	// without editing this list). The hole is in the int branch, so it applies to all of
	// them equally.
	//
	// The target list is the one pkg/sqlite's TestVocabulary_EveryFieldIsARealColumn
	// uses, restated rather than derived, because that test deliberately hard-codes it
	// so a rename of the vocabulary's type names is itself a failure.
	targets := []string{"scene", "performer", "studio", "tag", "gallery", "image", "group"}

	covered := 0
	for _, target := range targets {
		for _, f := range collab.ProposableFields(target) {
			if f.Type != collab.TypeInt {
				continue
			}
			covered++
			for _, v := range overflowing {
				value := v
				err := collab.ValidateValue(target, f.Field, &value)
				if err == nil {
					t.Errorf("%s/%s accepts %q. strconv.Atoi SATURATES on overflow and "+
						"reports the error separately, so without the error check this "+
						"arrives as MaxInt64: positive, plausible, and pointing at a row "+
						"that does not exist.", target, f.Field, v)
				}
			}
		}
	}

	// So the loop cannot pass by finding no int fields at all -- the vacuous-pass
	// failure this repo has hit before, where a scanner reported clean while matching
	// nothing.
	assert.NotZero(t, covered,
		"no TypeInt field was found in the vocabulary, so this test asserted nothing. "+
			"The overflow hole is in the int branch, so an empty set of int fields means "+
			"the shape of the vocabulary changed and this needs rewriting")
}

// AND THE BOUNDARY IS STILL VALID, so the answer cannot become "reject anything big".
// MaxInt64 parses and is positive, so it is a legal id even though no row carries it --
// existence is the apply path's question, not the vocabulary's.
func TestTheLargestRepresentableIdIsStillAccepted(t *testing.T) {
	value := "9223372036854775807" // MaxInt64
	assert.NoError(t, collab.ValidateValue("scene", "studio_id", &value),
		"MaxInt64 is a parseable positive int, so it is a legal id; a validator that "+
			"refused it would be refusing a legal value to work around a different "+
			"problem")
}
