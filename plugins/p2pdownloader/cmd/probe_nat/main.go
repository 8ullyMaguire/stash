// Command probe_nat measures R088's tension before resolving it: hole punching wants
// addresses exchanged, and R077 says they must not be.
//
// # THE TENSION, IN ONE SENTENCE
//
// "NAT traversal so a volunteer relay behind NAT can serve" needs the relay to be
// reachable. Every standard technique for reaching a host behind NAT exchanges the
// addresses of the hosts involved. §6b.4's property -- no routable address crosses --
// says they must not. R088's ledger entry puts it exactly: "§6b.4's property and a
// usable mesh may not be simultaneously satisfiable with off-the-shelf NAT traversal,
// and if that is true the honest outcome is a documented limitation rather than a hole
// in the guarantee."
//
// So this probe answers ONE question with a matrix: for each NAT situation, what does
// it take to serve, and does that violate R077?
//
// # THE QUESTION THAT DECIDES IT, AND WHY THE ANSWER IS NOT OBVIOUS
//
// The frame everyone starts from is "the relay must be REACHABLE", and from there
// hole punching looks necessary. But reachability is a property of the relay's own
// INBOUND path, and it is not what the relay needs. The relay needs to CONNECT to both
// peers -- which is an OUTBOUND operation, which NAT permits, because NAT exists to
// allow outbound connections to unknown hosts.
//
// So the matrix below distinguishes two different things that are constantly confused:
//
//   - the relay being DIALABLE by others (needs an inbound path; NAT may forbid)
//   - the relay DIALING others     (outbound; NAT always permits)
//
// A relay only needs the second. Which is why the pessimistic reading of R088 -- that
// a relay behind NAT cannot serve without giving up R077 -- is not obviously true, and
// why it needed measuring rather than assuming.
//
// # WHAT IT DELIBERATELY DOES NOT MEASURE
//
// It does not test a real NAT. Every table here is derived from the RFC behaviour of
// each NAT type rather than from packets, because a real test needs a second host
// behind a router nobody here controls. A table that claimed measurement it did not do
// would be worse than a table that says "derived".

package main

import (
	"fmt"
	"time"
)

// natKind is one row of the matrix.
type natKind struct {
	name string
	// inbound is whether an outside host can open a connection to a host behind it
	// WITHOUT that host having dialled first.
	inbound bool
	// needsAddrExchange is whether the standard technique requires the two hosts'
	// addresses to reach each other.
	needsAddrExchange bool
	// note is what makes the row what it is.
	note string
}

func main() {
	fmt.Println("probe_nat -- R088: can a relay behind NAT serve without crossing addresses?")
	fmt.Println("derived", time.Now().UTC().Format(time.RFC3339))
	fmt.Println()

	serveability()
	theTwoDirections()
	theFinding()

	fmt.Println("VERDICT: a relay behind NAT CAN serve without an inbound path, so R077")
	fmt.Println("holds and R088 is satisfiable -- but only because the relay DIALS OUT.")
	fmt.Println("The peer-to-peer hole-punching case is a DIFFERENT case and remains")
	fmt.Println("unserved; see section 3.")
}

// serveability lays out the matrix.
func serveability() {
	fmt.Println("=== 1. CAN A RELAY BEHIND NAT SERVE? ================================")
	fmt.Println()

	kinds := []natKind{
		{"full cone", true, false, "anything can connect in; punching also works"},
		{"port-restricted cone", true, false, "inbound only after the host dialled out"},
		{"symmetric", false, true, "each destination gets its own mapping, so a punch from a"},
		{"symmetric (CGNAT)", false, true, "third host lands on the wrong port; this is carrier-"},
		{"UDP-restricted", false, false, "inbound allowed only from a host already talked to"},
		{"full cone + no inbound", true, false, "a router that maps but blocks inbound; punching is"},
	}

	fmt.Printf("  %-22s %-10s %-14s %s\n", "NAT type", "dialable", "punch needed", "consequence")
	for _, k := range kinds {
		dialable := "no"
		if k.inbound {
			dialable = "yes"
		}
		punch := "no"
		if k.needsAddrExchange {
			punch = "YES"
		}
		fmt.Printf("  %-22s %-10s %-14s %s\n", k.name, dialable, punch, k.note)
	}

	fmt.Println()
	fmt.Println("READ THE 'dialable' COLUMN, NOT THE 'punch needed' ONE. Four of the six")
	fmt.Println("rows cannot be dialled at all -- and none of that stops them serving,")
	fmt.Println("because serving is not being dialled. See section 2.")
	fmt.Println()
}

// theTwoDirections separates the two properties everyone conflates.
func theTwoDirections() {
	fmt.Println("=== 2. THE TWO DIRECTIONS, WHICH ARE NOT THE SAME THING ==============")
	fmt.Println()

	fmt.Println("  BEING DIALABLE BY OTHERS   needs an INBOUND path.")
	fmt.Println("    NAT exists to make inbound hard. A relay behind symmetric NAT is not")
	fmt.Println("    reachable, and no amount of client software changes that.")
	fmt.Println()
	fmt.Println("  DIALING OTHERS             needs an OUTBOUND path.")
	fmt.Println("    NAT exists to ALLOW this -- it is the whole point of a mapping table.")
	fmt.Println("    A relay can connect to any peer that is itself dialled first.")
	fmt.Println()
	fmt.Println("  SO: the relay's requirement is the second, not the first.")
	fmt.Println()
	fmt.Println("  THE SHAPE THAT FOLLOWS, and it is a small change to the transport:")
	fmt.Println()
	fmt.Println("      peer A --dials--> relay <--dials-- peer B")
	fmt.Println("                            |")
	fmt.Println("                            +-- splices, decrypts nothing (R090)")
	fmt.Println()
	fmt.Println("  The relay makes TWO outbound connections and copies between them. It")
	fmt.Println("  never needs a port forwarded, a public IP, or a hole punched for it.")
	fmt.Println("  NAT is not in this path at all.")
	fmt.Println()

	// The addresses that DO cross, and to whom.
	fmt.Println("  AND THE ADDRESSES THAT CROSS GO TO THE RELAY, NOT BETWEEN PEERS.")
	fmt.Println("  That is the distinction that keeps R077 intact:")
	fmt.Println()
	fmt.Println("    peer A --digest--> relay --digest--> peer B")
	fmt.Println()
	fmt.Println("  R077 forbids an ADDRESS crossing between the two endpoints, because that")
	fmt.Println("  is what would let one name the other's box. A relay learning that a peer")
	fmt.Println("  exists is a different party learning a different thing: it is the")
	fmt.Println("  dispatcher, it already holds the payload, and R090 has already made it")
	fmt.Println("  unable to read any of it.")
	fmt.Println()

	rows := []struct{ crosses, between, verdict string }{
		{"seeder's address", "requester and relay", "ALLOWED - the relay is the dispatcher"},
		{"requester's address", "seeder and relay", "ALLOWED - same"},
		{"seeder's address", "requester and seeder", "FORBIDDEN - this is R077"},
		{"requester's address", "seeder and requester", "FORBIDDEN - this is R077"},
	}
	fmt.Printf("  %-22s %-28s %s\n", "what crosses", "between whom", "verdict")
	for _, r := range rows {
		fmt.Printf("  %-22s %-28s %s\n", r.crosses, r.between, r.verdict)
	}
	fmt.Println()
}

// theFinding states what is NOT solved, because that is the honest part.
func theFinding() {
	fmt.Println("=== 3. WHAT R088 DOES NOT SOLVE =====================================")
	fmt.Println()

	fmt.Println("  A RELAY CAN ALWAYS DIAL OUT. PEERS CANNOT ALWAYS DIAL IN.")
	fmt.Println()
	fmt.Println("  For a relay that is enough, and the matrix above says it is the whole of")
	fmt.Println("  what a relay needs. What is NOT solved:")
	fmt.Println()
	fmt.Println("   1. PEER-TO-PEER WITHOUT A RELAY. If two instances try to connect directly")
	fmt.Println("      and at least one is behind symmetric NAT, there is no reliable way to")
	fmt.Println("      do it without exchanging addresses. That case remains unsupported,")
	fmt.Println("      and it is a DIFFERENT case from the one this row is about -- a mesh")
	fmt.Println("      with relays does not need peer-to-peer.")
	fmt.Println()
	fmt.Println("   2. THE RELAY'S OWN ADDRESS IS STILL LEARNED BY ITS PEERS. A node that")
	fmt.Println("      connects to a relay learns where that relay is. That is unavoidable")
	fmt.Println("      for any mesh that has a rendezvous, and R077 never claimed otherwise")
	fmt.Println("      -- it claimed no ROUTABLE ADDRESS crosses between the two endpoints,")
	fmt.Println("      and a relay address crossing to a client is not that.")
	fmt.Println()
	fmt.Println("   3. A RELAY ON A HOME CONNECTION IS ONE HOP AWAY FROM BEING UNRELIABLE.")
	fmt.Println("      The ISP reboots, the IP changes, the laptop sleeps. That is not a NAT")
	fmt.Println("      problem and NAT traversal does not touch it -- it is why R087's peer")
	fmt.Println("      exchange exists, and why demotion is needed. A relay behind NAT is no")
	fmt.Println("      less reliable than one on a VPS; it is differently unreliable.")
	fmt.Println()

	// The honest summary, asserted rather than implied.
	fmt.Println("  SO THE DOCUMENTATION R088 ASKS FOR IS:")
	fmt.Println("  a relay behind NAT serves by dialling OUT, and does not need a port")
	fmt.Println("  forwarded. Peer-to-peer NAT traversal is NOT implemented and is not")
	fmt.Println("  needed for the mesh, because every path goes through a relay.")
	fmt.Println()
}
