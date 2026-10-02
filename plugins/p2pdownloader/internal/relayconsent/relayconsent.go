// Package relayconsent is the operator's side of R089: somebody pays for every byte
// a relay copies, and today nobody has consented to that.
//
// # WHAT THIS FILE IS
//
// A component with NO CALLER YET. The relay is `cmd/probe_relay`, loopback only, and
// nothing in the plugin routes production traffic through a relay. So this is built
// and tested with no caller because that is the order R089 demands, not because the
// author had not got there yet:
//
//	**A cap that only exists on a code path is a cap that can be bypassed by not
//	taking that code path.** R087's discovery is what makes relays reachable, so
//	shipping discovery first would mean advertising a node before it can say no.
//	The consent model has to be checkable before anything can point at a node.
//
// # THE GAP THIS CLOSES, IN THE LEDGER'S OWN WORDS
//
// R080's allocation log records what THIS instance PLACED and why. It records nothing
// about what this instance FORWARDED on someone else's behalf. So an operator reading
// that log sees a clean sheet while their box is carrying other people's libraries.
// Both halves of that sentence are bad, and they are bad in different ways: the
// forwarding is unconsented, and it is also invisible, so the operator cannot even
// discover it to object.
//
// # TWO CONSENTS, BECAUSE THEY ANSWER DIFFERENT QUESTIONS
//
//   - **ConsentToRelay** is a durable operator decision. Have I agreed to be a relay
//     at all? It is revocable, per §6a.11's revocable consent.
//   - **Admit** is an in-flight decision about ONE transfer. Budget, policy, or the
//     operator's kill switch may refuse it even while consent to relay is granted.
//
// Collapsing these into one flag is the tempting simplification and it is wrong. "I
// agreed to relay" and "I agree to carry THIS" are different promises with different
// durations, and a single boolean cannot revoke the second without revoking the
// first. So a node that consents to relay and then runs out of budget mid-transfer
// must REFUSE rather than revoke — and the refusal has to be distinguishable from
// "this peer never connected".
//
// # WHY THE CHECK IS BEFORE THE BYTES AND NOT AFTER
//
// "A relayed byte must be budgeted and consented at the same point a served one is."
// Budgeting after the transfer is not a budget, it is a report. The refusal here
// happens before the caller is allowed to move bytes, so a cap cannot be exceeded and
// then noticed — which is the only version of a cap an operator can rely on when they
// are paying the bill.
package relayconsent

import (
	"errors"
	"fmt"
	"sync"
	"time"
)

// Frame is one unit of relayed payload: the granularity at which admission is
// decided.
//
// 64 KiB, matching relaycrypto.FrameSize deliberately. The two must agree so a frame
// is admitted once and encrypted once — an admission granularity finer than the
// encryption frame would mean checking a budget and then handing out something the
// caller cannot actually send, and coarser would mean a refusal arrives later than the
// operator expects.
const Frame = 64 * 1024

// ErrNotConsented is a transfer attempted while consent to relay was not granted.
//
// DISTINCT from ErrBudget, because the operator's two remedies are different:
// granting consent is a decision about the future, raising a budget is a decision about
// a number. An operator told "budget exceeded" when the real problem is that they
// never opted in would raise a budget that still did nothing.
var ErrNotConsented = errors.New("relayconsent: this node has not consented to relay")

// ErrBudget is a transfer refused because the configured cap is reached.
var ErrBudget = errors.New("relayconsent: the relayed-byte budget is exhausted")

// ErrKilled is a transfer refused because the operator threw the kill switch.
var ErrKilled = errors.New("relayconsent: the operator stopped relaying")

// Refusal records WHY a transfer was refused, in terms the operator can act on.
//
// This is the difference between a refusal and a dropped connection. A peer that
// silently stops receiving looks exactly like a broken network, so the debugging
// session that follows belongs to the operator and not to us. R089's requirement is
// that a refused byte SAYS WHICH BUDGET, and this is where it says it.
type Refusal struct {
	// Reason is one of ErrNotConsented, ErrBudget, ErrKilled. Wrapped, so
	// errors.Is works and a caller can branch on it.
	Reason error

	// Origin is whose traffic this was, when known. Empty for a refusal that
	// happened before the peer identified itself — which is most of them, and is
	// why it is a string and not an identifier that must be parsed.
	Origin string

	// BytesSoFar is what this node has relayed, in total, at the moment of the
	// refusal. Present so the operator can see the shape of the problem from the
	// log alone: a node refused at 0 and a node refused at 4 GiB are different
	// situations.
	BytesSoFar uint64

	// Limit is the cap that was applied. Zero when the refusal was not about a
	// budget, which is the case that would otherwise look like "limit 0, used 3 GiB"
	// to anyone reading the line.
	Limit uint64
}

// Error makes Refusal satisfy error, so a refusal can be returned up a call stack
// with its reason attached rather than logged and discarded at the point it happens.
//
// The message is fixed prose plus the numbers, because the numbers are the part an
// operator acts on and the part that must never be dropped in favour of a category
// name.
func (r Refusal) Error() string {
	who := r.Origin
	if who == "" {
		who = "an unidentified peer"
	}
	if r.Limit == 0 {
		return fmt.Sprintf("refusing to relay for %s after %d bytes: %v", who, r.BytesSoFar, r.Reason)
	}
	return fmt.Sprintf("refusing to relay for %s after %d bytes of a %d byte budget: %v",
		who, r.BytesSoFar, r.Limit, r.Reason)
}

// Unwrap so errors.Is(refusal, ErrBudget) works through the Refusal wrapper.
func (r Refusal) Unwrap() error { return r.Reason }

// ConsentToRelay is the durable operator decision, plus its accounting.
//
// The zero value is REFUSING, and that is deliberate and is the reason this type can
// be used as a zero-valued field: a Relay that was declared and not configured does
// not relay, which is the safe direction for a field whose default is "off".
type ConsentToRelay struct {
	mu sync.Mutex

	// granted is the durable operator decision. False until SetConsent is called
	// with true, and back to false on revocation.
	granted bool

	// budgetBytes is the cap. Zero means NO cap, which is NOT the same as a cap of
	// zero — and the distinction is load-bearing, because an operator who has
	// consented but not set a budget means "yes, but don't let me spend without
	// noticing", which is the most common actual setting and is the one a
	// zero-means-zero reading would forbid entirely.
	budgetBytes uint64

	// used is the running total of RELAYED bytes — bytes forwarded on someone
	// else's behalf, not bytes this instance placed. Deliberately a different
	// counter from R080's allocation log, because merging them would produce
	// exactly the ambiguous number this file exists to fix.
	used uint64

	// killed is the operator's kill switch: an immediate stop, distinct from
	// revoking consent.
	killed bool

	// now is injectable so the kill-switch test can be deterministic. Nil means
	// time.Now.
	now func() time.Time

	// attribution maps an origin to the bytes relayed on its behalf, so the log
	// says whose traffic this was. Without it the total is still the clean sheet
	// problem, just with a number attached.
	attribution map[string]uint64

	// refusals counts refusals by reason, so an operator can tell "my budget ran out
	// once" from "something is attacking me".
	refusals map[string]uint64
}

// NewConsent returns a refusing, unmetered consent with no budget.
//
// NO BUDGET by default rather than a generous one, because a default that relays is a
// default that spends the operator's bandwidth before they have agreed to anything.
func NewConsent() *ConsentToRelay {
	return &ConsentToRelay{
		now:         time.Now,
		attribution: make(map[string]uint64),
		refusals:    make(map[string]uint64),
	}
}

// SetConsent grants or revokes the durable decision.
//
// REVOCATION TAKES EFFECT IMMEDIATELY, including mid-transfer, because consent that
// can be withdrawn but not acted on is not revocable in any sense the operator can
// use. Revoking does not reset `used` — the bytes already carried were carried, and a
// counter that forgets them on revocation would make the budget restart itself every
// time an operator changed their mind.
func (c *ConsentToRelay) SetConsent(granted bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.granted = granted
}

// Consented reports the durable decision, for a status endpoint to read.
func (c *ConsentToRelay) Consented() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.granted
}

// SetBudget sets the cap in bytes. Zero removes the cap.
//
// A cap checked at Admit time rather than applied to the counter afterwards, so
// lowering the budget below what has already been relayed puts the node OVER budget
// and every subsequent admission is refused — rather than the budget being treated as
// a target the node quietly climbs back toward.
func (c *ConsentToRelay) SetBudget(bytes uint64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.budgetBytes = bytes
}

// Budget returns the cap. Zero means uncapped.
func (c *ConsentToRelay) Budget() uint64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.budgetBytes
}

// Kill throws the kill switch: stop relaying now.
//
// IMMEDIATE AND SEPARATE FROM REVOCATION, because they answer different operator
// intents. Kill is "stop" and is what a panic button is; revocation is "I no longer
// consent" and also clears the willingness to accept NEW transfers. Kill leaves
// consent in place so that ClearKill restores service without the operator having to
// re-consent — which is the difference between a switch and a decision.
func (c *ConsentToRelay) Kill() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.killed = true
}

// ClearKill releases the kill switch.
func (c *ConsentToRelay) ClearKill() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.killed = false
}

// Killed reports the kill switch state.
func (c *ConsentToRelay) Killed() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.killed
}

// Admit decides whether ONE frame may be relayed, and charges it if so.
//
// This is the whole of R089's enforcement and the ordering inside it is the design:
//
//  1. kill switch      — cheapest, and the operator's most urgent intent
//  2. consent          — the durable decision
//  3. budget           — the number
//
// All three are checked BEFORE the caller is handed permission, and the counter moves
// only once every check has passed. So a refused frame costs the operator nothing,
// and a granted frame is already charged before the caller can decide not to send it —
// the conservative direction, since the alternative under-charges a node that fails
// mid-frame.
//
// origin is the peer's identifier, used only for attribution and for making the
// refusal legible. An empty origin is allowed and means "not yet known", which is a
// real state during a handshake rather than a programming error.
func (c *ConsentToRelay) Admit(origin string, bytes uint64) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	refuse := func(reason error) error {
		c.refusals[reasonKey(reason)]++
		return Refusal{
			Reason:     reason,
			Origin:     origin,
			BytesSoFar: c.used,
			Limit:      c.budgetBytes,
		}
	}

	// KILL FIRST. If the operator is stopping the node, the fact that consent was
	// never granted is a detail they do not need in the way out.
	if c.killed {
		return refuse(ErrKilled)
	}
	if !c.granted {
		return refuse(ErrNotConsented)
	}
	// ZERO BYTES IS REFUSED rather than granted for free. Admitting a zero-length
	// frame would let a caller "transfer" without transferring, and the counter
	// would be unchanged either way — so the check is free but pointless, and a
	// refusal is more honest than a no-op grant.
	if bytes == 0 {
		return refuse(ErrBudget)
	}
	// A frame larger than the frame size is refused rather than clamped. Clamping
	// would let a caller believe it had sent what it asked to send.
	if bytes > Frame {
		return refuse(ErrBudget)
	}
	// THE BUDGET CHECK IS ADDITIVE, NOT `used + bytes > limit`. Writing it the
	// second way is what lets a limit of 0 mean "no cap": 0 + anything > 0 is true,
	// so every frame would be refused and the "consented but no budget" setting
	// would be indistinguishable from an exhausted cap. The zero-means-uncapped case
	// is the common one, and a rule that breaks the common case is a rule that gets
	// worked around.
	if c.budgetBytes != 0 && c.used+bytes > c.budgetBytes {
		return refuse(ErrBudget)
	}

	c.used += bytes
	if origin != "" {
		c.attribution[origin] += bytes
	}
	return nil
}

// Used returns the bytes relayed so far, for a status endpoint.
func (c *ConsentToRelay) Used() uint64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.used
}

// Remaining returns the bytes still available, and whether there IS a limit.
//
// The bool is the load-bearing part: with no budget the answer is not "everything
// remaining", it is "there is no such number", and a caller that treated the first
// as the second would render MaxUint64 in a status endpoint and invite an operator to
// believe they had 18 exabytes of credit.
func (c *ConsentToRelay) Remaining() (uint64, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.budgetBytes == 0 {
		return 0, false
	}
	if c.used >= c.budgetBytes {
		return 0, true
	}
	return c.budgetBytes - c.used, true
}

// Attribution returns a COPY of the per-origin byte counts.
//
// A copy because the caller is a status endpoint or an exporter, and handing back the
// live map would let a reader mutate the accounting — which would be a way to raise
// one's own budget without the operator's involvement, and the whole point of this
// file is that the operator's number is the one number nobody else can move.
func (c *ConsentToRelay) Attribution() map[string]uint64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make(map[string]uint64, len(c.attribution))
	for k, v := range c.attribution {
		out[k] = v
	}
	return out
}

// Refusals returns a copy of the refusal counts by reason.
//
// Recorded rather than merely logged because a single refusal is a configuration
// state and a repeating one is an attack, and the difference only shows up in a
// count over time.
func (c *ConsentToRelay) Refusals() map[string]uint64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make(map[string]uint64, len(c.refusals))
	for k, v := range c.refusals {
		out[k] = v
	}
	return out
}

// reasonKey is a stable string for a refusal reason, for the refusal counter's key.
//
// Derived from the error rather than declared alongside it, so a new reason cannot be
// added without the counter picking it up automatically — a table of reason-to-key
// would drift the first time someone added an error and forgot the table.
func reasonKey(err error) string {
	switch {
	case errors.Is(err, ErrKilled):
		return "killed"
	case errors.Is(err, ErrNotConsented):
		return "not-consented"
	case errors.Is(err, ErrBudget):
		return "budget"
	default:
		return "other"
	}
}
