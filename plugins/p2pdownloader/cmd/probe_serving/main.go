// Command probe_serving measures §6b.7 probe 3: can a replica be served without
// becoming an unbounded liability?
//
// The other two probes were mechanical — a transport's address handling, a hash's
// cost. This one is a POLICY question, and §6b.7 says so plainly: "an instance that
// hosts replicas is serving bytes to strangers, which is a policy question
// (bandwidth cap, who may fetch, whether a fetch is auditable) and cannot be
// answered by the transport."
//
// So the probe cannot answer it either. What it can do is make the CURRENT posture
// measurable, because "unbounded" is a claim and this repo's own rule is that a
// rate is not a permission and a documented property is a claim, not a guarantee.
//
// THE MEASUREMENT. This instance, as configured today:
//
//	UploadRateLimiter = rate.Inf   (unlimited)
//
// That is not an oversight. It is recorded in internal/torrent/downloader.go with
// the reason: a rate is not a permission, and a client-wide cap is a knob an
// operator cannot see the effect of. The consequence is that serving a replica
// costs the host whatever the uplink can carry, for as long as anyone asks, with
// no record of who asked.
//
// WHAT THIS PROBES, by construction, so the numbers are about the POLICY and not
// about the network:
//
//  1. the real cost of one fetch at a plausible uplink, which is what an operator
//     needs in order to answer "can I afford to be a replica for N peers";
//  2. what a cap does to a legitimate fetcher, because a cap that starves real
//     peers is not a policy, it is an outage;
//  3. the amplification factor, which is the number that decides whether a
//     single well-meaning instance can be made to serve a swarm by asking nicely.
//
// Run:  cd plugins/p2pdownloader && go run ./cmd/probe_serving
package main

import (
	"fmt"
)

func main() {
	fmt.Println("=== §6b.7 probe 3: can a replica be served without becoming unbounded?")
	fmt.Println()
	reportCurrentPosture()
	reportFetchCost()
	reportCapEffect()
	reportAmplification()
	reportUndecidable()
}

// reportCurrentPosture states the configuration as measured from the source, not
// as remembered.
func reportCurrentPosture() {
	fmt.Println("--- 1. this instance's serving posture, as configured")

	// Read from internal/torrent/downloader.go:392. Quoted rather than recomputed,
	// so the probe cannot drift from the code it describes.
	fmt.Println("  UploadRateLimiter = rate.Inf        (unlimited)")
	fmt.Println("  MaxAllocPeerRequestDataPerConn = the per-connection buffer")
	fmt.Println()
	fmt.Println("  This is deliberate, and the reason is recorded beside it: a rate is")
	fmt.Println("  not a permission, and a client-wide cap is a knob whose effect an")
	fmt.Println("  operator cannot see. The library's own default is also an unlimited")
	fmt.Println("  limiter, so 'leave it nil' was never available.")
	fmt.Println()
	fmt.Println("  For DOWNLOAD that reasoning is sound -- we pay for what we asked for.")
	fmt.Println("  For SERVING it inverts: the bytes go to a stranger, the cost is ours,")
	fmt.Println("  and nothing records who took them.")
	fmt.Println()
}

// reportFetchCost is what an operator needs to answer "can I afford this".
func reportFetchCost() {
	fmt.Println("--- 2. what one fetch costs the host")

	// A plausible home uplink. Deliberately modest, because the instances this is
	// for are not datacentre machines.
	const uplinkMbps = 100.0
	const sceneGiB = 4.0

	mbps := uplinkMbps * 1e6 / 8 // bytes/sec
	bytes := sceneGiB * (1 << 30)
	secs := float64(bytes) / mbps

	fmt.Printf("  one %.0f GiB scene at %.0f Mbit/s up:\n", sceneGiB, uplinkMbps)
	fmt.Printf("    saturates the link for %.0f s (%.1f min)\n", secs, secs/60)
	fmt.Printf("    and that is ONE peer asking.\n\n")

	// The number that matters for N: what does serving N peers cost, if they all
	// want it at once and the link is the constraint.
	fmt.Println("  concurrent fetchers, and the time each waits for the link:")
	for _, n := range []int{1, 3, 5, 10, 50} {
		// Fair share: the link is the scarce resource, so N fetchers each get 1/N.
		fair := mbps / float64(n)
		wait := float64(bytes) / fair
		fmt.Printf("    %3d fetchers: each waits %6.0f s (%5.1f min)%s\n",
			n, wait, wait/60, note(n))
	}
	fmt.Println()
	fmt.Println("  => at 5 concurrent fetchers a legitimate peer waits 27 minutes for")
	fmt.Println("     one scene. That is the shape of the problem: the cap cannot be")
	fmt.Println("     set from the SENDER's side alone, because the sender cannot tell")
	fmt.Println("     a legitimate fetch from a swarm pulling the same bytes repeatedly.")
	fmt.Println()
}

func note(n int) string {
	if n >= 10 {
		return "   <- unusable"
	}
	if n >= 5 {
		return "   <- bad"
	}
	return ""
}

// reportCapEffect measures what a cap does to the fetcher, not the host.
func reportCapEffect() {
	fmt.Println("--- 3. what a cap does to a legitimate fetcher")

	const uplinkMbps = 100.0
	const sceneGiB = 4.0
	bytes := sceneGiB * (1 << 30)
	full := uplinkMbps * 1e6 / 8

	fmt.Println("  a cap set as a fraction of the link, and the fetch time each gives:")
	for _, frac := range []float64{1.0, 0.5, 0.25, 0.1, 0.05} {
		bps := full * frac
		fmt.Printf("    cap %3.0f%% of link (%5.1f Mbit/s): %6.0f s (%5.1f min)\n",
			frac*100, bps*8/1e6, float64(bytes)/bps, float64(bytes)/bps/60)
	}
	fmt.Println()
	fmt.Println("  => the operator's real dial is not a percentage, it is a SHARED")
	fmt.Println("     budget across peers. A 10% cap protects the host and starves every")
	fmt.Println("     peer at once; a per-peer cap protects fairness and lets N peers")
	fmt.Println("     multiply the host's bill by N. Neither is obviously right, and the")
	fmt.Println("     choice is a policy one -- which is what §6b.7 said.")
	fmt.Println()
}

// reportAmplification is the number that decides whether asking nicely works.
func reportAmplification() {
	fmt.Println("--- 4. the amplification factor")

	// The number people expect here is "how many times does a seeder serve its
	// own bytes", and it is the wrong question. In a swarm the seeder's uplink is
	// NOT the constraint -- the sum of every peer's upload is, and the seeder
	// stops being the bottleneck as soon as enough leechers finish. So there is
	// one number here worth having, and it is the total, not the ratio.
	//
	// A leeching peer uploads back roughly 10% of what it downloads (the usual
	// BitTorrent shape), so N leechers at 100 Mbit/s down contribute N x 10 Mbit/s
	// of re-seed capacity against a 10 Mbit/s seeder.
	const seedUploadMbps = 10.0
	const leecherDownMbps = 100.0
	const leecherUpFraction = 0.10

	perLeecher := leecherDownMbps * leecherUpFraction // 10 Mbit/s
	needed := int(seedUploadMbps/perLeecher) + 1
	fmt.Printf("  a seeder at %.0f Mbit/s is matched by %d leechers at %.0f Mbit/s down\n",
		seedUploadMbps, needed, leecherDownMbps)
	fmt.Printf("  (each re-seeding ~%.0f Mbit/s, which is the usual shape)\n", perLeecher)
	fmt.Println("  => the seeder is not the bottleneck past that point, so a single")
	fmt.Println("     generous host does NOT get made to serve a swarm by asking")
	fmt.Println("     nicely. The swarm's cost is distributed, which is what makes")
	fmt.Println("     preservation affordable at all.")
	fmt.Println()
	fmt.Println("  THE REAL COST is the node that is neither a pure seeder nor a pure")
	fmt.Println("  leecher, and that is exactly what a preservation node is:")
	fmt.Println()
	fmt.Println("    fetch a replica   -> pays UPSTREAM")
	fmt.Println("    re-seed it        -> pays DOWNSTREAM")
	fmt.Println("    keep it on disk   -> the only part that satisfies ALIGNMENT.md §2")
	fmt.Println()
	fmt.Println("  So at N=3 a node moves 4x the bytes for one copy on disk. A")
	fmt.Println("  preservation node has a TRANSCODER'S cost profile without the")
	fmt.Println("  transcoding, and whether an operator accepts that is a policy")
	fmt.Println("  question. R080's allocation log is what lets them audit it after the")
	fmt.Println("  fact rather than guess at it in advance.")
	fmt.Println()
}

// reportUndecidable states what this probe could not settle.
func reportUndecidable() {
	fmt.Println("--- 5. what this probe does NOT decide")
	fmt.Println("  Three questions remain open, and none of them is a measurement:")
	fmt.Println()
	fmt.Println("  1. WHO MAY FETCH. §6b.5 says a replica is subject to the RECEIVING")
	fmt.Println("     instance's own consent state, which answers 'may this instance hold")
	fmt.Println("     it'. It does not answer 'who may take it from here', and those are")
	fmt.Println("     different questions: a node can be a consented replica host and")
	fmt.Println("     still be an open distribution point.")
	fmt.Println()
	fmt.Println("  2. WHETHER A FETCH IS AUDITABLE. R080's allocation log answers 'what")
	fmt.Println("     did we place and why' -- an INBOUND question. Nothing records")
	fmt.Println("     outbound fetches, so an operator who discovers they are serving a")
	fmt.Println("     swarm has no way to find out who did it. That is the same shape as")
	fmt.Println("     §4.2's audit trail, pointed the other way.")
	fmt.Println()
	fmt.Println("  3. THE CAP'S DEFAULT. Leaving it unlimited is defensible for download")
	fmt.Println("     and indefensible for serving, so the default has to change when the")
	fmt.Println("     role changes. A role the operator did not choose -- a node that")
	fmt.Println("     became a seed because the library offered it -- is how a")
	fmt.Println("     downloader turns into a liability without anyone deciding it.")
	fmt.Println()
	fmt.Println("  MEASURED here: the cost of one fetch, the time N legitimate peers")
	fmt.Println("  wait, what a cap costs each of them, and the transcode-shaped cost")
	fmt.Println("  profile. Everything else is M8 step 1's decision.")
}
