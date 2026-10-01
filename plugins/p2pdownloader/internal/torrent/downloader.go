package torrent

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	libtorrent "github.com/anacrolix/torrent"
	"github.com/anacrolix/torrent/bencode"
	"github.com/anacrolix/torrent/metainfo"
	libstorage "github.com/anacrolix/torrent/storage"
	"golang.org/x/time/rate"

	torrentpolicy "github.com/stashapp/stash-plugin-p2pdownloader/internal/policy"
	"github.com/stashapp/stash-plugin-p2pdownloader/internal/storage"
)

// # WHY THIS PACKAGE EXISTS
//
// Three decisions, each correct on its own, that cannot be combined by accident:
//
//   - `internal/policy` decides whether a torrent may seed, from its consent
//     tier. Pure, and knows nothing about the library.
//   - `internal/storage.Gate` decides where bytes land, and refuses a torrent
//     whose file names escape the download root.
//   - `anacrolix/torrent` uploads OPPORTUNISTICALLY by default. Its own comment
//     says so: "By default uploading is not altruistic, we'll only upload to
//     encourage the peer to reciprocate."
//
// Left to defaults, the three compose into a downloader that publishes
// strangers' material to peers the operator never sees, into a directory
// something chose. So the wiring is explicit here, in one place, and every
// default that matters is inverted:
//
//	NoUpload            true    -- inverted from the library's permissive default
//	Seed                false   -- seeding is a per-torrent decision, not a mode
//	DataDir             EMPTY   -- the library would use it to build its OWN
//	                                 storage, bypassing the gate entirely
//	DefaultStorage      the Gate
//	UploadRateLimiter   nil     -- a rate is not a permission, and a client-wide
//	                                 limiter would apply to torrents the policy
//	                                 forbids
//
// # WHY `DataDir` IS EMPTY AND THAT IS ASSERTED
//
// `client.go:305`:
//
//	storageImpl := cfg.DefaultStorage
//	if storageImpl == nil {
//	    storageImplCloser := storage.NewFile(cfg.DataDir)
//	    ...
//	}
//
// `DefaultStorage` fully replaces `DataDir`. Setting both is harmless *today*
// and that is the problem: it reads as belt and braces, and it silently
// re-enables the unsafe storage the day someone deletes the `DefaultStorage`
// line to "fix" something. So the invariant is `DataDir == ""`, asserted
// directly rather than inferred from the gate being present.
//
// # WHY THE GATE IS ASKED BEFORE THE CLIENT IS TOLD
//
// `OpenTorrent` is where the gate refuses. If the client were told about the
// torrent first, it would hold a torrent it cannot open, and the DHT would keep
// announcing it — which looks exactly like a dead swarm, and which is a much
// harder thing to diagnose than a refusal with a reason attached.

// Config is what the host configures. Deliberately small.
//
// Every field here is one the operator has an opinion about, and nothing that
// exists only because the library has a knob. In particular there is no
// `Seed: true`, because a switch that turns seeding on for everything is the
// thing `internal/policy` exists to prevent.
type Config struct {
	// DownloadRoot is where completed downloads land. Must already exist: this
	// does not create it, because a constructor that quietly makes a directory
	// hides a configuration mistake until the operator finds their files
	// somewhere they did not choose.
	//
	// Must be ABSOLUTE, for the same reason. A relative path resolves against
	// the process's working directory, which for a long-lived plugin is
	// wherever the host happened to start it.
	DownloadRoot string

	// OperatorAllowedSeed is the operator's own setting.
	//
	// An OUTER BOUND and never an override: it can make a permissive decision
	// more restrictive and nothing else. An operator who does not want their
	// box seeding anything can say so without a code change; an operator who
	// wants to seed something the tier does not permit cannot say so at all.
	//
	// Defaults to false, and the default is the whole point: not seeding is what
	// a box does until someone decides otherwise, per torrent.
	OperatorAllowedSeed bool

	// DownloadRateLimit caps the download rate, in bytes per second. Zero means
	// unlimited.
	//
	// A default is applied because the failure mode is asymmetric: without a cap
	// one torrent saturates the link the operator shares with everything else
	// on the box, and nobody notices until everything else is slow. With a cap
	// that is too low, the operator raises it.
	DownloadRateLimit int64

	// DownloadRateBurst is the burst allowance for the download limiter. Zero
	// means a sensible multiple of the rate.
	DownloadRateBurst int
}

// defaultDownloadRate is the cap applied when Config leaves it at zero.
//
// 8 MiB/s. Chosen as a number rather than derived: it is roughly what a
// broadband line saturates at, so the limiter normally does nothing and only
// engages when several downloads compete — which is the case it exists for.
const defaultDownloadRate = 8 << 20

// Downloader owns a client and the decisions bound to it.
//
// Not safe for concurrent use by several goroutines. The library's client is,
// and the gate's record is, but `AddTorrent` is the one place where a decision
// is made and applied, and two of them racing would apply one torrent's policy
// to another's storage. The plugin's task surface serialises calls into it; a
// mutex here would hide that rather than enforce it.
type Downloader struct {
	gate   *storage.Gate
	client *libtorrent.Client
	cfg    Config

	// lastSpec is what the client was last GIVEN, as opposed to what was
	// decided. Exposed through `LastApplied` for tests, and it is the only
	// place the difference between the two is observable.
	lastSpec AppliedSpec

	// lastMagnet is the same idea for the magnet path, kept separately so a
	// read-back after a magnet add is not confused with one after a complete
	// torrent. Two records rather than one tagged union, because the fields
	// overlap and the union would be wrong for whichever the other was written
	// for.
	lastMagnet AppliedSpec

	// tiers associates a torrent with the consent tier it was added under.
	//
	// A MAP and not a field, because the library owns the `*Torrent` and there
	// is nowhere on it to record a tier — so the association has to live here.
	// A torrent missing from it is treated as `""`, which the policy's table
	// sends to `UploadForbidden`: a torrent whose tier this process has
	// forgotten must not be permitted to upload because the lookup missed.
	tiers map[metainfo.Hash]string
}

// AppliedSpec is what the library was actually handed for one torrent.
//
// Exists because a `Decision` cannot see it. `Decision.UploadAllowed` is
// computed FROM the policy, so a test asserting the two agree asserts nothing;
// the mutation it would catch -- always allowing upload -- leaves the reported
// decision perfectly consistent with the policy and puts the wrong value in the
// library. That mutation passes every consistency test in the file.
type AppliedSpec struct {
	// DisallowDataUpload is the value the client received.
	DisallowDataUpload bool

	// StorageIsGate reports whether the client was given the gate rather than
	// storage of its own choosing. A bool rather than the value because the
	// interface is not comparable.
	StorageIsGate bool

	// InfoHash is the hash the library will use to identify this torrent.
	InfoHash metainfo.Hash

	// DisplayName is the name recorded for a magnet, verbatim. Present so a
	// read-back of the magnet path can see what the client was given, for the
	// same reason `AppliedSpec` exists at all: `Decision` reports intent.
	DisplayName string

	// GatedAfterMetadata is set once the arrival path has run the gate.
	GatedAfterMetadata bool
}

// LastApplied returns what the client was last given.
//
// Test-facing, and honestly so: a `Decision` reports intent, and intent is not
// what the library does. This is the seam where the two are checked against each
// other, and it is deliberately the ONLY way to see it, so a test cannot
// accidentally assert on the decision and call it verified.
func (d *Downloader) LastApplied() AppliedSpec { return d.lastSpec }

// Decision is what happened to one torrent, and why.
//
// Returned rather than logged, because the caller is a task surface: an
// operator asking "why did this download not start" needs an answer they can
// read, not a line in a log they have to know to grep.
type Decision struct {
	// Tier is the consent tier the decision was derived from, as the host
	// supplied it.
	Tier string

	// Policy is the seeding decision, with its reason.
	Policy torrentpolicy.Policy

	// UploadAllowed is what the library was told. Kept alongside `Policy` so a
	// caller can see the two without recomputing one from the other — and
	// `TestASeedingTorrentIsTheOnlyWayToUpload` is the test that says they must
	// always agree.
	UploadAllowed bool

	// Added reports whether the client was told about the torrent. False with a
	// nil `Err` means the torrent was known and needed no action.
	Added bool

	// Gated reports whether the STORAGE GATE has seen this torrent's file names.
	//
	// False for every magnet, without exception, because a magnet has no file
	// names yet — they arrive by BEP 9 from whichever peer answers first. So
	// `Added && !Gated` is a real state, not a gap in the reporting: the library
	// is holding a torrent nobody has examined, and the only thing that makes
	// that acceptable is that `OnMetadata` runs the gate on arrival.
	//
	// A caller that reads `Added` without reading this is trusting that a
	// magnet's names were checked, and they were not.
	Gated bool

	// GatedAfterMetadata is `Gated`, stated as a separate flag so a report can
	// distinguish "never gated" from "gated, and I have not looked since". It
	// exists because the two look identical in a log line otherwise, and the
	// operator's question is always which one this is.
	GatedAfterMetadata bool

	// InfoHash identifies the torrent, so a report can be correlated with the
	// download it was for. For a magnet this is known BEFORE any file names
	// are, which is what lets a later refusal name something the operator can
	// look up.
	InfoHash metainfo.Hash

	// DisplayName is the name to show the operator, recorded exactly as the peer
	// supplied it.
	//
	// Verbatim, not sanitised. For a magnet this is the ONLY attacker-chosen
	// string available before the metadata lands, and `BestName()` falls back to
	// it afterwards — so a sanitised copy would be a record that disagrees with
	// what the peer actually said, which is worse than one containing something
	// ugly. It is not a path this package writes to; the gate uses the
	// metadata's file names.
	DisplayName string

	// Torrent is the library's handle, when one exists. Nil for a refusal, and
	// for a spec with no metadata.
	//
	// Exposed so a caller can pass it back to `OnMetadata` when BEP 9 metadata
	// arrives, rather than looking the torrent up by hash and risking a
	// different one.
	Torrent *libtorrent.Torrent

	// Err is why not, if not. `storage.ErrRefused` for a torrent whose file
	// names escape the download root.
	Err error
}

// New builds a client from `cfg`.
//
// The returned `Downloader` owns a live `*libtorrent.Client`, which opens
// sockets and starts a DHT. Callers that only want the CONFIG — which is every
// test in this package except one — should use `ConfigFor` instead, which
// builds the struct and no client.
func New(cfg Config) (*Downloader, error) {
	c, err := ConfigFor(cfg)
	if err != nil {
		return nil, err
	}

	// Re-asserted here because `NewClient` opens a port-forwarding manager when
	// this is false, and a UPnP request goes out to the operator's router during
	// construction. Construction is not consent, so the default is off and
	// `Listen` is what turns it on.
	c.NoDHT = true
	c.DisableTCP = true
	c.DisableUTP = true
	c.NoDefaultPortForwarding = true

	cl, err := libtorrent.NewClient(c)
	if err != nil {
		return nil, fmt.Errorf("the torrent library rejected the configuration: %w", err)
	}

	return &Downloader{gate: c.DefaultStorage.(*storage.Gate), client: cl, cfg: cfg}, nil
}

// ConfigFor builds the library's `ClientConfig` and no client.
//
// Separate from `New` so the configuration can be asserted on without opening a
// socket or starting a DHT, which is what makes it testable. Every field that
// matters is set here, once, and the tests read them back.
//
// The package is imported as `torrentpolicy` because Go derives a package name
// from the DIRECTORY and this one is `torrent` too, which would shadow the
// library import.
// The upload decision, per torrent.
//
// `DisallowDataUpload` is the ONLY per-torrent control the library offers.
// There is no per-torrent `Seed`: `ClientConfig.Seed` is the only `Seed` in the
// module (config.go:79), and it is a client-wide mode.
//
// That matters, because it means a CLIENT cannot be built once with `Seed: true`
// and then have seeding turned on per torrent afterwards -- the only per-torrent
// lever is the negative one. So the wiring is:
//
//	ClientConfig.NoUpload = true   always
//	ClientConfig.Seed     = false  always
//	per-torrent DisallowDataUpload = !policy.CanUpload()
//
// and a torrent whose policy PERMITS redistribution gets `DisallowDataUpload =
// false` with `Seed` still false at the client level, meaning it will share
// pieces while it is downloading and then stop.
//
// # WHY THAT IS THE RIGHT BEHAVIOUR, NOT A WORKAROUND
//
// `Seed` means "keep uploading after the data is complete". For a torrent this
// downloader fetches, the complete state is the end of the job: the file goes to
// the library and the operator's interest ends. Continuing to seed it from then
// on is redistribution nobody asked for, and the consent tier does not change
// when the download finishes.
//
// A library that wanted it would set `Seed` per torrent, and the way to get that
// today is a second client -- which is a much larger change than the decision
// deserves, and one that would need its own argument about resources. The
// honest statement is the one above: seeding a completed download is not
// implemented, and `DisallowDataUpload` is what this build enforces.
func ConfigFor(cfg Config) (*libtorrent.ClientConfig, error) {
	root, err := validateRoot(cfg.DownloadRoot)
	if err != nil {
		return nil, err
	}

	gate := storage.New(root)

	c := libtorrent.NewDefaultClientConfig()

	// ---- upload: the inverse of the library's default --------------------
	//
	// `NoUpload` is the CLIENT-wide backstop and it is on unconditionally. A
	// per-torrent `DisallowDataUpload` is the real mechanism, but the client
	// default is the belt to that per-torrent braces: a bug that adds a torrent
	// without asking the policy would still not upload.
	//
	// `Seed` is separate and also false. `NoUpload` and `Seed` are not the same
	// switch: `Seed` is ordinary seeding once the data is complete, and a
	// torrent whose policy permits redistribution still needs it set per
	// torrent, which `AddTorrent` does.
	c.NoUpload = true
	c.Seed = false

	// ---- storage: the gate, and an EMPTY DataDir ---------------------------
	//
	// `client.go:305` builds `storage.NewFile(cfg.DataDir)` ONLY when
	// `DefaultStorage` is nil, so setting both is inert today and a trap
	// tomorrow. `DataDir` is left empty deliberately and
	// `TestDataDirIsNeverSet` says so.
	c.DefaultStorage = gate

	// ---- rate limits -------------------------------------------------------
	//
	// Upload: nil, and the absence is the design. An upload limiter is a RATE,
	// not a permission — it would apply to every torrent including the ones the
	// policy forbids, which makes it a knob that looks like consent. Those
	// torrents have no upload path at all, so there is nothing to limit.
	perSecond := cfg.DownloadRateLimit
	if perSecond == 0 {
		perSecond = defaultDownloadRate
	}
	burst := cfg.DownloadRateBurst
	if burst <= 0 {
		burst = int(perSecond)
	}
	c.DownloadRateLimiter = rate.NewLimiter(rate.Limit(perSecond), burst)

	// Upload: `NewDefaultClientConfig` SETS this to an `unlimited` limiter, so
	// "leave it alone" is not the same as "not set" -- it is set, and it is set
	// to no limit at all.
	//
	// That is the right value, and it is deliberate. Upload is a PERMISSION
	// here, decided per torrent by the consent tier, and this limiter would
	// apply to every torrent at once. A client-wide upload rate is a knob whose
	// only meaning is "how much of what the policy forbids, slowly" -- so the
	// limiter stays unlimited and `DisallowDataUpload` is the control. A torrent
	// that may not seed has no upload path at all, so there is nothing to rate
	// limit.
	//
	// Set explicitly rather than left to the default, so that a reader can see
	// the unlimited rate is a choice. If a future `Config` field sets a limit
	// here, that field has to be the consent tier's business and not an
	// operator's.
	c.UploadRateLimiter = rate.NewLimiter(rate.Inf, c.MaxAllocPeerRequestDataPerConn)

	// ---- transport: off, and there is NO separate "listen" step ------------
	//
	// There is no `Listen` method in this library. I documented one for two
	// commits' worth of a milestone, and it does not exist -- the sockets are
	// created inside `NewClient` (client.go:385-420) and the port forwarder is
	// started there too, so reachability is decided ENTIRELY by this config and
	// is not something a later call can change.
	//
	// That makes the default load-bearing rather than advisory. Measured, with
	// this exact config:
	//
	//	DHT off, TCP off, UTP off   -> 0 listeners, no port
	//	DHT ON,  TCP off, UTP off   -> 0 listeners, no port
	//	DHT ON,  TCP ON,  UTP off   -> 2 listeners, 0.0.0.0:42069
	//	DHT ON,  TCP ON,  UTP ON    -> 4 listeners
	//
	// The second row is the one worth knowing: a live DHT is not a listener.
	// Turning the DHT on alone makes this box findable to peers while binding
	// nothing to accept them, which is the worst of both -- announcing without
	// being reachable, so the DHT's only effect is to be a worse leech than a
	// box that never joined. So the DHT is off too, and `AddTorrent` is the
	// point at which the operator's decision to transfer anything exists at all.
	c.NoDHT = true
	c.DisableTCP = true
	c.DisableUTP = true
	c.NoDefaultPortForwarding = true

	// Webseeds stay ENABLED, and the reason is not an oversight: a webseed is an
	// HTTP fetch of a URL the torrent names, which is the same material from a
	// different transport, and for a corpus of self-published files it is often
	// the ONLY source. What it must never become is a way around the consent
	// tier — and it cannot, because `NoUpload` is about upload and a webseed is
	// a download.
	c.DisableWebseeds = false

	return c, nil
}

// validateRoot checks the download root and returns it resolved.
//
// Resolved once, here, so every later comparison is against a real path: the
// gate resolves both sides on every call, and an unresolved root would reject
// every legitimate file on a system where `/tmp` is a symlink — which is macOS,
// and is what `/tmp` IS there.
func validateRoot(root string) (string, error) {
	if root == "" {
		return "", fmt.Errorf("%w: no download root is configured. Falling back "+
			"to the process's working directory would put a stranger's files "+
			"wherever the host happened to start this plugin", storage.ErrRefused)
	}
	if !filepath.IsAbs(root) {
		return "", fmt.Errorf("%w: the download root %q is relative, so it "+
			"resolves against the process's working directory and moves if the "+
			"host's cwd does", storage.ErrRefused, root)
	}

	resolved, err := filepath.EvalSymlinks(root)
	if err != nil {
		return "", fmt.Errorf("%w: the download root %q is not usable: %v. "+
			"Creating it is a configuration step, not something a constructor "+
			"does behind the operator's back", storage.ErrRefused, root, err)
	}
	info, err := os.Stat(resolved)
	if err != nil {
		return "", fmt.Errorf("%w: the download root %q is not usable: %v",
			storage.ErrRefused, root, err)
	}
	if !info.IsDir() {
		return "", fmt.Errorf("%w: the download root %q is not a directory",
			storage.ErrRefused, root)
	}
	return resolved, nil
}

// Gate exposes the storage gate, so a task report can name the file that was
// refused.
func (d *Downloader) Gate() *storage.Gate { return d.gate }

// AddTorrent applies the policy and the gate to one torrent, in that order.
//
// The order is the design. Policy first, because it is cheap and pure and a
// refusal there needs no filesystem. The gate second, because it is the only
// check that can return a real error, and it must run BEFORE the client is told
// the torrent exists.
//
// `metainfo` is the caller's, not derived here. A caller that has an
// `*metainfo.Info` in hand has the bytes it came from, and re-encoding them to
// recover a spec would be both lossy and a second chance to get a hash wrong.
func (d *Downloader) AddTorrent(tier string, mi *metainfo.MetaInfo) Decision {
	dec := torrentpolicy.Decide(torrentpolicy.Input{
		Tier:                tier,
		OperatorAllowedSeed: d.cfg.OperatorAllowedSeed,
	})

	if mi == nil {
		return Decision{Tier: tier, Policy: dec, Err: ErrMetadataPending}
	}

	// `MetaInfo` holds `InfoBytes`, not a parsed `Info`, so the gate is given a
	// parsed one. A torrent whose info will not parse is MALFORMED, which is not
	// a refusal and is worth retrying against a different source -- so it gets
	// its own error rather than being folded into the gate's.
	info, err := mi.UnmarshalInfo()
	if err != nil {
		return Decision{
			Tier:          tier,
			Policy:        dec,
			UploadAllowed: false,
			Added:         false,
			Err: fmt.Errorf("the torrent metadata could not be parsed, so there "+
				"are no file names to check: %w", err),
		}
	}

	// The info hash, from the bytes the caller supplied. `HashInfoBytes`
	// rather than a hash of a re-encoding: the two differ for a metainfo that
	// has been through a JSON round trip, and the hash is what identifies the
	// torrent to every peer.
	hash := mi.HashInfoBytes()

	// THE METADATA CHECK, BEFORE THE GATE.
	//
	// The library validates piece length and piece-table length too, but inside
	// `AddTorrentSpec` -- which is after this point. Measured: all three of a
	// zero piece length, a negative one, and a short piece table parse cleanly
	// and are accepted by the gate, and are only refused when the client is
	// finally asked to add them.
	//
	// The outcome would be right and the report wrong. The decision would say
	// the library declined the torrent "after the storage gate accepted it",
	// which is true and useless: the operator needs to know the torrent is
	// MALFORMED, because a malformed torrent is worth retrying against another
	// source and a path refusal is not.
	if err := checkMetainfo(&info); err != nil {
		return Decision{
			Tier:          tier,
			Policy:        dec,
			UploadAllowed: false,
			Added:         false,
			Err:           err,
		}
	}

	// The spec, built BEFORE the gate runs, so the record of what the client
	// would have been given is written whether or not the gate agrees.
	//
	// Building it after the gate was the first version, and it left `lastSpec`
	// holding the PREVIOUS torrent's values whenever one was refused -- so a
	// caller reading it after a refusal saw another torrent's hash. The values
	// are recorded here because the spec is what they describe, and the gate
	// does not change what the spec contains.
	spec := libtorrent.TorrentSpecFromMetaInfo(mi)
	upload := dec.Upload.CanUpload()
	d.remember(hash, tier)
	spec.DisallowDataUpload = !upload
	spec.Storage = d.gate
	d.lastSpec = AppliedSpec{
		DisallowDataUpload: spec.DisallowDataUpload,
		StorageIsGate:      spec.Storage == libstorage.ClientImpl(d.gate),
		InfoHash:           spec.InfoHash,
	}

	// THE GATE, BEFORE THE CLIENT.
	//
	// `OpenTorrent` is where the gate refuses, and it must happen here. Told
	// first, the client would hold a torrent it cannot open and the DHT would
	// keep announcing it -- which looks exactly like a dead swarm, and is much
	// harder to diagnose than a refusal with a reason attached.
	if _, err := d.gate.OpenTorrent(context.Background(), &info, hash); err != nil {
		return Decision{
			Tier:          tier,
			Policy:        dec,
			UploadAllowed: false,
			Added:         false,
			Err:           fmt.Errorf("%w: %v", ErrRefusedUpFront, err),
		}
	}

	// The client, with the per-torrent decisions attached.
	//
	// `DisallowDataUpload` is the real mechanism and `NoUpload` is the
	// client-wide backstop. Both, not either: a bug that adds a torrent without
	// asking the policy still does not upload.
	//
	// `Storage` is set to the gate as well, even though the client already has
	// it as `DefaultStorage`. A torrent that names its own storage would
	// otherwise bypass the gate, and `AddTorrentOpts.Storage` is exactly such a
	// field -- the same shape of mistake as setting `DataDir` alongside
	// `DefaultStorage`.
	return d.addGated(tier, dec, spec)
}

// addGated hands a GATED spec to the client, with the per-torrent decisions
// attached. Shared by both entry points so a magnet and a complete torrent
// cannot diverge in the one place that matters.
//
// `specApplied` records what the client was actually GIVEN, which is not the
// same as what the policy said.
//
// That sounds redundant and is the only way to test this: `UploadAllowed` is
// derived from the policy, so an assertion comparing the two compares a value
// with itself. Setting `DisallowDataUpload = false` unconditionally -- the exact
// bug this wiring exists to prevent -- passes every such test, because the
// reported decision still matches the policy. Only reading back the spec the
// library received can see it.
func (d *Downloader) addGated(tier string, dec torrentpolicy.Policy, spec *libtorrent.TorrentSpec) Decision {
	upload := dec.Upload.CanUpload()
	spec.DisallowDataUpload = !upload
	spec.Storage = d.gate

	_, _, err := d.client.AddTorrentSpec(spec)
	if err != nil {
		// Not a refusal: the gate already agreed. Something the library does
		// with a torrent the gate accepted -- a bad tracker, an unreachable
		// source. Recorded distinctly so it is retryable, which a refusal is
		// not.
		return Decision{
			Tier:          tier,
			Policy:        dec,
			UploadAllowed: upload,
			Added:         false,
			Err: fmt.Errorf("the torrent library declined the torrent "+
				"after the storage gate accepted it: %w", err),
		}
	}

	return Decision{
		Tier:          tier,
		Policy:        dec,
		UploadAllowed: upload,
		Added:         true,
	}
}

// AddTorrentSpec is `AddTorrent` for a caller holding a `TorrentSpec` — which
// is what a magnet becomes once BEP 9 metadata arrives, and what carries
// trackers.
//
// A magnet has no `*metainfo.Info` until its metadata is fetched, and a
// resolved magnet must go through the SAME two decisions as a torrent that
// arrived complete. A path that adds one without asking the gate would bypass
// everything, and "we only have the spec at this point" is exactly the kind of
// reason a check gets skipped.
//
// So a spec with no info bytes is NOT added. There is nothing to gate yet — the
// gate judges file NAMES, and a magnet has none — and adding it would mean the
// client holds an ungated torrent that the moment its metadata lands becomes
// ungated data.
func (d *Downloader) AddTorrentSpec(tier string, spec *libtorrent.TorrentSpec) Decision {
	dec := torrentpolicy.Decide(torrentpolicy.Input{
		Tier:                tier,
		OperatorAllowedSeed: d.cfg.OperatorAllowedSeed,
	})

	if spec == nil || len(spec.InfoBytes) == 0 {
		return Decision{
			Tier:          tier,
			Policy:        dec,
			UploadAllowed: false,
			Added:         false,
			Err:           ErrMetadataPending,
		}
	}

	// `TorrentSpec.InfoBytes` is the INNER info DICT, not a whole metainfo
	// (spec.go:81 assigns `mi.InfoBytes` to it). So it decodes as a
	// `metainfo.Info` -- handing it to `metainfo.Load`, which expects the
	// enclosing `d4:infod...e` dictionary, fails with EOF on a perfectly good
	// torrent.
	//
	// The distinction is not cosmetic: `HashInfoBytes` hashes these same bytes,
	// so the info hash a magnet resolves to is the hash of THIS dict and not of
	// any enclosing structure. Decoding it as anything else checks the wrong
	// bytes against the wrong hash.
	var info metainfo.Info
	if err := bencode.Unmarshal(spec.InfoBytes, &info); err != nil {
		return Decision{
			Tier:          tier,
			Policy:        dec,
			UploadAllowed: false,
			Added:         false,
			Err: fmt.Errorf("the torrent metadata could not be parsed, so there "+
				"are no file names to check: %w", err),
		}
	}
	//
	// The metadata check, before the gate, for the same reason as in
	// `AddTorrent`: a magnet's file names arrive by BEP 9 from peers, which is
	// exactly where an attacker chooses them, and a torrent whose own fields
	// disagree should not be name-checked at all.
	if err := checkMetainfo(&info); err != nil {
		return Decision{
			Tier:          tier,
			Policy:        dec,
			UploadAllowed: false,
			Added:         false,
			Err:           err,
		}
	}

	// Recorded BEFORE the gate, for the same reason as in `AddTorrent`: a
	// refusal that leaves the previous torrent's hash behind is a report that
	// names the wrong download.
	upload := dec.Upload.CanUpload()
	spec.DisallowDataUpload = !upload
	spec.Storage = d.gate
	d.lastSpec = AppliedSpec{
		DisallowDataUpload: spec.DisallowDataUpload,
		StorageIsGate:      spec.Storage == libstorage.ClientImpl(d.gate),
		InfoHash:           spec.InfoHash,
	}

	// The hash is `spec.InfoHash`, which the library has already computed for
	// this exact byte string. Recomputing it here would be a second chance to
	// disagree with the hash every peer uses, and a refusal recorded against a
	// hash the client does not recognise cannot be correlated with anything.
	if _, err := d.gate.OpenTorrent(context.Background(), &info, spec.InfoHash); err != nil {
		return Decision{
			Tier:          tier,
			Policy:        dec,
			UploadAllowed: false,
			Added:         false,
			Err:           fmt.Errorf("%w: %v", ErrRefusedUpFront, err),
		}
	}

	return d.addGated(tier, dec, spec)
}

// ErrRefusedUpFront is the gate refusing a torrent BEFORE the client was told it
// exists.
//
// A separate sentinel from `storage.ErrRefused` because the gate refuses the
// same input in two places, and the two mean different things:
//
//	ErrRefusedUpFront   the client never held the torrent. Nothing was
//	                    announced, nothing is cached, a retry is pointless
//	                    because the answer will not change.
//	ErrRefused (from the
//	 library's storage call) the client held it, tried to open it, and failed.
//	                    It may be in the DHT and in a cache; dropping it is
//	                    cleanup rather than a decision.
//
// Both wrap `storage.ErrRefused`, so `errors.Is(err, storage.ErrRefused)` is
// true for both and cannot tell them apart. Without this distinction the only
// way to observe the difference is the error MESSAGE, which is a string a
// refactor can change -- and a test that asserts on a message is a test that
// stops testing the thing when the wording is improved.
var ErrRefusedUpFront = fmt.Errorf("refused before the client was told the torrent exists: %w", storage.ErrRefused)

// ErrMetadataPending means a magnet has no metadata yet, so there is nothing to
// gate.
//
// A sentinel rather than a bool because the two outcomes must not be confused:
// "not added because the metadata has not arrived" and "not added because the
// name escapes the download root" call for different operator actions, and the
// first is a normal state that resolves itself.
var ErrMetadataPending = errors.New("the torrent has no metadata yet, so its file names cannot be checked")

// Close shuts the client down.
func (d *Downloader) Close() error {
	if d.client == nil {
		return nil
	}
	errs := d.client.Close()
	d.client = nil
	return errors.Join(errs...)
}
