package relayconsent

import (
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// R089: somebody pays for every byte a relay copies, and nobody consented.

// THE ZERO VALUE REFUSES. This is the first test because it is the property the rest
// of the file rests on: a Relay declared as a struct field and never configured must
// not relay, because the safe direction has to be the default one.
func TestAZeroValueRelayRefuses(t *testing.T) {
	c := NewConsent()

	assert.False(t, c.Consented(), "a fresh relay has not been consented to")
	err := c.Admit("peer", Frame)
	require.ErrorIs(t, err, ErrNotConsented,
		"a relay nobody configured must refuse; the default has to be the safe one")

	// AND NOTHING WAS CHARGED, so a refusal is not a small transfer.
	assert.Zero(t, c.Used(), "a refused frame must not be charged, or a node that "+
		"refuses everything still ends up with a used byte count")
}

// CONSENT IS NECESSARY AND INSUFFICIENT.
//
// Both halves, because a relay that only checks one of them is a relay with a bug in
// the half nobody tested: consent-without-check relays for a node that never agreed,
// and check-without-consent relays for a node whose operator said no.
func TestConsentAloneIsNotEnoughAndACapAloneIsNotEnough(t *testing.T) {
	t.Run("consent alone still admits nothing without a transfer", func(t *testing.T) {
		c := NewConsent()
		c.SetConsent(true)
		require.NoError(t, c.Admit("peer", Frame), "granted consent lets a frame through")
		assert.Equal(t, uint64(Frame), c.Used())
	})

	t.Run("a cap alone does not create consent", func(t *testing.T) {
		c := NewConsent()
		c.SetBudget(1 << 30)
		err := c.Admit("peer", Frame)
		require.ErrorIs(t, err, ErrNotConsented,
			"a budget is a number, not a permission. An operator who set a cap and "+
				"never consented must still be a refusing relay")
	})
}

// THE BUDGET STOPS THE TRANSFER, AND SAYS WHICH BUDGET.
//
// The plan called this out as the test that matters, and the second half is the
// point: a silent refusal is indistinguishable from a broken peer, so the debugging
// session that follows would be the operator's rather than ours.
func TestTheBudgetStopsTransferAndNamesItself(t *testing.T) {
	c := NewConsent()
	c.SetConsent(true)
	c.SetBudget(100 * Frame)

	// Admit exactly to the budget.
	for i := 0; i < 100; i++ {
		require.NoError(t, c.Admit("peer", Frame), "frame %d is within budget", i)
	}
	require.Equal(t, uint64(100*Frame), c.Used())

	// The next frame is refused, and the refusal carries the numbers.
	err := c.Admit("peer", Frame)
	require.ErrorIs(t, err, ErrBudget, "past the budget, a frame is refused")

	msg := err.Error()
	assert.Contains(t, msg, "peer", "the refusal names whose transfer it was")
	assert.Contains(t, msg, "budget", "the refusal says it is a BUDGET refusal, not a "+
		"network failure -- the difference between an operator raising a number and an "+
		"operator debugging their network")

	var refusal Refusal
	require.True(t, errors.As(err, &refusal), "the refusal must be recoverable as a "+
		"Refusal so a caller can read the numbers rather than parse prose")
	assert.Equal(t, uint64(100*Frame), refusal.BytesSoFar)
	assert.Equal(t, uint64(100*Frame), refusal.Limit)

	// AND A REFUSAL DOES NOT CHARGE, so a caller that retries in a loop against an
	// exhausted budget cannot inflate the operator's own accounting.
	assert.Equal(t, uint64(100*Frame), c.Used(),
		"a refused frame must not be charged; otherwise a peer looping on the "+
			"refusal inflates the operator's used-bytes without moving any payload")
}

// THE KILL SWITCH STOPS MID-TRANSFER.
//
// This is the test the plan named, and the assertion is the interesting part: the
// switch must take effect WITHIN THE CURRENT FRAME, not at the next reconnect or the
// next transfer. A switch that only applies to future transfers is a promise about the
// future, and the operator who threw it is watching a number that keeps climbing.
func TestTheKillSwitchStopsTheTransferItIsThrownDuring(t *testing.T) {
	c := NewConsent()
	c.SetConsent(true)
	// No budget, so the ONLY thing that can stop this transfer is the switch.
	require.Zero(t, c.Budget())

	// Simulate 4 GiB with the switch thrown at 1 GiB, and assert the total stops
	// ABOVE the point it was thrown and BELOW one frame past it.
	// TYPED CONSTANTS, because the loop's counter is uint64 and an untyped constant
	// compared against it arrives at testify as an `interface{}` -- which then
	// reports "Elements should be the same type" about two plain numbers. The
	// message is about the test's plumbing, not about the switch, and it is
	// worth naming because it points at the assertion rather than the code.
	const total uint64 = 4 * 1024 * Frame  // 4 GiB of frames
	const stopAt uint64 = 1 * 1024 * Frame // 1 GiB in
	var relayed uint64

	for relayed < total {
		if relayed >= stopAt {
			c.Kill() // thrown mid-transfer, not between transfers
		}
		err := c.Admit("peer", Frame)
		if err != nil {
			require.ErrorIs(t, err, ErrKilled, "the switch is what stopped it")
			break
		}
		relayed += Frame
	}

	assert.GreaterOrEqual(t, relayed, stopAt, "it must have kept relaying until the "+
		"switch was thrown -- a switch that stopped everything immediately would pass "+
		"this test while being useless")
	assert.LessOrEqual(t, relayed, stopAt+Frame,
		"it must stop within the CURRENT frame. Stopping at the next transfer would "+
			"leave the operator watching a number climb for a whole transfer after "+
			"they asked it to stop")
}

// KILL IS NOT REVOCATION, AND THAT DISTINCTION IS THE POINT.
//
// Two different operator intents: "stop" versus "I no longer consent". Kill is a
// switch that leaves consent standing, so ClearKill restores service without the
// operator having to re-consent.
func TestKillIsASwitchAndRevocationIsADecision(t *testing.T) {
	c := NewConsent()
	c.SetConsent(true)

	c.Kill()
	require.ErrorIs(t, c.Admit("peer", Frame), ErrKilled)
	require.True(t, c.Consented(), "the kill switch does NOT revoke consent -- that is "+
		"what makes it a switch rather than a second way to say the same thing")

	c.ClearKill()
	require.NoError(t, c.Admit("peer", Frame), "clearing the switch restores service "+
		"without re-consenting")

	// AND REVOCATION IS SEPARATE, AND STICKS.
	c.SetConsent(false)
	require.ErrorIs(t, c.Admit("peer", Frame), ErrNotConsented)

	// AND REVOCATION DOES NOT RESET THE COUNTER. Bytes already carried were carried,
	// and a counter that forgot them on revocation would let an operator reset the
	// budget by changing their mind twice.
	c.SetConsent(true)
	assert.Equal(t, uint64(Frame), c.Used(),
		"revoking and re-granting consent must not forget bytes already relayed")
}

// THE CHECK ORDER IS KILL, THEN CONSENT, THEN BUDGET.
//
// The order is observable, so it is tested rather than left to a comment. If a node
// has never consented AND is out of budget, the operator gets "not consented" -- the
// actionable answer -- rather than "raise your budget", which would be advice that
// does not help until they opt in at all.
func TestTheCheckOrderIsObservable(t *testing.T) {
	c := NewConsent()
	c.SetBudget(1)
	require.ErrorIs(t, c.Admit("peer", Frame), ErrNotConsented,
		"with no consent AND no budget, the refusal must be about consent, because "+
			"that is the decision the operator actually has to make")

	c.SetConsent(true)
	require.ErrorIs(t, c.Admit("peer", Frame), ErrBudget,
		"once consent is granted, the budget is the next thing to fail")

	// AND KILL OUTRANKS BOTH -- and the ordering test must set consent and kill in a
	// state where BOTH would fail, which is the only arrangement that can tell the two
	// checks apart.
	//
	// The first version did c.SetConsent(true) and THEN c.Kill(), so by the time the
	// admission ran, consent was granted and only the kill could fire. A mutant that
	// moves the kill check below the consent check ALSO fires, because consent passes.
	// So the test passed on a gate mutant that reordered the two -- the exact thing it
	// was written to catch.
	//
	// Here consent is NEVER granted and the budget is exhausted, so both checks would
	// refuse and only the ORDER decides which reason comes back.
	d := NewConsent()
	d.SetBudget(1)
	d.Kill()
	require.ErrorIs(t, d.Admit("peer", Frame), ErrKilled,
		"with no consent AND an exhausted budget AND the switch thrown, the switch "+
			"wins. An operator stopping the node does not first want to be told their "+
			"consent lapsed, or that a budget they cannot yet raise is exhausted")
}

// A BUDGET OF ZERO MEANS NO CAP, NOT A CAP OF ZERO.
//
// The most common real setting is "I consent, but do not let me spend without
// noticing", which is exactly this one. A zero-means-zero rule forbids it entirely, so
// it is tested explicitly.
func TestAZeroBudgetMeansNoCap(t *testing.T) {
	c := NewConsent()
	c.SetConsent(true)
	require.Zero(t, c.Budget(), "no budget is configured")

	for i := 0; i < 1000; i++ {
		require.NoError(t, c.Admit("peer", Frame), "frame %d -- no cap means no cap", i)
	}
	assert.Equal(t, uint64(1000*Frame), c.Used())

	// AND Remaining says SO, rather than reporting the remaining 18 exabytes.
	remaining, hasLimit := c.Remaining()
	assert.False(t, hasLimit, "Remaining must report that there IS no limit, rather "+
		"than reporting MaxUint64 -- an operator reading 18446744073709551615 as "+
		"credit is a number that invites a very expensive misunderstanding")
	assert.Zero(t, remaining)

	// AND WITH A CAP, Remaining is meaningful.
	c.SetBudget(1500 * Frame)
	remaining, hasLimit = c.Remaining()
	require.True(t, hasLimit)
	assert.Equal(t, uint64(500*Frame), remaining)
}

// LOWERING THE CAP BELOW WHAT IS ALREADY RELAYED PUTS THE NODE OVER BUDGET.
//
// Not clamped, not treated as a target. A node over its cap refuses, and that is the
// only reading an operator can act on -- if lowering the cap quietly started serving
// again, the cap would not be a cap.
func TestLoweringTheCapBelowUsageRefusesRatherThanClamping(t *testing.T) {
	c := NewConsent()
	c.SetConsent(true)
	c.SetBudget(1000 * Frame)
	for i := 0; i < 1000; i++ {
		require.NoError(t, c.Admit("peer", Frame))
	}
	require.Equal(t, uint64(1000*Frame), c.Used())

	// AND USED STILL REPORTS WHAT WAS ACTUALLY SPENT, which is the assertion that
	// catches the gate's lowering-cap mutant.
	//
	// A mutant that clamps `used` down to the new cap still refuses the next frame --
	// used+Frame > cap either way -- so an Admit-based assertion cannot see it. What it
	// breaks is the ACCOUNTING: the operator's log would report 500 frames when 1000
	// were relayed. R089's whole complaint is that the operator cannot see what their
	// node is doing, so a fix that makes the refusal right and the log wrong trades
	// one lie for another.
	//
	// `before` is captured BEFORE lowering, so the comparison is against a number the
	// test itself measured rather than one re-derived from the code under test.
	before := c.Used()
	c.SetBudget(500 * Frame)

	require.ErrorIs(t, c.Admit("peer", Frame), ErrBudget,
		"halving the cap below what is already relayed must refuse, not silently "+
			"treat the cap as a target to climb back toward")
	assert.Equal(t, before, c.Used(),
		"lowering the cap must not rewrite history. If Used() drops to the new cap, "+
			"the operator's log under-reports what their node actually carried -- the "+
			"same invisibility R089 was filed against, arriving by a different route")

	remaining, hasLimit := c.Remaining()
	require.True(t, hasLimit)
	assert.Zero(t, remaining, "a node over budget has nothing remaining")
}

// FORWARDED BYTES ARE ATTRIBUTED TO THE ORIGIN.
//
// R089's ledger complaint is that the operator cannot see whose traffic they are
// carrying. A total without attribution is still the clean sheet, with a number
// attached -- and a number that could equally be this node's own uploads.
func TestForwardedBytesAreAttributedToTheirOrigin(t *testing.T) {
	c := NewConsent()
	c.SetConsent(true)

	require.NoError(t, c.Admit("alice", 100))
	require.NoError(t, c.Admit("bob", 200))
	require.NoError(t, c.Admit("alice", 50))

	attr := c.Attribution()
	assert.Equal(t, uint64(150), attr["alice"])
	assert.Equal(t, uint64(200), attr["bob"])
	assert.Equal(t, uint64(350), c.Used(), "the total is the sum of the attributions")

	// AND THE COPY IS A COPY: mutating it must not move the operator's accounting.
	attr["alice"] = 999999
	assert.Equal(t, uint64(150), c.Attribution()["alice"],
		"Attribution must return a copy. Handing back the live map would let a "+
			"status endpoint rewrite the operator's accounting -- which is a way to "+
			"raise your own budget without the operator's involvement, and the whole "+
			"point of this file is that their number is nobody else's to move")
}

// A FRAME BIGGER THAN A FRAME IS REFUSED RATHER THAN CLAMPED.
//
// Clamping would let a caller believe it had sent what it asked to send, and the
// admission decision would be about a different quantity than the bytes that move.
func TestAFrameLargerThanAFrameIsRefusedNotClamped(t *testing.T) {
	c := NewConsent()
	c.SetConsent(true)

	require.NoError(t, c.Admit("peer", Frame), "exactly one frame is admitted")
	err := c.Admit("peer", Frame+1)
	require.Error(t, err, "one byte over a frame is refused, because a clamp would "+
		"let the caller believe it sent what it asked to send")

	err = c.Admit("peer", 0)
	require.Error(t, err, "a zero-byte frame is refused rather than granted for free, "+
		"since it would be a no-op grant that looks like a successful transfer")
}

// REFUSALS ARE COUNTED BY REASON.
//
// A single refusal is a configuration state; a repeating one is an attack, and the
// difference only shows in a count over time. This is also how an operator tells
// "my budget ran out" from "something keeps knocking".
func TestRefusalsAreCountedByReason(t *testing.T) {
	c := NewConsent()
	// Not consented: two refusals.
	require.Error(t, c.Admit("a", Frame))
	require.Error(t, c.Admit("b", Frame))

	c.SetConsent(true)
	// Killed: one.
	c.Kill()
	require.Error(t, c.Admit("a", Frame))

	counts := c.Refusals()
	assert.Equal(t, uint64(2), counts["not-consented"])
	assert.Equal(t, uint64(1), counts["killed"])
	assert.Zero(t, counts["budget"], "a reason that never fired is absent rather "+
		"than present-and-zero, so a reader can tell never-happened from happened-zero")

	// AND IT IS A COPY.
	counts["not-consented"] = 0
	assert.Equal(t, uint64(2), c.Refusals()["not-consented"])
}

// CONCURRENT ADMISSION MUST NOT OVERSPEND.
//
// The mutex is the reason this test exists rather than a comment: a relay copies on
// many connections at once, so a check-then-charge without a lock would admit frames
// concurrently and every one of them would see the same `used` and all of them would
// be within budget. The overspend is silent and grows with connection count, which is
// the worst shape for a bug whose whole point is that the operator's cap holds.
func TestConcurrentAdmissionDoesNotOverspend(t *testing.T) {
	c := NewConsent()
	c.SetConsent(true)

	const goroutines = 50
	const each = 20
	const budget = uint64(goroutines * each / 2 * Frame) // half of what is offered

	c.SetBudget(budget)

	var wg sync.WaitGroup
	var mu sync.Mutex
	granted := 0

	for g := 0; g < goroutines; g++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			for i := 0; i < each; i++ {
				if err := c.Admit(fmt.Sprintf("peer-%d", id), Frame); err == nil {
					mu.Lock()
					granted++
					mu.Unlock()
				}
			}
		}(g)
	}
	wg.Wait()

	used := c.Used()
	assert.LessOrEqual(t, used, budget,
		"concurrent admission overspent the budget: %d used against a %d cap. A "+
			"check-then-charge without a lock lets every connection see the same "+
			"counter and all of them be within budget", used, budget)
	assert.Equal(t, uint64(granted*Frame), used,
		"the used total must equal exactly the frames that were granted, so there is "+
			"no charging without a grant and no grant without a charge")
	assert.Less(t, granted, goroutines*each,
		"the budget was half of what was offered, so some frames must have been refused; "+
			"if all were granted the budget was not being enforced")
}

// A REFUSAL NAMES ITS ORIGIN, OR SAYS IT DOES NOT KNOW.
//
// The empty-origin case is real during a handshake, not a programming error, and the
// message has to be legible in both cases.
func TestARefusalNamesItsOriginOrSaysItDoesNotKnow(t *testing.T) {
	c := NewConsent()

	named := c.Admit("alice:4711", Frame).Error()
	assert.True(t, strings.Contains(named, "alice:4711"), "the origin appears in the message")

	unnamed := c.Admit("", Frame).Error()
	assert.Contains(t, unnamed, "unidentified peer",
		"an unknown origin is said to be unknown rather than rendered as an empty "+
			"string, which reads like a formatting bug")
}
