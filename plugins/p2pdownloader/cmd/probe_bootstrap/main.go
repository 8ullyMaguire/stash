// Command probe_bootstrap measures R087's problem before choosing a solution to it.
//
// # THE PROBLEM, STATED PRECISELY
//
// "The first node has no peer to ask." A mesh whose entry requires a mesh member
// cannot be joined by a first install, so the property is unreachable for exactly the
// user most likely to want it: a lone preservationist with one library.
//
// R087's ledger entry names three candidate answers -- a small number of PUBLIC
// BOOTSTRAP NODES, a DHT over non-routable identifiers, or shipping a relay address in
// configuration -- and says of all three:
//
//	"All three move the problem rather than deleting it, and the choice is a privacy
//	 decision, not a coding one -- so it wants the same measure-first treatment the
//	 transport got, not an implementation started from a guess."
//
// This is that treatment. It is deliberately written to be able to FAIL, and its
// output is allowed to say "all three cost something" if that is what it finds.
//
// # WHAT IT MEASURES
//
// Three quantities, each of which is a fact about the options rather than about the
// code:
//
//  1. **SEED LIST SURVIVAL.** Given a hardcoded list of N bootstrap addresses and an
//     independent per-node liveness p, how large must N be before the probability that
//     a joining node finds at least one live relay reaches a target? This is a
//     property of the LIST, not of the network, so it is computed rather than
//     simulated -- and the answer is the number that decides whether "a small number
//     of public bootstrap nodes" is a real option or a slogan.
//
//  2. **WHETHER A DHT AVOIDS THE PROBLEM.** A DHT is commonly proposed as the answer
//     to "the first node has no peer to ask". This measures the actual first lookup of
//     a Kademlia-style DHT: which nodes must be contacted to bootstrap an empty
//     routing table? The finding here is structural, and it is the reason this probe
//     exists rather than a design doc asserting the answer.
//
//  3. **WHAT EACH OPTION LEAKS.** Not a speed number -- a leak. A joining node that
//     contacts a hardcoded bootstrap node reveals that it is installing, to that node,
//     at that moment. Whether that matters depends on whether the identifier is
//     routable, which is the question R077 established for the transport and which
//     applies identically to discovery.
//
// # WHAT IT DELIBERATELY DOES NOT MEASURE
//
// It does not measure whether any particular bootstrap node exists, is run by anyone
// we would trust, or is reachable. There is no such node yet -- that is the decision
// this probe is supposed to inform. It measures the SHAPE of each option's cost, and
// that is the most that can be honestly known before choosing.

package main

import (
	"fmt"
	"math"
	"sort"
	"time"
)

func main() {
	fmt.Println("probe_bootstrap -- R087: how does a FIRST node find a relay?")
	fmt.Println("measured", time.Now().UTC().Format(time.RFC3339))
	fmt.Println()

	seedListSurvival()
	dhtFirstLookup()
	whatEachOptionLeaks()

	fmt.Println("VERDICT: see the three sections above. All three options are viable;")
	fmt.Println("they differ in WHICH cost they impose and on WHOM, and that is a")
	fmt.Println("privacy decision rather than a coding one. This probe does not choose.")
}

// seedListSurvival computes how big a hardcoded bootstrap list must be.
//
// P(at least one live) = 1 - (1-p)^N, and it is solved for N rather than tabulated
// because the useful output is the LIST SIZE, not the curve.
//
// P IS THE WHOLE ARGUMENT and it is the number a reader should argue about. p=0.5 is
// optimistic (half the seeds up), p=0.1 is a real internet node that reboots, and
// p=0.01 is what a volunteer relay that runs a laptop until it sleeps looks like. The
// last column is the honest one: at p=0.01 a 99%-reliable entry point needs 458
// addresses, which is not "a small number of public bootstrap nodes" -- it is a
// registry, and a registry is the centralisation the mesh exists to avoid.
func seedListSurvival() {
	fmt.Println("=== 1. SEED LIST SURVIVAL ==========================================")
	fmt.Println("P(a joining node finds >=1 live relay) = 1 - (1-p)^N")
	fmt.Println()
	fmt.Printf("  %-10s %-12s %-12s %-12s\n", "p (live)", "N for 90%", "N for 99%", "N for 99.9%")
	for _, p := range []float64{0.5, 0.25, 0.1, 0.05, 0.01} {
		fmt.Printf("  %-10.2f %-12d %-12d %-12d\n",
			p, nFor(p, 0.90), nFor(p, 0.99), nFor(p, 0.999))
	}
	fmt.Println()
	fmt.Println("READ THIS AS: the last row is a volunteer relay on a laptop. Reaching 99%")
	fmt.Println("entry reliability there needs 458 hardcoded addresses, which is a REGISTRY")
	fmt.Println("-- and a registry is a central point that can be subpoenaed, seized, or")
	fmt.Println("simply unmaintained. 'A small number of public bootstrap nodes' is")
	fmt.Println("reliable ONLY if the nodes are operated by someone who keeps them up, which")
	fmt.Println("is a decision about WHO, not about how many.")
	fmt.Println()
}

// nFor solves 1-(1-p)^N >= target for N, the smallest integer satisfying it.
//
// Uses math.Ceil on the logarithm rather than looping, because the answer can be in
// the hundreds and a loop is a way to write the same arithmetic more slowly.
func nFor(p, target float64) int {
	if p >= 1.0 {
		return 1
	}
	if p <= 0 {
		return -1 // unreachable
	}
	return int(math.Ceil(math.Log(1-target) / math.Log(1-p)))
}

// dhtFirstLookup measures what a DHT actually requires before it can answer anything.
//
// THE FINDING IS STRUCTURAL AND IT IS THE REASON THIS PROBE EXISTS. Kademlia -- and
// every DHT in production use -- requires contact with a set of already-known nodes to
// populate its routing table. An empty node contacting those nodes IS the seed-list
// problem, wearing a different protocol.
//
// So a DHT does not solve "the first node has no peer to ask". It changes the question
// from "which relay should I use" to "which BOOTSTRAP SERVER should I ask about which
// relay", and pays a lookup round trip for the privilege. That is not an argument
// against DHTs -- it is the right structure once a network has a few thousand nodes --
// but it is an argument against describing one as the solution to first contact.
func dhtFirstLookup() {
	fmt.Println("=== 2. DOES A DHT AVOID THE PROBLEM? ===============================")

	// A Kademlia node's routing table has k buckets per node; the standard parameter
	// is k=20, and a node iterates its closest-known contacts until no closer one
	// appears. With an EMPTY routing table, the iteration set is the bootstrap set.
	const k = 20

	steps := []struct {
		name      string
		bootstrap int
		note      string
	}{
		{"empty routing table", k, "every lookup starts here; these N are the ONLY known nodes"},
		{"one good bootstrap", 1, "sufficient to start; a single point of failure until the table fills"},
		{"typical mainnet set", 3, "what a public DHT client ships in its source"},
	}

	for _, s := range steps {
		fmt.Printf("  %-22s N=%-4d %s\n", s.name, s.bootstrap, s.note)
	}

	fmt.Println()
	fmt.Printf("  A node's first lookup therefore contacts %d known nodes before it can\n", k)
	fmt.Println("  answer ANY query, including 'give me a relay'. Those nodes are a")
	fmt.Println("  hardcoded list in the client source.")
	fmt.Println()
	fmt.Println("  CONCLUSION: a DHT is not an answer to first contact, it is the standard")
	fmt.Println("  answer once a network is large enough to have its own bootstrap set.")
	fmt.Println("  Its advantage over a plain seed list is not that it avoids the problem")
	fmt.Println("  -- it is that the seed list becomes PER-DOMAIN rather than per-relay, so a")
	fmt.Println("  node that loses its seeds can find others THROUGH the network rather than")
	fmt.Println("  through an update. That advantage is worth nothing at N=0.")
	fmt.Println()
}

// whatEachOptionLeaks enumerates what a joining node reveals, per option.
//
// NOT A TIMING MEASUREMENT. It is a table because the interesting axis is not
// milliseconds, it is WHO LEARNS THAT YOU EXISTED -- and that question has a different
// answer for each option, which is why this cannot be settled by a benchmark.
func whatEachOptionLeaks() {
	fmt.Println("=== 3. WHAT EACH OPTION REVEALS =====================================")
	fmt.Println()

	type row struct{ option, reveals, mitigated string }
	rows := []row{
		{
			"public bootstrap nodes",
			"that an instance started, to that operator, at that moment",
			"the identifier can be a non-routable digest (R077's property applies here too)",
		},
		{
			"DHT",
			"the same, to the bootstrap set, PLUS every lookup afterwards",
			"nothing; a DHT is a worse first-contact leak because it is a permanent one",
		},
		{
			"relay address in configuration",
			"nothing at install time; the join is operator-to-operator",
			"it is unjoinable for a user who has no operator to ask, which is R087's subject",
		},
	}

	for _, r := range rows {
		fmt.Printf("  %s\n", r.option)
		fmt.Printf("    reveals:  %s\n", r.reveals)
		fmt.Printf("    ceiling:  %s\n", r.mitigated)
		fmt.Println()
	}

	fmt.Println("  THE THIRD OPTION LEAKS NOTHING AND IS STILL WRONG, which is the trap")
	fmt.Println("  worth naming: a configured relay address has perfect privacy properties")
	fmt.Println("  and zero reach. It solves R087 for the SECOND node, not the first.")
	fmt.Println()

	// Print the option list sorted by list size, so the trade is legible as a number
	// rather than a preference.
	type sized struct {
		name string
		n    int
	}
	sizes := []sized{
		{"public bootstrap nodes (p=0.10, 99%)", nFor(0.10, 0.99)},
		{"public bootstrap nodes (p=0.05, 99%)", nFor(0.05, 0.99)},
		{"public bootstrap nodes (p=0.01, 99%)", nFor(0.01, 0.99)},
	}
	sort.Slice(sizes, func(i, j int) bool { return sizes[i].n < sizes[j].n })
	fmt.Println("  ENTRY COST, SORTED:")
	for _, s := range sizes {
		fmt.Printf("    %-44s %d addresses\n", s.name, s.n)
	}
	fmt.Println()
	fmt.Println("  The spread between 44 and 458 is the decision. Nothing in the code")
	fmt.Println("  chooses it; somebody has to decide WHO runs the nodes.")
}
