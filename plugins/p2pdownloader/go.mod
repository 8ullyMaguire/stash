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

go 1.24.0

require github.com/anacrolix/torrent v1.61.0

require (
	github.com/anacrolix/chansync v0.7.0 // indirect
	github.com/anacrolix/generics v0.1.1-0.20251125230353-15d98d46693b // indirect
	github.com/anacrolix/log v0.17.1-0.20251118025802-918f1157b7bb // indirect
	github.com/anacrolix/missinggo v1.3.0 // indirect
	github.com/anacrolix/missinggo/perf v1.0.0 // indirect
	github.com/anacrolix/missinggo/v2 v2.10.0 // indirect
	github.com/anacrolix/sync v0.5.5-0.20251119100342-d78dd1f686f1 // indirect
	github.com/edsrzf/mmap-go v1.1.0 // indirect
	github.com/go-llsqlite/adapter v0.0.0-20230927005056-7f5ce7f0c916 // indirect
	github.com/go-llsqlite/crawshaw v0.5.6-0.20250312230104-194977a03421 // indirect
	github.com/huandu/xstrings v1.3.2 // indirect
	github.com/klauspost/cpuid/v2 v2.2.3 // indirect
	github.com/mr-tron/base58 v1.2.0 // indirect
	github.com/multiformats/go-multihash v0.2.3 // indirect
	github.com/multiformats/go-varint v0.0.6 // indirect
	github.com/spaolacci/murmur3 v1.1.0 // indirect
	go.etcd.io/bbolt v1.3.6 // indirect
	golang.org/x/crypto v0.44.0 // indirect
	golang.org/x/exp v0.0.0-20251113190631-e25ba8c21ef6 // indirect
	golang.org/x/sys v0.38.0 // indirect
	lukechampine.com/blake3 v1.1.6 // indirect
)
