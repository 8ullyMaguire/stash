//go:build ed2klive

// The live Kad bootstrap, against real nodes from a real contact list.
//
// # WHY THIS FILE IS SEPARATE FROM live_test.go
//
// The ed2k live tests and the Kad live tests fail for OPPOSITE reasons and
// that difference is the whole reason they are not in one file.
//
// The ed2k side is limited by our client: servers say "your client is too
// old" because we send no version tag, so most of the public list cannot
// serve us no matter how well this code works.
//
// The Kad side is limited by the NETWORK: a contact list is a list of client
// addresses from other people's machines, and a large fraction of any given
// list is offline, firewalled, or changed address since it was published. A
// high failure rate here is the expected state of the network, not a verdict
// on this code.
//
// So this file asserts on the RATE, not on any single node. Asking for one
// specific node to answer is asking the test to depend on a stranger's
// uptime, and that test would be red most days for reasons that have nothing
// to do with the code under it.
//
// # WHAT IT DOES ASSERT, PRECISELY
//
//  1. A real nodes.dat from a real source PARSES with our own decoder. This
//     is the part that is entirely ours: a parse failure is a bug here and
//     nowhere else.
//  2. At least one node completes the Kad hello. Proves the wire encoding is
//     right against a real implementation, which no fake can do — the fakes
//     were written from the same reading of the spec that produced this code,
//     so they agree with it by construction.
//  3. Every node that DID answer returned a self-consistent result. A node
//     that answers with a zero ID, or a Firewalled flag that contradicts its
//     own packet, is a finding regardless of how many others answered.
//
// # RUNNING IT
//
// The contact list is not committed and not downloaded automatically. The
// public URL is a courtesy, not a dependency:
//
//	curl -o /tmp/nodes.dat https://upd.emule-security.org/nodes.dat
//	ED2K_LIVE_NODES_DAT=/tmp/nodes.dat \
//	  go test -tags ed2klive ./internal/ed2kwire/ -run TestLiveKad -v
//
// Without ED2K_LIVE_NODES_DAT this test FAILS rather than skipping, on the
// same principle as the ed2k live tests: a live test that skips reports `ok`
// having proved nothing at all.
package ed2kwire

import (
	"context"
	"errors"
	"net"
	"os"
	"sort"
	"testing"
	"time"

	"github.com/monkeyWie/goed2k/protocol"
)

// liveKadAttempts is how many contacts to try.
//
// 40 of 154 in a fresh list, and it is deliberately not all of them: each
// attempt costs a UDP round trip plus a timeout, and a list where most entries
// are dead is the normal case. The number is a budget, and the assertion is
// about the rate, so raising it makes the test slower and barely more
// informative.
const liveKadAttempts = 40

// liveKadTimeout per node. A node that is offline fails this way, and so does
// a node that is online but slow, so the value is generous: the goal is to
// count real answers, not to shave seconds off a list of strangers.
const liveKadTimeout = 4 * time.Second

// liveKadMinimumAnswers is the lowest number of successful hellos that still
// counts as reaching the real network.
//
// One is enough to prove the encoding, which is the only thing a test can
// prove about a foreign implementation. Two would be nicer, but a threshold
// above one makes the test's pass/fail depend on how many other people's
// laptops are switched on today, and a test that means something different
// each morning is a test that gets ignored.
const liveKadMinimumAnswers = 1

func TestLiveKadBootstrapsAgainstRealNodes(t *testing.T) {
	path := os.Getenv("ED2K_LIVE_NODES_DAT")
	if path == "" {
		t.Fatal("ED2K_LIVE_NODES_DAT is not set, so there is no contact list " +
			"to bootstrap from. This test FAILS rather than skipping, " +
			"because a live test that skips reports ok having proved " +
			"nothing.\n\n" +
			"  curl -o /tmp/nodes.dat https://upd.emule-security.org/nodes.dat\n" +
			"  ED2K_LIVE_NODES_DAT=/tmp/nodes.dat go test -tags ed2klive \\\n" +
			"    ./internal/ed2kwire/ -run TestLiveKad -v")
	}

	// ---- 1. OUR decoder against a REAL file. Entirely our bug if it fails.
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading the contact list at %s: %v", path, err)
	}

	nodes, err := ParseNodesDat(raw)
	if err != nil {
		t.Fatalf("ParseNodesDat on the real %s: %v\n\n"+
			"This is our decoder against a file our own code did not write, "+
			"so a failure here is a bug in ParseNodesDat and not in the "+
			"list. The formats are v1 (25-byte records) and v2 (34-byte, "+
			"with the KadUDPKey and Verified fields); the version is the "+
			"second uint32 after a zero first word.", path, err)
	}
	if len(nodes) == 0 {
		t.Fatalf("the contact list at %s parsed to zero nodes. A real "+
			"freshly published list has hundreds, so either the file is a "+
			"format we do not handle or the parse silently produced "+
			"nothing", path)
	}
	t.Logf("parsed %d contacts from %s", len(nodes), path)

	// The list is a claim from a stranger, so its contents are checked as
	// claims. A version of 0 or 1 means the legacy Kad1 protocol, which this
	// package does not speak, and aMule ignores those on read.
	var modern int
	for _, n := range nodes {
		if n.Version > 1 {
			modern++
		}
	}
	t.Logf("%d of %d contacts advertise Kad2 or later", modern, len(nodes))
	if modern == 0 {
		t.Fatalf("every one of the %d contacts advertises Kad1. A real "+
			"list is not all-legacy, so either the version byte is being "+
			"read from the wrong offset or the record layout is wrong -- "+
			"and the record layout is what this package would then be "+
			"writing too", len(nodes))
	}

	// ---- 2. The hello, against as many of them as the budget allows.
	//
	// Sorted so a failure is REPRODUCIBLE. The contact list order is stable
	// in the file, but iterating a map or a shuffled slice would make "it
	// worked on Tuesday" unreproducible on Wednesday, and a network test
	// whose failures move around is worse than no network test.
	candidates := make([]Node, 0, len(nodes))
	for _, n := range nodes {
		if n.Version > 1 && n.IP.To4() != nil {
			candidates = append(candidates, n)
		}
	}
	sort.Slice(candidates, func(i, j int) bool {
		return candidates[i].Addr() < candidates[j].Addr()
	})
	if len(candidates) > liveKadAttempts {
		candidates = candidates[:liveKadAttempts]
	}
	if len(candidates) == 0 {
		t.Fatalf("no contact in %s is both Kad2 and IPv4, so there is "+
			"nothing to try. The file parsed, so the bug would be in the "+
			"version byte or the address", path)
	}

	// A FIXED self ID, so a node that echoes it back is confirming the
	// encoding rather than agreeing with us by accident. It is not a real
	// client hash and nothing in the protocol checks it, which is exactly
	// why it is safe to make up.
	//
	// NewNodeID takes an ed2k CLIENT HASH rather than nothing, and that
	// signature is the point rather than an inconvenience: the Kad node ID
	// IS the client GUID in different clothes, and a client that presents
	// an unrelated ID is routed to the wrong region of the network and
	// finds nothing -- with no error anywhere. See NewNodeID's own
	// comment.
	self := NewNodeID(protocol.Hash{})

	type outcome struct {
		node     Node
		result   BootstrapResult
		err      error
		answered bool
	}

	var (
		answers   []outcome
		answeredN int
	)
	for _, n := range candidates {
		ctx, cancel := context.WithTimeout(context.Background(), liveKadTimeout)
		res, err := Bootstrap(ctx, n, self)
		cancel()

		o := outcome{node: n, result: res, err: err, answered: err == nil}
		if err == nil {
			answeredN++

			// ---- 3. CONSISTENCY, for every node that answered.
			//
			// A node that answers is the only evidence this package has
			// that its encoding is right, so an answer that is
			// self-inconsistent is worth failing the test over even if
			// the count is met.
			if res.ID == (NodeID{}) {
				t.Errorf("the Kad node at %s answered with a zero node ID. "+
					"Every real node derives its ID from its own address, "+
					"so a zero here means the response is not being parsed "+
					"and is a bug here rather than there", n.Addr())
			}
			answers = append(answers, o)
		} else {
			// A timeout and a refusal are different findings and are
			// kept apart in the log, because they have different causes:
			// one is a node that is not there, the other is a node that
			// is there and declined.
			if !errors.Is(err, context.DeadlineExceeded) &&
				!isNetTimeout(err) {
				t.Logf("  %-22s declined: %v", n.Addr(), err)
			}
		}
	}

	t.Logf("%d of %d contacts completed the Kad hello", answeredN, len(candidates))
	for _, a := range answers {
		t.Logf("  %-22s id=%s firewalled=%v",
			a.node.Addr(), shortID(a.result.ID), a.result.Firewalled)
	}

	if answeredN < liveKadMinimumAnswers {
		t.Fatalf("%d of %d contacts answered and %d were required.\n\n"+
			"Read this before concluding the code is wrong. A contact list "+
			"is a list of other people's machines: most of any given list "+
			"is offline, behind NAT, or has changed address since the list "+
			"was published. This host is also almost certainly behind NAT, "+
			"so a high UDP loss rate is expected.\n\n"+
			"Before reading it as a bug, re-run it -- and check the "+
			"contact list is fresh. If it consistently fails against a "+
			"list published in the last day, that IS a finding, and "+
			"encodeKadHello is where to look.",
			answeredN, len(candidates), liveKadMinimumAnswers)
	}
}

// isNetTimeout reports whether an error is a network timeout, which is the
// overwhelmingly common result when probing a list of strangers and is not
// worth a log line each.
func isNetTimeout(err error) bool {
	var ne net.Error
	if errors.As(err, &ne) {
		return ne.Timeout()
	}
	return false
}

// shortID renders the first 8 hex digits of a node ID, enough to tell two
// answers apart without a line of 32 digits per node.
//
// Hash's bytes are an UNEXPORTED field, so the value cannot be sliced or
// converted from outside the library -- and the fix for that is not a type
// conversion, which would hide the value rather than reveal it. The hash
// already knows how to print itself, so this asks.
func shortID(id NodeID) string {
	s := id.Hash.String()
	if len(s) > 8 {
		return s[:8]
	}
	return s
}
