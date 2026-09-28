package ed2kwire

import (
	"testing"
	"time"
)

// quickDeadlines shrinks the three dial timeouts for the hermetic suite.
//
// # WHY THE SUITE IS ALLOWED TO DO THIS, AND WHY IT MAY NOT DO IT QUIETLY
//
// The real deadlines are 8s and 15s, measured against 85.17.116.222 on
// 2026-09-28: 3093ms to the first byte, then 11.4s of quiet. Waiting that long
// for a local fake that has nothing to say would make this package's test
// suite take a quarter of a minute per test that exercises a quiet server, and
// a suite nobody runs protects nothing.
//
// So the tests shrink the deadlines, and two things keep that honest:
//
//  1. TestTheRealDeadlinesAreTheMeasuredOnes below pins the production values
//     against the measurements they came from. A test cannot make production
//     wrong by editing a var, because that test fails.
//  2. The shrink is done in ONE place, called from each test, rather than by
//     each test assigning to the vars. Three tests each doing their own
//     assignment is three tests that can disagree about what "fast" means.
//
// The fakes block rather than close, so a client that has read everything it
// expects must still wait for its drain deadline to expire. That wait is what
// this shrinks.
func quickDeadlines(t *testing.T) {
	t.Helper()

	oldDial := dialTimeout
	oldBurst := burstDrainTimeout
	oldFacts := factsDrainTimeout

	// 50ms is long enough that a loopback read never races it — a fake has
	// already written everything by the time the client reads — and short
	// enough that a test costs milliseconds.
	dialTimeout = 2 * time.Second
	burstDrainTimeout = 50 * time.Millisecond
	factsDrainTimeout = 50 * time.Millisecond

	// Restored when the test ends, not when the helper returns: a test that
	// calls this and then keeps working must not be left with the shrunk
	// values, and t.Cleanup is the only hook that runs in that order.
	t.Cleanup(func() {
		dialTimeout = oldDial
		burstDrainTimeout = oldBurst
		factsDrainTimeout = oldFacts
	})
}

// TestTheRealDeadlinesAreTheMeasuredOnes: the production values are pinned
// against the measurements that produced them.
//
// # THIS TEST IS THE POINT OF quickDeadlines
//
// The first version of the timeouts was 400ms, chosen because a loopback fake
// replies instantly. Every hermetic test passed. Against a real server, every
// connection was refused in 0.4s with "the server accepted the connection and
// then said nothing at all" — on a server that was about to answer, and did,
// 3.1 seconds later.
//
// A test suite cannot find that bug, because every fake it has is fast. This
// one records the numbers a real server produced, so the constants have a
// documented origin that a later reader can check against a real server rather
// than against a loopback.
func TestTheRealDeadlinesAreTheMeasuredOnes(t *testing.T) {
	// Measured on 85.17.116.222, three consecutive connections, 2026-09-28:
	// the first byte arrived after 3093ms, 3094ms and 3093ms.
	const measuredFirstByte = 3093 * time.Millisecond
	// Then a gap of 11.4s before the next four packets arrived.
	const measuredQuietGap = 11445 * time.Millisecond

	// The first deadline must clear the real first-byte latency with room
	// to spare. An equality here would be a deadline that fires exactly
	// when the server is answering, which is a coin flip.
	if burstDrainTimeout <= measuredFirstByte {
		t.Errorf("burstDrainTimeout is %s, but a real server took %s to "+
			"send its first byte. The deadline must exceed the latency it "+
			"was measured against, or every slow-but-working server is "+
			"reported as dead", burstDrainTimeout, measuredFirstByte)
	}

	// The second must clear the quiet gap, for the same reason.
	if factsDrainTimeout <= measuredQuietGap {
		t.Errorf("factsDrainTimeout is %s, but a real server went quiet "+
			"for %s mid-burst", factsDrainTimeout, measuredQuietGap)
	}

	// And the outer bound must exceed both, which is the relationship
	// TestTheDialDeadlineExceedsBothDrainDeadlines checks. Both are
	// asserted because the sum is the thing that must hold, and the
	// individual values are the things a reader will retune.
	if dialTimeout <= burstDrainTimeout+factsDrainTimeout {
		t.Errorf("dialTimeout is %s and the two drains sum to %s, so the "+
			"outer deadline fires first", dialTimeout,
			burstDrainTimeout+factsDrainTimeout)
	}
}
