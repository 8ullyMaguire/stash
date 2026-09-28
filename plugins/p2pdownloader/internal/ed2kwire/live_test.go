//go:build ed2klive

// Live ed2k interop checks. NOT part of `go test ./...`.
//
// # WHY THIS FILE IS BEHIND A BUILD TAG
//
// Three reasons, and the third is the one that matters most.
//
//  1. A suite that depends on a third party's uptime is not a test. It fails
//     for reasons that have nothing to do with this code, and a developer
//     learns to ignore it — which is how a real interop break gets ignored too.
//
//  2. It costs real network time on every run, and M5's hermetic suite is
//     deliberately fast so it gets run.
//
//  3. A live test that SKIPS is worse than no live test: `t.Skip` prints
//     `ok` and the suite's green says nothing about whether interop works. So
//     these tests are not reachable by a plain `go test ./...` at all — the
//     build tag is the mechanism, and a skip is never the answer.
//
// # HOW TO RUN
//
//	go test -tags ed2klive ./internal/ed2kwire/ -v
//
// A failure here is a REAL finding about the wire layer. A timeout is usually
// not: most addresses in a nodes.dat are dead, and a public ed2k server may
// be down or may refuse a client it does not recognise.

package ed2kwire

import (
	"context"
	"io"
	"net/http"
	"os"
	"testing"
	"time"

	"github.com/monkeyWie/goed2k/protocol"
)

// liveServers are public ed2k servers used for the hello exchange.
//
// They are read from ED2K_LIVE_SERVERS when it is set, so this file does not
// hardcode a host that may be down in six months' time. The default is empty
// and an empty list is a FAILURE, not a skip: a live run that tested nothing
// and reported success is the exact outcome §3 above is about.
func liveServers(t *testing.T) []string {
	t.Helper()
	raw := os.Getenv("ED2K_LIVE_SERVERS")
	if raw == "" {
		t.Fatal("ED2K_LIVE_SERVERS is not set. A live run with no servers " +
			"would report success having tested nothing, which is worse " +
			"than not running. Set it to a comma-separated host:port list.")
	}
	var out []string
	for _, s := range splitComma(raw) {
		if s != "" {
			out = append(out, s)
		}
	}
	if len(out) == 0 {
		t.Fatal("ED2K_LIVE_SERVERS is set but empty after parsing")
	}
	return out
}

func splitComma(s string) []string {
	var out []string
	start := 0
	for i := 0; i <= len(s); i++ {
		if i == len(s) || s[i] == ',' {
			if i > start {
				out = append(out, s[start:i])
			}
			start = i + 1
		}
	}
	return out
}

// TestLiveAServerAcceptsOurHello is the highest-value live assertion.
//
// A wrong opcode or a wrong size field produces a server that never answers.
// Nothing hermetic can see that, because our own encoder and decoder agree
// with each other — which is precisely the failure mode the golden-bytes test
// exists to catch and the live test exists to confirm.
func TestLiveAServerAcceptsOurHello(t *testing.T) {
	for _, addr := range liveServers(t) {
		t.Run(addr, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(),
				30*time.Second)
			defer cancel()

			srv, err := Dial(ctx, addr)
			if err != nil {
				// A refusal here is a finding only if it is OUR refusal. A
				// refused CONNECTION is the server's business, so the
				// distinction matters for reading the failure.
				t.Fatalf("Dial(%s): %v", addr, err)
			}
			defer srv.Close()

			// A server that answered is a server that decoded our hello and
			// chose to reply. Report what it said, because a zero user count
			// is a real and common shape for a server that is up.
			t.Logf("connected: guid=%s users=%d files=%d",
				srv.Hash(), srv.Users(), srv.Files())
		})
	}
}

// TestLiveKadNodesDatDownloads: the nodes.dat is the only way a client learns
// where the Kad network is, and it is served over HTTP by the same
// infrastructure.
func TestLiveKadNodesDatDownloads(t *testing.T) {
	const url = "https://www.emule-security.org/nodes.dat"

	// Not using net/http here on purpose: a fetch that cannot be bounded is
	// the same unbounded-read hazard the packet reader has, and the point of
	// this layer is that every read from a stranger is bounded. So the
	// request is given a client with its own timeout.
	client := &http.Client{Timeout: 30 * time.Second}

	resp, err := client.Get(url)
	if err != nil {
		t.Fatalf("fetching %s: %v", url, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("%s returned %s", url, resp.Status)
	}

	raw, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		t.Fatalf("reading the nodes.dat: %v", err)
	}
	if len(raw) == 0 {
		t.Fatal("the nodes.dat was empty")
	}

	nodes, err := ParseNodesDat(raw)
	if err != nil {
		t.Fatalf("the live nodes.dat does not parse: %v", err)
	}
	if len(nodes) == 0 {
		t.Fatal("the live nodes.dat parsed to zero nodes")
	}
	t.Logf("%d nodes, first: %s", len(nodes), nodes[0].Addr())

	// Spot-check that the addresses are plausible rather than mirrored. A
	// byte-order slip produces valid-looking IPs in the wrong order, and
	// 0.0.0.0 or a multicast address in a contact list is the tell.
	for i, n := range nodes {
		if n.IP.IsUnspecified() || n.IP.IsMulticast() {
			t.Errorf("node %d (%s) is unspecified or multicast, which a "+
				"real contact list does not contain. Suspect the address "+
				"byte order", i, n.Addr())
			break
		}
	}
}

// TestLiveAKadNodeAnswersOurHello checks one real Kad node. It is the only
// check that can catch a node ID derived from the wrong thing, because a wrong
// ID produces no error — only a client that never finds a peer.
func TestLiveAKadNodeAnswersOurHello(t *testing.T) {
	raw := os.Getenv("ED2K_LIVE_NODES_DAT")
	if raw == "" {
		t.Fatal("ED2K_LIVE_NODES_DAT is not set (a path to a nodes.dat). " +
			"Without a real contact list this test would have nothing to " +
			"contact, and reporting success would be a lie.")
	}

	body, err := os.ReadFile(raw)
	if err != nil {
		t.Fatalf("reading %s: %v", raw, err)
	}
	nodes, err := ParseNodesDat(body)
	if err != nil {
		t.Fatalf("the nodes.dat does not parse: %v", err)
	}
	if len(nodes) == 0 {
		t.Fatal("the nodes.dat has no contacts")
	}

	self := NewNodeID(protocol.MustHashFromString("0123456789ABCDEF0123456789ABCDEF"))

	// Try up to ten nodes: most addresses in any contact list are dead, and
	// "the first one was down" is not a finding about this code.
	answered := 0
	var lastErr error
	for i := 0; i < len(nodes) && i < 10; i++ {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		got, err := Bootstrap(ctx, nodes[i], self)
		cancel()
		if err != nil {
			lastErr = err
			continue
		}
		answered++
		t.Logf("node %d (%s) answered: id=%s firewalled=%v",
			i, nodes[i].Addr(), got.ID.Hash, got.Firewalled)
	}

	if answered == 0 {
		t.Fatalf("none of the first 10 contacts answered Kad. Last error: "+
			"%v. This is USUALLY the contact list being stale rather than a "+
			"defect, but a systematic encoding error would look the same, "+
			"so it is reported rather than skipped", lastErr)
	}
}
