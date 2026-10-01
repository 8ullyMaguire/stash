package collab

import (
	"context"
	"errors"
)

// The three-state sharing switch. M8 step 8.1, R083, spec §6b.3.
//
//	off         fetches no, seeds no, uploads no
//	fetch_only  fetches yes, seeds NO, uploads no
//	full        fetches yes, seeds yes, uploads per the existing upload control
//
// # WHY THREE STATES AND NOT A BOOLEAN
//
// §6b.3 states the reason outright: "`fetch_only` is the state a privacy-conscious
// user actually wants and a boolean forces them to choose wrongly in one direction or
// the other."
//
// There is no pair of booleans that escapes this, and that is the part worth being
// explicit about, because the obvious repair is "add a second bool so `seeding` can be
// set independently". Two bools admit `fetch=false, seed=true`, which is an instance
// that seeds content it never fetched — either a bug or a deliberate open-seeder, and
// the type cannot say which. A three-value type makes that combination
// unrepresentable, which is the same argument as `ShareChoice` being a string rather
// than a bool: the third state is the one a bool erases, and a type that cannot hold
// it cannot be checked for it.
//
// SO EVERY METHOD HERE IS A REFUSAL OR A PERMISSION ABOUT SOMETHING SPECIFIC, and
// there is deliberately no `Allowed()` that answers "may this instance participate".
// §6b.3's table has three independent columns, so a single boolean would be a lossy
// summary of a decision nobody made — and a switch whose summary is lossy is a switch
// somebody will read instead of asking the real question.

// AutoAcquireStore reads and writes the instance's capability-2 posture.
//
// AN INTERFACE RATHER THAN A CONCRETE TYPE, for the reason `ModeStore` is one: the
// acquisition scheduler needs to be testable against a store that refuses, and a test
// that has to stand up a real database to check "the scheduler stops when the switch
// says off" is a test that can fail for reasons that have nothing to do with the
// scheduler.
//
// IT HAS NO SETTER-ON-THE-CONTEXT ESCAPE. The mode gets `WithMode`/`ModeFrom` because
// a resolver needs the posture request-scoped; the acquire switch is read at the point
// of enforcement on every acquisition decision, and caching it in a context would
// mean a switch changed mid-request is not seen until the next one. A switch that is
// read fresh every time is the property that makes "the operator turned it off and it
// stopped" true within one request rather than one session.
type AutoAcquireStore interface {
	AutoAcquire(ctx context.Context) (AutoAcquire, error)
	SetAutoAcquire(ctx context.Context, a AutoAcquire) error
}

// AutoAcquire is the instance's capability-2 posture: how much of the acquisition
// queue this instance acts on.
type AutoAcquire string

const (
	// AcquireOff does nothing. Fetches nothing, seeds nothing, uploads nothing.
	AcquireOff AutoAcquire = "off"

	// AcquireFetchOnly fetches and never seeds.
	//
	// THIS IS THE DEFAULT, and the default is the load-bearing decision in this file.
	// §6b.3's reasoning: an instance that fetches but never seeds "leaks no data
	// outward and cannot be made into a source for someone else", so opting a new
	// install in to the harmless half of the capability is not a leak.
	// Non-negotiable #7 is about the PUBLISH path, and fetch_only has no publish path.
	//
	// The alternative — defaulting to `off` — makes preservation (capability 3,
	// §6b.4) unreachable for everyone who never found a setting, which is how a
	// feature that exists to prevent loss becomes absent rather than declined.
	AcquireFetchOnly AutoAcquire = "fetch_only"

	// AcquireFull fetches and seeds. Uploads of DATA remain under the existing
	// upload control and this switch does not widen them.
	AcquireFull AutoAcquire = "full"
)

// acquireStates is the one list of the three, so Valid and the migration's CHECK and
// any enumeration agree. It is a function rather than a package var because a package
// var is mutable state, and a mutable list of valid states is a list somebody will
// append to.
func acquireStates() []AutoAcquire {
	return []AutoAcquire{AcquireOff, AcquireFetchOnly, AcquireFull}
}

// AcquireStates returns the permitted states, in increasing order of participation.
//
// EXPORTED because the database CHECK, an API resolver's enum and this package's
// Valid() must not be able to disagree about what the three are. A fourth state needs
// all of them changed, which is the review §6b.3's states are supposed to get.
func AcquireStates() []AutoAcquire { return acquireStates() }

// Valid reports whether the state is one the schema permits.
//
// AN UNKNOWN VALUE IS FALSE, never a default, and that direction is the one that
// matters: this value arrives from a database column, a wire message or an API
// argument, and treating an unrecognised one as the most permissive state would turn a
// typo into a publish. See `ShareChoice.Valid`, which makes the same argument.
//
// # WHY "" IS VALID, WHICH LOOKS LIKE THE OPPOSITE OF THE RULE ABOVE
//
// Because plan step 8.1 requires that "the switch's zero value must be `off`, so an
// unset column is the safe state rather than the harmful one" — and a struct literal or
// a context value that omits the field produces "".
//
// The two requirements look contradictory and are not: what has to be SAFE is not
// `Valid()`, it is every REFUSAL. And "" is the safest state there is:
//
//   - "" does not fetch, so an unset switch acquires nothing;
//   - "" does not seed, so an unset switch publishes nothing;
//   - and it is DISTINGUISHABLE from a recognised state, so a caller that meant to set
//     something and did not can still be told apart from one that deliberately chose
//     `off`.
//
// So "" reads AS off without BEING off. Making `Valid()` return true for "" would
// hide that distinction, which is why instead `FromStored` and `ParseAutoAcquire`
// resolve "" to AcquireOff at the boundary and this predicate stays strict — a value
// read from a wire is still required to name a state.
//
// The refusal behaviour is pinned by TestTheZeroValueIsOff and TestAnEmptyValueIsSafe,
// and the fact that "" is refused by the SCHEMA's CHECK is a separate matter: a stored
// "" cannot be written, so an unset column in the database is the default `fetch_only`
// and an unset value in Go is `off`. Those are different situations and they get
// different answers, on purpose.
func (a AutoAcquire) Valid() bool {
	for _, s := range acquireStates() {
		if a == s {
			return true
		}
	}
	return a == ""
}

// String satisfies fmt.Stringer, so a state in an error message reads as the operator
// typed it.
func (a AutoAcquire) String() string { return string(a) }

// Fetches reports whether this instance may acquire content into its library.
//
// `off` is the only state that refuses. Everything else fetches, including
// `fetch_only` — which is the whole reason the state exists.
func (a AutoAcquire) Fetches() bool {
	return a == AcquireFetchOnly || a == AcquireFull
}

// Seeds reports whether this instance may re-seed content it holds.
//
// THE MIDDLE STATE IS NOT SEEDING, and this is the single most important method in
// the file: an instance at `fetch_only` participates in the mesh and becomes, by
// accumulation, one of the sources the next instance fetches from. §6b.3 says
// fetch_only "cannot be made into a source for someone else", and that is a claim
// about this method returning false — if it returned true, the spec's central privacy
// argument for the default would be false while the code looked correct.
//
// An unknown state refuses. A state this build does not recognise is not permission to
// publish anything.
func (a AutoAcquire) Seeds() bool {
	return a == AcquireFull
}

// UploadsData reports whether this switch permits uploading instance data.
//
// ALWAYS FALSE for all three states, and that is not an oversight — it is §6b.3's
// table read literally, where `full`'s uploads column says "per the existing upload
// control". So this method answers "does the sharing switch widen data uploads", and
// it never does. Data uploads are governed by a different control and widening them
// here would make this switch a capability list, which is the shape the file header
// warns against.
func (a AutoAcquire) UploadsData() bool { return false }

// MayAcquire reports whether an acquisition may proceed at all.
//
// NAMED FOR THE OPERATION rather than derived from a summary, because "should this
// instance be acquiring" is the question a scheduler actually asks and the one whose
// wrong answer puts uninvited content in somebody's library. It is exactly Fetches,
// and exists as its own name so a caller reading the acquisition path does not have to
// know that seeding is a separate question.
func (a AutoAcquire) MayAcquire() bool { return a.Fetches() }

// MaySeed reports whether a seed may be offered for a held item.
//
// SEPARATE FROM MayAcquire on purpose. An instance that may fetch and may not seed is
// the ordinary, spec-blessed case, and a single combined predicate would make it
// unrepresentable — which is the boolean trap the three states exist to avoid.
func (a AutoAcquire) MaySeed() bool { return a.Seeds() }

// ErrAutoAcquireInvalid is returned when a caller supplies something that is not one
// of the three states.
//
// ITS OWN SENTINEL, not ErrShareChoiceInvalid, and the distinction is load-bearing
// rather than tidiness. The two switches govern different things: ShareChoice is a
// USER's per-library metadata answer and AutoAcquire is an INSTANCE's capability
// posture. A caller holding ErrShareChoiceInvalid knows it must not publish a user's
// metadata; handing it that error for a bad auto_acquire value would have it retry or
// degrade against the wrong switch, and the two have opposite remedies — a share
// refusal wants the user asked again, an acquire refusal wants the value rejected.
var ErrAutoAcquireInvalid = errors.New("not a valid auto_acquire state")

// ParseAutoAcquire validates a stored or supplied value.
//
// IT REFUSES AN UNRECOGNISED VALUE RATHER THAN DEFAULTING, and the error is a
// sentinel so a caller distinguishes "this state is not one I know" from "the
// database was unreachable". The tempting repair is to coerce to AcquireFetchOnly —
// the schema default — and that turns a schema bug into a state nobody chose.
func ParseAutoAcquire(s string) (AutoAcquire, error) {
	a := AutoAcquire(s)
	if a == "" {
		// The zero value resolves to off rather than being refused, which is what makes
		// "an unset value is the safe state" true at the boundary rather than only in
		// the methods. A stored "" is impossible (the CHECK refuses it), so this branch
		// is reached by a Go caller who omitted the field, not by a corrupted row.
		return AcquireOff, nil
	}
	if a.Valid() {
		return a, nil
	}
	return AcquireOff, ErrAutoAcquireInvalid
}
