// Package acquisition decides what this instance FETCHES, automatically, without
// asking.
//
// M8 step 8.1 (R083, R075, R076), spec §6b.3. This is capability 2's entry point
// and it shares the transport with capability 3 (preservation) WITHOUT requiring
// it: probe 1 measured that the current transport does not meet §6b.4, but a
// fetch-only acquisition queue needs no address-withholding transport to be
// *correct*. So this step is unblocked and preservation is not.
//
// # THE SWITCH IS THREE STATES, AND A BOOLEAN CANNOT EXPRESS THE MIDDLE ONE
//
//	off          fetches no, seeds no, uploads no data
//	fetch_only   fetches yes, seeds no, uploads no data
//	full         fetches yes, seeds yes, per the existing upload control
//
// `fetch_only` is the state a privacy-conscious user actually wants, and a boolean
// forces them to choose wrongly in one direction or the other. §6b.3 states the
// default is `fetch_only` rather than `off`, and the reason is worth restating
// because it is counter-intuitive: an instance that fetches but never seeds leaks
// no data outward and cannot be made into a source for somebody else, so opting
// users in to the *harmless* half of the capability is not a leak. Non-negotiable
// #7 is about the publish path, and `fetch_only` has no publish path.
//
// # WHY THE ZERO VALUE IS `off`
//
// AcquireMode's zero value is AcquireOff, not AcquireFetchOnly and not a boolean.
// This is the whole safety property and it is a type-level decision:
//
//   - a new database column that has not been written yet reads as `off`, so an
//     upgrade cannot begin acquiring before the operator has decided;
//   - a struct that forgot to set the field reads as `off`;
//   - a JSON decode of a missing key reads as `off`;
//   - a fourth state added later cannot be smuggled in as a bool, because there is
//     no bool to smuggle it into.
//
// A permissive zero value here would be a silent publish path, which is the one
// failure mode §6b.3 and #7 both exist to prevent.
//
// # ENFORCEMENT IS AT THE POINT THE PERMISSION IS READ
//
// Not by omitting rows from a query. §6b.3 is explicit: "Enforcement is at the
// point the permission is read, in the same place the existing tier policy is
// read, and not by omitting rows from a query", because #7's own note is that a
// refactor dropping the filter must not silently resume publishing.
//
// So this package exposes PERMISSIONS (`MayFetch`, `MaySeed`), and every caller
// asks. There is deliberately no `QueryAcquirable()` that returns a filtered list,
// because that shape is the one a refactor can bypass by forgetting the WHERE
// clause — and a caller who has a list cannot tell a filtered list from a
// complete one.

package acquisition

import (
	"errors"
	"fmt"
)

// AcquireMode is the three-state acquisition switch (§6b.3).
//
// NAMED, NOT BOOLEAN, and the name says what it is: this is an *acquire* mode, so
// `off` reads as "do not acquire" rather than as a vague disablement of something
// unspecified.
type AcquireMode string

const (
	// AcquireOff fetches nothing and seeds nothing.
	//
	// AND IT IS THE ZERO VALUE, which is stated above and is the point of the
	// whole type.
	AcquireOff AcquireMode = "off"

	// AcquireFetchOnly fetches and never seeds.
	//
	// The default for a new install (§6b.3), and the state a privacy-conscious
	// user wants: no bytes leave this instance, so this half of the capability
	// carries no publish path for #7 to be violated through.
	AcquireFetchOnly AcquireMode = "fetch_only"

	// AcquireFull fetches and seeds, subject to the existing per-torrent upload
	// control.
	//
	// NOT "uploads freely". Seeding is still decided per torrent by
	// `internal/policy` from the consent tier, so AcquireFull widens *what may be
	// fetched* and never overrides a tier that forbids redistribution. The two
	// are different questions asked at different times, and conflating them is
	// invisible when it goes wrong.
	AcquireFull AcquireMode = "full"
)

// Errors. Separate sentinels because each names a different party's problem, and
// a caller acts differently on each.
var (
	// ErrNotPermitted means the mode forbids this action.
	//
	// Returned rather than silently returning false, because a caller that ignores
	// the error proceeds -- and a caller that only checks a bool proceeds anyway.
	// The error makes the refusal impossible to drop without being noticed.
	ErrNotPermitted = errors.New("acquisition: this mode does not permit that action")

	// ErrUnknownMode means a mode this build does not recognise.
	ErrUnknownMode = errors.New("acquisition: unrecognised acquire mode")
)

// Known reports whether m is a mode this build understands.
//
// FAIL CLOSED. An unrecognised value — from a newer build, a hand-edited row, a
// truncated database — is NOT treated as permissive. That is the same rule
// `internal/policy` applies to an unknown consent tier, and for the same reason:
// these are assertions, and an assertion nobody can be identified for is not
// permission to publish.
func Known(m AcquireMode) bool {
	switch m {
	case AcquireOff, AcquireFetchOnly, AcquireFull:
		return true
	}
	return false
}

// Parse turns a stored string into a mode, refusing anything unrecognised.
//
// The error is returned rather than defaulted, because defaulting is exactly the
// bug: a migration that writes a value this build does not know would silently
// become `off` or silently become `full`, and only one of those is loud.
func Parse(s string) (AcquireMode, error) {
	m := AcquireMode(s)
	if !Known(m) {
		return AcquireOff, fmt.Errorf("%w: %q", ErrUnknownMode, s)
	}
	return m, nil
}

// MayFetch reports whether this instance may fetch automatically.
//
// Returns an ERROR on refusal rather than a bare false, because a caller that
// ignores the error proceeds -- and a caller that only checks the bool proceeds
// anyway. The error makes the refusal impossible to drop without being noticed.
func (m AcquireMode) MayFetch() (bool, error) {
	if !m.mayFetch() {
		return false, m.fetchRefusal()
	}
	return true, nil
}

// mayFetch is the predicate behind MayFetch, separated so a caller that supplies
// its OWN reason (Decide) is not forced to discard it in favour of the generic one.
func (m AcquireMode) mayFetch() bool {
	switch m {
	case AcquireOff, AcquireFetchOnly, AcquireFull:
		return m != AcquireOff
	}
	// Unrecognised: refuse. Fail closed, same rule as an unknown consent tier.
	return false
}

// fetchRefusal says WHY fetching is not permitted, distinguishing a known mode
// that says no from a mode this build does not recognise. They are different
// problems and an operator acts differently on each.
func (m AcquireMode) fetchRefusal() error {
	if Known(m) {
		return fmt.Errorf("%w: %s does not fetch", ErrNotPermitted, m)
	}
	return fmt.Errorf("%w: %q is not a mode this build knows", ErrUnknownMode, string(m))
}

// MaySeed reports whether this instance may seed automatically.
//
// DISTINCT FROM MayFetch and not derivable from it, because the whole point of
// `fetch_only` is that these two answers differ. A caller that computed seeding
// from fetching would make the middle state unreachable, which is the exact
// failure a boolean causes and the reason this type exists.
func (m AcquireMode) MaySeed() (bool, error) {
	switch m {
	case AcquireOff, AcquireFetchOnly:
		return false, fmt.Errorf("%w: %s does not seed", ErrNotPermitted, m)
	case AcquireFull:
		return true, nil
	}
	return false, fmt.Errorf("%w: %q is not a mode this build knows", ErrUnknownMode, string(m))
}

// seedRefusal is the MaySeed counterpart of fetchRefusal, kept separate because
// the two messages differ and a caller showing the wrong one names the wrong
// action.
func (m AcquireMode) seedRefusal() error {
	if Known(m) {
		return fmt.Errorf("%w: %s does not seed", ErrNotPermitted, m)
	}
	return fmt.Errorf("%w: %q is not a mode this build knows", ErrUnknownMode, string(m))
}

// UploadsData reports whether bytes may leave this instance under this mode.
//
// A THIRD question, not a synonym for MaySeed. `AcquireFull` seeds, and seeding
// means uploading, but §6b.3's table separates "seeds" from "uploads data" — the
// latter is "per the existing upload control", which is `internal/policy`'s
// decision from the consent tier. So this method answers the mode's half and the
// per-torrent control answers the rest.
//
// Present because a caller asking "does this instance publish?" should not have to
// reason about which of the two upstream answers it needs.
func (m AcquireMode) UploadsData() bool {
	// Only `full` has a publish path at all, and even then the per-torrent upload
	// control decides. Deliberately not an error-returning method: the answer is
	// only ever used to decide whether to consult the control, and a bool that
	// cannot be wrong is the right shape here.
	return m == AcquireFull
}

// Decision is what the queue may do with one candidate.
type Decision struct {
	// Fetch is whether this candidate may be acquired automatically.
	Fetch bool
	// Reason explains a refusal.
	//
	// Present because §6b.6's migration story means an instance can be INHERITED
	// into a mode, and an operator who does not know why their queue is empty will
	// eventually work around the switch rather than read it.
	Reason string
}

// Decide answers §6b.3's question for one candidate: may this be acquired, given
// the mode, and given that we already have it?
//
// `alreadyHave` is a parameter because §6b.3's queue is explicitly "what the mesh
// holds, MINUS what this instance already has" — and a re-download of something
// already present is not an acquisition, it is wasted bandwidth and a confusing
// duplicate in the library.
//
// THE ORDER MATTERS and is the testable part: the mode is consulted FIRST, and
// `alreadyHave` never becomes the reason an opted-out instance reports an empty
// queue as though acquisition were broken. A refusal must name the switch, because
// the switch is what the operator can change.
func Decide(m AcquireMode, alreadyHave bool) Decision {
	// Asked DIRECTLY rather than through MayFetch, because MayFetch's error is a
	// permission error naming the mode -- and Decide needs to add WHY, in terms an
	// operator can act on. Routing it through MayFetch meant the first reason won
	// and R083 never reached the text, which the test caught.
	//
	// The switch is still consulted first: `mayFetch` is a pure predicate with no
	// side effects, and the permission-read enforcement of §6b.3 is a property of
	// the CALLER asking before it acts, not of this function's internals.
	mayFetch := m.mayFetch()
	if !mayFetch {
		if !Known(m) {
			// An unrecognised mode is a different problem from a switched-off one,
			// and conflating them would send an operator to read a settings screen
			// when the real answer is "this build does not understand the row".
			return Decision{
				Fetch: false,
				Reason: fmt.Sprintf("the stored acquire mode is %q, which this build "+
					"does not recognise (R083). It is refused rather than guessed at, "+
					"because an unrecognised value must not default to acquiring.", string(m)),
			}
		}
		return Decision{
			Fetch: false,
			Reason: fmt.Sprintf("automatic acquisition is %s for this instance (R083); "+
				"the switch is why the queue is empty, and nothing else is", m),
		}
	}
	if alreadyHave {
		return Decision{
			Fetch: false,
			// NOT an error and not a permission failure: the queue is correct.
			Reason: "this instance already holds this content",
		}
	}
	return Decision{Fetch: true}
}
