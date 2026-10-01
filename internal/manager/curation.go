package manager

import (
	"context"
	"fmt"

	"github.com/stashapp/stash/internal/autoproposal"
	"github.com/stashapp/stash/internal/collab"
	"github.com/stashapp/stash/pkg/models"
)

// CollabTargetStore returns the write surface an approved proposal is applied through.
//
// It is a method rather than a field access because the direct autotag sink needs it
// and the applier needs it, and both must be the SAME store: a direct write that
// differs from the approved path's write is a second implementation of "what a link
// is", and the two would drift exactly where it matters.
func (s *Manager) CollabTargetStore() collab.TargetStore {
	return s.CollabApply
}

// CurationAuthor returns the user automatic suggestions are attributed to.
//
// IT IS A DELIBERATE CHOICE AND NOT A DEFAULT, which is the plan's requirement and the
// reason this is a method that can fail. §4.2's audit trail is the only way an operator
// later learns where a value came from, and "the scheduler did it" is not
// investigable -- so an instance must say which user's name is on a machine's claim.
//
// The user comes from the instance's configured owner. That is the natural choice: the
// owner is the person who ran the scan, so attributing to them is honest, and an
// operator who wants a different user configures a different one rather than having
// the scheduler guess.
func (s *Manager) CurationAuthor(ctx context.Context) (autoproposal.Author, error) {
	users := s.UserStore
	if users == nil {
		// REFUSED rather than user 0. autoproposal.NewCurator refuses an unattributed
		// curator, and this is the same refusal one layer earlier: an unbuildable state
		// rather than a runtime check.
		return autoproposal.Author{}, fmt.Errorf("no user store, so there is no user to " +
			"attribute automatic suggestions to. A machine's claim must name who it is " +
			"made on behalf of, or the audit trail records an author nobody can name")
	}

	// THE OWNER, NOT "THE FIRST USER" AND NOT "THE ONLY USER".
	//
	// §6b.2 wants the attribution to be a deliberate act, and IsOwner is the field that
	// says who is running this instance. Two rules that would both be wrong:
	//
	//   - "the first user" attributes claims to whoever happens to sort first, which is
	//     a person who may not have run the scan and did not choose to be named on it.
	//   - "the only user, else refuse" is right for a single-user instance and WRONG the
	//     moment a second user exists -- it would stop attributing automatic claims at
	//     all, which is the unattributable state the requirement exists to forbid, and
	//     it would do so on a multi-user instance that has an obvious owner.
	//
	// So the owner is selected, and the multi-user case is handled by the flag rather
	// than by a count.
	list, err := users.FindAll(ctx)
	if err != nil {
		return autoproposal.Author{}, fmt.Errorf("listing users to attribute automatic suggestions: %w", err)
	}
	if len(list) == 0 {
		return autoproposal.Author{}, fmt.Errorf("this instance has no user, so " +
			"automatic suggestions have nobody to be attributed to. They are not filed " +
			"unattributed: a claim with no author cannot be investigated later, so the " +
			"scan does not run")
	}

	var owners []models.User
	for _, u := range list {
		if u.IsOwner {
			owners = append(owners, *u)
		}
	}
	if len(owners) == 0 {
		return autoproposal.Author{}, fmt.Errorf("this instance has %d users and no "+
			"owner, so there is nobody to attribute automatic suggestions to. Every "+
			"claim would be unattributed, and an unattributable claim cannot be "+
			"investigated later", len(list))
	}
	if len(owners) > 1 {
		// More than one owner, which the model permits. Refusing rather than picking
		// one: attributing a machine's claim to an arbitrary owner names a person who
		// did not choose to be on it.
		return autoproposal.Author{}, fmt.Errorf("this instance has %d owners, so "+
			"there is no single user to attribute automatic suggestions to. Choosing "+
			"one arbitrarily would put a name on a machine's claim that nobody chose",
			len(owners))
	}

	owner := owners[0]
	// A DISABLED OWNER IS STILL THE OWNER, and the name is recorded rather than
	// refused. The claim is genuinely the owner's to answer for -- they configured the
	// instance and the scan -- and a disabled account does not unmake that. Refusing
	// here would mean a disabled owner silently ungoverns every future claim, which is
	// the opposite of what disabling an account should do.
	return autoproposal.Author{UserID: owner.ID, Name: owner.Username}, nil
}
