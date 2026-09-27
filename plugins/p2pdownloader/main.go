// Command stash-plugin-p2pdownloader is the StashForge P2P downloader plugin.
//
// # WHY THIS IS ITS OWN GO MODULE
//
// The whole claim of M5 is that the downloader is a PLUGIN, and the test for
// that is not a naming convention — it is a module boundary. A package inside
// the core tree cannot import a different module without the core's go.mod
// gaining a `require` and a `replace`, and both are visible in review. A
// directory with its own go.mod *inside* the core tree would be a nested module,
// which Go tolerates and which leaves the code inside `go build ./...` and
// greppable as core. So it lives at `plugins/p2pdownloader/`, outside.
//
// `TestP2PDownloaderIsNotImportedByCore` in internal/api enforces that, and
// `TestP2PDownloaderIsNotBundledByCore` enforces the half that greps cannot see:
// that the built core BINARY does not contain this code either. A core that
// shells out to a bundled downloader passes the import grep and is exactly the
// thing M5 exists to rule out — outside the import graph, inside the artifact.
//
// # WHY `interface: rpc` AND NOT `raw` OR `js`
//
// Stash loads plugins three ways (pkg/plugin/config.go:355):
//
//   - js   — goja, i.e. JavaScript. Cannot host a BitTorrent client.
//   - raw  — a binary that is started, runs to completion and exits. A
//     downloader needs a DHT and inbound connections, so it must stay UP
//     between requests; a process that exits after one fetch cannot.
//   - rpc  — a binary launched once and long-lived, over net/rpc/jsonrpc.
//
// rpc is the only one that fits, and the reason is a property of the protocol
// rather than a preference.
package main

import (
	"fmt"
	"os"

	"github.com/stashapp/stash-plugin-p2pdownloader/internal/rpc"
)

func main() {
	// One job, and it is the only thing this binary does: serve the plugin
	// interface until the host closes it. Everything else is in internal/ so it
	// is testable without a process boundary.
	if err := rpc.Serve(os.Stdin, os.Stdout); err != nil {
		// The host reads stderr and attributes it to the plugin, so the message
		// is written rather than logged through a package this binary does not
		// otherwise depend on.
		fmt.Fprintf(os.Stderr, "p2p-downloader: %v\n", err)
		os.Exit(1)
	}
}
