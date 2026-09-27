// The P2P downloader plugin's own module.
//
// # WHY THIS FILE EXISTS AT ALL
//
// It is the ENFORCEMENT of M5's central claim, not a packaging convenience. A
// package inside the core tree cannot import a different module without the
// core's go.mod gaining a `require` and a `replace` — and both are visible in
// review. So the downloader having a go.mod of its own, at a path outside the
// core module, is what makes "the core does not know this exists" a fact rather
// than a promise.
//
// The module path is deliberately NOT under github.com/stashapp/stash/. A
// nested path would still be a separate module, but it would read as part of the
// project, and the test that checks the boundary also checks the path — a
// downloader whose module claims to be core is one directory rename away from
// being imported by it.
//
// # NO DEPENDENCIES, AND THAT IS NOT AN ACCIDENT
//
// `require` is empty. The transfer protocols (anacrolix/torrent, and whatever
// ed2k turns out to need) arrive in step 5.1, AFTER anacrolix/torrent has been
// evaluated — and the plan is explicit that the evaluation comes before the
// dependency, because the whole question at 5.1 is whether that library is
// usable at all.
//
// So this module depends on nothing but the standard library: net/rpc,
// net/rpc/jsonrpc, encoding/json. That is also what makes the seam tests
// meaningful — they can prove the core binary does not contain this code
// precisely because there is no version of it that the core could have linked
// against.
//
// The standard library is the only dependency, so a `go build` here cannot reach
// the network. That is worth preserving: a build that can fail because a proxy
// is down is a build whose failure says nothing about the code.
module github.com/stashapp/stash-plugin-p2pdownloader

go 1.23
