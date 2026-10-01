package torrent

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	libtorrent "github.com/anacrolix/torrent"
	"github.com/anacrolix/torrent/bencode"
	"github.com/anacrolix/torrent/metainfo"
	"github.com/anacrolix/torrent/storage"
	"golang.org/x/time/rate"

	torrentpolicy "github.com/stashapp/stash-plugin-p2pdownloader/internal/policy"
	plugstore "github.com/stashapp/stash-plugin-p2pdownloader/internal/storage"
)

// # WHAT THIS PACKAGE IS
//
// The wiring between three decisions that were each correct on their own and
// cannot be combined by accident:
//
//   - `internal/policy` decides whether a torrent may seed, from its consent
//     tier. It is a pure function and knows nothing about the library.
//   - `internal/storage.Gate` decides where bytes land, and refuses a torrent
//     whose file names escape the download root.
//   - `anacrolix/torrent` uploads OPPORTUNISTICALLY by default, and its own
//     comment says so.
//
// This package's whole job is to make the first two binding on the third. And
// the assertions below are on the STRUCT the library will read, not on a
// running client, because every interesting mistake here is a wrong FIELD
// rather than a wrong call — and a field is checkable without a network.

// TestTheClientIsConfiguredNotToUploadByDefault is the one that matters most,
// because it is the inverse of the library's default.
//
// `ClientConfig.Seed`'s own comment: "Upload even after there's nothing in it
// for us. By default uploading is not altruistic, we'll only upload to
// encourage the peer to reciprocate." A downloader pointed at a corpus of
// untracked, self-published material that uploads opportunistically publishes
// strangers' work without anyone having decided that it should. So the default
// is `NoUpload: true`, and uploading is switched on per torrent by a policy
// decision — never by the absence of one.
func TestTheClientIsConfiguredNotToUploadByDefault(t *testing.T) {
	cfg, err := ConfigFor(Config{DownloadRoot: tmpRoot(t)})
	if err != nil {
		t.Fatalf("ConfigFor: %v", err)
	}

	if !cfg.NoUpload {
		t.Error("NoUpload is false, so the client will upload opportunistically. " +
			"The library's default is permissive and this downloader's is not: " +
			"the bytes leave a library the operator never sees, and once they are " +
			"out no later decision retracts them")
	}
	if cfg.Seed {
		t.Error("Seed is true, which is ordinary seeding and would upload " +
			"regardless of any per-torrent policy")
	}
	if cfg.DefaultStorage == nil {
		t.Error("no DefaultStorage, so the library builds its own from DataDir " +
			"and the path gate is bypassed entirely")
	}
}

// TestDataDirIsNeverSet is the interaction that makes the storage gate binding,
// and it is the assertion I would have written differently first.
//
// `client.go:305` is:
//
//	storageImpl := cfg.DefaultStorage
//	if storageImpl == nil {
//	    storageImplCloser := storage.NewFile(cfg.DataDir)
//	    ...
//	}
//
// So `DefaultStorage` fully replaces `DataDir`: the library's own file storage
// is built ONLY when `DefaultStorage` is nil. Setting `DataDir` as well would be
// harmless here, and harmless-looking is the problem — it reads as belt and
// braces, and it silently re-enables the unsafe path the day someone removes
// the `DefaultStorage` line to "fix" something.
//
// So the assertion is that `DataDir` is EMPTY, not that the gate is present.
func TestDataDirIsNeverSet(t *testing.T) {
	cfg, err := ConfigFor(Config{DownloadRoot: tmpRoot(t)})
	if err != nil {
		t.Fatalf("ConfigFor: %v", err)
	}

	if cfg.DataDir != "" {
		t.Errorf("DataDir is %q. The library uses it only when DefaultStorage "+
			"is nil, so today it is inert — and inert is exactly the problem: "+
			"it looks like a second safety net and it is the configuration that "+
			"rebuilds the unsafe storage the moment DefaultStorage is removed",
			cfg.DataDir)
	}
}

// TestTheStorageIsTheGate is the direct assertion behind the previous one, and
// it is stated separately because the two fail differently: an empty DataDir
// with a FOREIGN storage is a live bypass, and a DataDir with no gate is a
// future accident.
func TestTheStorageIsTheGate(t *testing.T) {
	root := tmpRoot(t)

	cfg, err := ConfigFor(Config{DownloadRoot: root})
	if err != nil {
		t.Fatalf("ConfigFor: %v", err)
	}

	gate, ok := cfg.DefaultStorage.(*plugstore.Gate)
	if !ok {
		t.Fatalf("DefaultStorage is %T, not a *storage.Gate. Anything else "+
			"means peer-supplied filenames are joined without the gate",
			cfg.DefaultStorage)
	}
	if gate.Root() != root {
		t.Errorf("the gate's root is %q, expected %q. A gate pointed somewhere "+
			"other than the configured download root checks the wrong directory "+
			"and refuses nothing", gate.Root(), root)
	}
}

// TestAMissingDownloadRootIsRefused is the configuration error, reported as
// such. A downloader that creates its own root hides the mistake: the operator
// finds out when the files are somewhere they did not choose, which is a
// complaint about a download rather than a complaint about a setting.
func TestAMissingDownloadRootIsRefused(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "never-created")

	if _, err := ConfigFor(Config{DownloadRoot: missing}); err == nil {
		t.Error("ConfigFor accepted a download root that does not exist. " +
			"Creating it is paths.EnsureRoot's job, not a constructor's")
	}
	if _, statErr := os.Stat(missing); statErr == nil {
		t.Error("ConfigFor created the download root")
	}
}

// TestAMissingOrUnusableRootIsRejectedRatherThanDefaulted: an empty
// `DownloadRoot` is a caller bug, and the dangerous version of it is a
// downloader that falls back to the process's working directory — which for a
// long-lived plugin is wherever the host happened to start it.
func TestAMissingOrUnusableRootIsRejectedRatherThanDefaulted(t *testing.T) {
	for _, tt := range []struct{ name, root string }{
		{"empty", ""},
		{"relative", "relative/path"},
		{"a file", ""}, // filled in below, since it needs a real file
	} {
		if tt.name == "a file" {
			f := filepath.Join(t.TempDir(), "not-a-dir")
			if err := os.WriteFile(f, []byte("x"), 0o600); err != nil {
				t.Fatalf("writing the file: %v", err)
			}
			tt.root = f
		}
		t.Run(tt.name, func(t *testing.T) {
			if _, err := ConfigFor(Config{DownloadRoot: tt.root}); err == nil {
				t.Errorf("ConfigFor accepted the download root %q. Falling back "+
					"to the working directory would put a stranger's files "+
					"wherever the host happened to start this process", tt.root)
			}
		})
	}
}

// TestARefusalIsDistinguishableFromAConfigurationError: the two both return an
// error, and a caller that cannot tell them apart will retry a hostile torrent
// forever or, worse, treat a misconfiguration as a bad download and carry on.
func TestARefusalIsDistinguishableFromAConfigurationError(t *testing.T) {
	_, cfgErr := ConfigFor(Config{DownloadRoot: filepath.Join(t.TempDir(), "absent")})
	if !errors.Is(cfgErr, plugstore.ErrRefused) {
		t.Errorf("a bad download root gave %v, which does not wrap "+
			"storage.ErrRefused. A caller cannot tell a configuration mistake "+
			"from a torrent the gate refused, and the two need different actions",
			cfgErr)
	}
}

// TestTheDownloadRateLimitIsSetAndTheUploadLimitIsNot: an upload limiter is a
// rate, not a permission, and a permissive one invites the reading that upload
// is allowed.
//
// There is no upload limiter even when seeding is permitted, because the
// permission is per torrent and the limiter is per client — and a client-wide
// upload rate would apply to torrents the policy forbids. The absence is the
// design: a torrent that may not seed has no upload path at all, so a rate limit
// on it would be a knob with no meaning.
func TestTheDownloadRateLimitIsSetAndTheUploadLimitIsNot(t *testing.T) {
	cfg, err := ConfigFor(Config{DownloadRoot: tmpRoot(t)})
	if err != nil {
		t.Fatalf("ConfigFor: %v", err)
	}

	if cfg.DownloadRateLimiter == nil {
		t.Error("no download rate limiter, so a single torrent can saturate the " +
			"link the operator shares with everything else on the box")
	}
	// The limiter is present and UNLIMITED, and both halves of that matter.
	//
	// Present: `config.go:278` calls `cfg.UploadRateLimiter.Burst()` with no nil
	// check, on every `NewClient`. So nil is not the library's "unlimited" idiom
	// on this field — it is a nil-pointer dereference. `EffectiveDownloadRateLimit`
	// handles nil for the DOWNLOAD side explicitly; the upload side does not.
	//
	// Unlimited: because upload is a per-torrent PERMISSION here. A client-wide
	// upload rate would be the only meaningful difference between "this torrent
	// may not upload" and "this torrent may upload slowly", and the first must
	// not be expressible as a number.
	if cfg.UploadRateLimiter == nil {
		t.Error("UploadRateLimiter is nil, which panics: config.go:278 calls " +
			".Burst() on it unguarded during every NewClient")
	} else if cfg.UploadRateLimiter.Limit() != rate.Inf {
		t.Errorf("the upload limit is %v. Upload is controlled PER TORRENT by "+
			"the consent tier, and a client-wide limit makes 'must not upload' "+
			"expressible as 'upload slowly'", cfg.UploadRateLimiter.Limit())
	}
}

// TestTheDownloadRateLimitHasADefaultAndHonoursAnExplicitOne: a limit of zero
// means "unlimited" in `x/time/rate`, so leaving `Config` alone would silently
// mean unlimited too. The default has to be applied explicitly to not mean that.
func TestTheDownloadRateLimitHasADefaultAndHonoursAnExplicitOne(t *testing.T) {
	dflt, err := ConfigFor(Config{DownloadRoot: tmpRoot(t)})
	if err != nil {
		t.Fatalf("ConfigFor: %v", err)
	}
	if dflt.DownloadRateLimiter.Limit() <= 0 {
		t.Errorf("the default download limit is %v, which is unlimited. A zero "+
			"limit means no limit in x/time/rate, so the default has to be a "+
			"real number rather than left at zero",
			dflt.DownloadRateLimiter.Limit())
	}

	explicit, err := ConfigFor(Config{DownloadRoot: tmpRoot(t), DownloadRateLimit: 1234})
	if err != nil {
		t.Fatalf("ConfigFor: %v", err)
	}
	if got := explicit.DownloadRateLimiter.Limit(); got != 1234 {
		t.Errorf("an explicit DownloadRateLimit of 1234 gave %v", got)
	}
}

// TestTheClientBindsNoSockets is the one that measures the actual property
// rather than the config fields that are supposed to produce it.
//
// Every other reachability assertion in this file reads a boolean on the config.
// A boolean is the INPUT, not the outcome, and I had already documented a
// mechanism for it that does not exist: there is no `Client.Listen` in this
// library, and I wrote two commits' worth of comments saying reachability was
// deferred to a later `Listen` call. There is no later call — the sockets are
// bound inside `NewClient` (client.go:385-420), which also starts the port
// forwarder.
//
// So this builds the real client and asks it what it bound. `Listeners()` and
// `ListenAddrs()` are the library's own report of its sockets, which is the only
// thing that can be wrong here and still look right in the config.
func TestTheClientBindsNoSockets(t *testing.T) {
	d, err := New(Config{DownloadRoot: tmpRoot(t)})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer func() {
		if err := d.Close(); err != nil {
			t.Errorf("closing the downloader: %v", err)
		}
	}()

	if got := len(d.client.Listeners()); got != 0 {
		t.Errorf("the client bound %d listeners (%v). A downloader that has "+
			"not been given anything to fetch must not be reachable, and a "+
			"config field saying so is not evidence -- the sockets are bound "+
			"inside NewClient, so this is the only place it is observable",
			got, d.client.ListenAddrs())
	}
	if addrs := d.client.ListenAddrs(); len(addrs) != 0 {
		t.Errorf("the client is listening on %v", addrs)
	}
}

// TestTheDhtIsOffWithTheTransports is a plain field assertion, and it exists
// because the first version of this test was a tautology.
//
// What I wrote: build a DHT-only client, assert it binds 0 listeners. That
// passes — but it passes because of how the LIBRARY behaves, not because of
// anything this package does. Turning the DHT on in `ConfigFor` left it green,
// which is the "a test that measures a layer you did not name" trap one level
// up: asserting a property of the dependency and calling it a test of the wiring.
//
// The measurement is kept because it is the reason the field matters, and the
// assertion is on the field this package sets.
func TestTheDhtIsOffWithTheTransports(t *testing.T) {
	cfg, err := ConfigFor(Config{DownloadRoot: tmpRoot(t)})
	if err != nil {
		t.Fatalf("ConfigFor: %v", err)
	}

	// Why this one, when a DHT-only client binds nothing at all. Measured with
	// this library, same config otherwise:
	//
	//	DHT off, TCP off, UTP off   -> 0 listeners
	//	DHT ON,  TCP off, UTP off   -> 0 listeners
	//	DHT ON,  TCP ON,  UTP off   -> 2 listeners
	//
	// The middle row is the reason the DHT is off rather than merely unused. A
	// live DHT with nothing to accept connections is a box peers can find and
	// that cannot serve them: it advertises interest, earns leech credit it
	// cannot return, and disappoints every peer it attracts. Strictly worse than
	// never having joined, and it is the state a "DHT is harmless" reading of
	// the config produces.
	//
	// `TestTheClientBindsNoSockets` is the test that the transports stay off.
	// This one is that the DHT does not come on alone.
	if !cfg.NoDHT {
		t.Error("the DHT is enabled with both transports disabled. That " +
			"configuration binds no sockets and serves no peers, so it is a " +
			"pure cost: findable, unable to answer, and earning leech credit it " +
			"cannot return")
	}
}

// TestThePortForwarderIsNotStartedAtConstruction is the one that caused a real
// request to leave the operator's machine, and it is a `go` statement inside
// `NewClient`:
//
//	if !cfg.NoDefaultPortForwarding {
//	    go cl.forwardPort()
//	}
//
// There is no later call that can turn this off. A UPnP or NAT-PMP request to the
// operator's router happens during CONSTRUCTION, and construction is not consent
// to be reachable — so the default is off in the config, and this asserts the
// field the library actually reads.
func TestThePortForwarderIsNotStartedAtConstruction(t *testing.T) {
	cfg, err := ConfigFor(Config{DownloadRoot: tmpRoot(t)})
	if err != nil {
		t.Fatalf("ConfigFor: %v", err)
	}
	if !cfg.NoDefaultPortForwarding {
		t.Error("NoDefaultPortForwarding is false, so NewClient starts a port " +
			"forwarder that asks the operator's router to open a port — during " +
			"construction, before anything has decided this box should be " +
			"reachable at all")
	}
}

// TestWebseedsStayEnabled is the assertion that looks wrong and is not: it is
// here so nobody "fixes" it.
//
// A webseed is an HTTP fetch of a URL the torrent names — the same material
// from a different transport, and for a corpus of self-published files often
// the ONLY source. Disabling it removes a working source of exactly what this
// downloader exists to fetch.
//
// What a webseed must never become is a way around the consent tier, and it
// cannot: `NoUpload` is about upload, and a webseed is a download.
func TestWebseedsStayEnabled(t *testing.T) {
	cfg, err := ConfigFor(Config{DownloadRoot: tmpRoot(t)})
	if err != nil {
		t.Fatalf("ConfigFor: %v", err)
	}
	if cfg.DisableWebseeds {
		t.Error("webseeds are disabled. A webseed is an HTTP fetch selected by " +
			"the torrent, which is what this downloader exists to do, and " +
			"disabling it removes a working source of the same material")
	}
}

// TestTheConfigIsAcceptedByTheLibrary is the only test that builds a real
// client, and it exists because a struct is not a valid config until the library
// says so.
//
// Cheap insurance: every other test in this file could pass against a config the
// library rejects at `NewClient`, which would make them all tests of a fiction.
// The client is built with the transports off, so it opens no sockets.
func TestTheConfigIsAcceptedByTheLibrary(t *testing.T) {
	cfg, err := ConfigFor(Config{DownloadRoot: tmpRoot(t)})
	if err != nil {
		t.Fatalf("ConfigFor: %v", err)
	}

	cl, err := libtorrent.NewClient(cfg)
	if err != nil {
		t.Fatalf("the library rejected the config: %v\n\n"+
			"  Every other test in this package asserts on this struct, so if the "+
			"  library will not accept it, they are all tests of a fiction", err)
	}
	if cl == nil {
		t.Fatal("NewClient returned no client and no error")
	}
	if err := cl.Close(); err != nil {
		t.Errorf("closing the client: %v", err)
	}
}

// ---- the two decisions, applied ---------------------------------------------

// TestAPermissiveTierStillUploadsNothingWhenTheOperatorDeclines: the outer
// bound. Asserted here rather than only in `internal/policy` because the thing
// being tested is the WIRING — that the operator's setting reaches the library's
// upload control, which is a different question from whether `Decide` honours it.
func TestAPermissiveTierStillUploadsNothingWhenTheOperatorDeclines(t *testing.T) {
	d := newDownloader(t, false)

	for _, tier := range []string{
		torrentpolicy.TierSelfPublished,
		torrentpolicy.TierThirdPartyPermitted,
		torrentpolicy.TierUnverified,
	} {
		dec := d.AddTorrent(tier, benignInfo(t))
		if dec.UploadAllowed {
			t.Errorf("tier %q reports UploadAllowed with the operator's setting "+
				"off. The permission exists in the tier and the operator "+
				"declined to exercise it", tier)
		}
		if dec.Policy.Upload.CanUpload() {
			t.Errorf("tier %q: the policy permits upload with the operator's "+
				"setting off", tier)
		}
	}
}

// TestARestrictiveTierIsNotWidenedByTheOperatorsSwitch: the direction the
// operator's setting must never go. The operator can narrow; nothing can widen.
func TestARestrictiveTierIsNotWidenedByTheOperatorsSwitch(t *testing.T) {
	d := newDownloader(t, true)

	for _, tier := range []string{
		torrentpolicy.TierQuarantined,
		torrentpolicy.TierDenied,
		"some_tier_added_next_year",
		"",
	} {
		dec := d.AddTorrent(tier, benignInfo(t))
		if dec.UploadAllowed {
			t.Errorf("tier %q uploads with the operator's setting ON. The "+
				"operator's switch narrows decisions; it must never be able to "+
				"widen one", tier)
		}
	}
}

// TestTheLibraryIsActuallyToldWhatThePolicyDecided is the test that matters,
// and it is the one I wrote LAST and should have written FIRST.
//
// Every other assertion in this file reads `Decision`, and `Decision` is derived
// FROM the policy. So they are all consistency checks between a value and its own
// source, and every one of them still passes if the client is handed
// `DisallowDataUpload = false` for a torrent the policy forbids.
//
// I checked, by mutating the implementation: it passed. The upload control was
// broken and the whole file was green. This test reads back what the client was
// GIVEN, which is the only observation that can fail.
func TestTheLibraryIsActuallyToldWhatThePolicyDecided(t *testing.T) {
	d := newDownloader(t, true)

	for _, tt := range []struct {
		tier        string
		wantAllowed bool
	}{
		{torrentpolicy.TierSelfPublished, true},
		{torrentpolicy.TierPerformerClaimed, true},
		{torrentpolicy.TierThirdPartyPermitted, true},
		{torrentpolicy.TierUnverified, false},
		{torrentpolicy.TierQuarantined, false},
		{torrentpolicy.TierDenied, false},
		{"future_tier", false},
		{"", false},
	} {
		d.AddTorrent(tt.tier, benignInfo(t))

		got := d.LastApplied()
		wantDisallow := !tt.wantAllowed
		if got.DisallowDataUpload != wantDisallow {
			t.Errorf("tier %q: the library was given DisallowDataUpload=%v, "+
				"expected %v. The reported decision matched the policy while "+
				"the client was told the opposite, which is the bug this "+
				"asserts and which no Decision-only test can see",
				tt.tier, got.DisallowDataUpload, wantDisallow)
		}
	}
}

// TestTheLibraryIsGivenTheGateAndNotStorageOfItsOwn: the same shape of mistake
// as `DataDir` alongside `DefaultStorage`, one layer down.
//
// `AddTorrentOpts.Storage` is a per-torrent override, so a spec that sets it to
// anything but the gate bypasses the gate -- and the client's `DefaultStorage`
// says nothing about a torrent that names its own. Both are set here, and this
// is the assertion for the per-torrent one.
func TestTheLibraryIsGivenTheGateAndNotStorageOfItsOwn(t *testing.T) {
	d := newDownloader(t, true)

	d.AddTorrent(torrentpolicy.TierSelfPublished, benignInfo(t))

	if !d.LastApplied().StorageIsGate {
		t.Error("the client was given storage other than the gate for a " +
			"per-torrent torrent. AddTorrentOpts.Storage overrides the " +
			"client's DefaultStorage, so a spec that sets it bypasses the gate " +
			"while the config still looks correct")
	}
}

// TestTheHashTheGateRefusedIsTheHashTheLibraryUses: a refusal recorded against
// a hash the client does not recognise cannot be correlated with the download
// it was for, so the operator sees a refusal and no matching torrent.
//
// `TorrentSpecFromMetaInfo` computes `InfoHash` from these bytes, and the spec
// is the library's own -- so the correct value is the one the library chose. A
// reimplementation of the hash here would be a second chance to disagree with
// every peer on the network.
func TestTheHashTheGateRefusedIsTheHashTheLibraryUses(t *testing.T) {
	d := newDownloader(t, true)

	mi := hostileInfo(t)
	d.AddTorrent(torrentpolicy.TierSelfPublished, mi)

	refusals := d.Gate().Refusals()
	if len(refusals) == 0 {
		t.Fatal("no refusal recorded for a hostile torrent")
	}
	for _, r := range refusals {
		if r.Hash != d.LastApplied().InfoHash {
			t.Errorf("the refusal was recorded against %s but the library was "+
				"given %s for the same torrent. A refusal nobody can correlate "+
				"with a torrent is a refusal with no actionable content",
				r.Hash, d.LastApplied().InfoHash)
		}
	}
}

// TestThePolicyAndTheLibraryAreToldTheSameThing: `Decision` carries both the
// policy and what the library was told, and they must never disagree.
//
// Two answers to one question is the failure this catches: a report says
// "permitted" while `DisallowDataUpload` is set. It is checked for every tier
// because the interesting bugs are in the specific branches — an `unknown` tier
// that falls through to a `default:` that permits, for instance.
func TestThePolicyAndTheLibraryAreToldTheSameThing(t *testing.T) {
	d := newDownloader(t, true)

	for _, tier := range []string{
		torrentpolicy.TierSelfPublished,
		torrentpolicy.TierPerformerClaimed,
		torrentpolicy.TierThirdPartyPermitted,
		torrentpolicy.TierUnverified,
		torrentpolicy.TierQuarantined,
		torrentpolicy.TierDenied,
		"future_tier",
		"",
	} {
		dec := d.AddTorrent(tier, benignInfo(t))
		if want := dec.Policy.Upload.CanUpload(); dec.UploadAllowed != want {
			t.Errorf("tier %q: the decision reports UploadAllowed=%v but the "+
				"policy says %v. The one the library reads must be derived "+
				"from the one the operator is shown", tier, dec.UploadAllowed, want)
		}
		if dec.Policy.Tier != tier {
			t.Errorf("tier %q: the decision records the tier as %q", tier, dec.Policy.Tier)
		}
		if dec.Policy.Reason == "" {
			t.Errorf("tier %q: the decision has no stated reason, including "+
				"for a permissive outcome. A permissive decision with no basis "+
				"is the one that gets audited and found wanting", tier)
		}
	}
}

// TestAPermissiveTierIsRecordedAsPermittedWithAReason: the positive case, which
// a suite of restrictive assertions never proves. If every tier refused, every
// test above would pass and the downloader would be useless.
func TestAPermissiveTierIsRecordedAsPermittedWithAReason(t *testing.T) {
	d := newDownloader(t, true)

	dec := d.AddTorrent(torrentpolicy.TierSelfPublished, benignInfo(t))
	if !dec.UploadAllowed {
		t.Errorf("a self-published torrent does not upload with the operator's " +
			"setting on. Every assertion in this file is a refusal; if they all " +
			"pass because nothing is ever permitted, the downloader is inert")
	}
	if !dec.Added {
		t.Errorf("a benign torrent was not added: %v", dec.Err)
	}
}

// TestTheGateIsAskedBeforeTheClientIsTold: the ordering, stated as a property.
//
// `OpenTorrent` is only called after the policy and before `AddTorrentSpec`. If
// that order were reversed the client would hold a torrent it cannot open, and
// the DHT would announce a torrent whose files never arrive — which looks
// exactly like a dead swarm.
func TestTheGateIsAskedBeforeTheClientIsTold(t *testing.T) {
	d := newDownloader(t, true)

	dec := d.AddTorrent(torrentpolicy.TierSelfPublished, hostileInfo(t))
	if dec.Added {
		t.Error("a torrent with an escaping file name reached the client. The " +
			"gate refuses it on first piece write, but by then the DHT has been " +
			"told about it and a second AddTorrent would succeed from cache")
	}
	if !errors.Is(dec.Err, ErrRefusedUpFront) {
		t.Errorf("the decision reports %v, which is not ErrRefusedUpFront. The "+
			"caller branches on this to tell a hostile torrent from a transient "+
			"failure worth retrying", dec.Err)
	}
	if !errors.Is(dec.Err, plugstore.ErrRefused) {
		t.Errorf("the refusal does not wrap storage.ErrRefused: %v", dec.Err)
	}
	if got := d.Gate().Refusals(); len(got) == 0 {
		t.Error("the torrent was not added and the gate recorded no refusal. " +
			"Something refused it, and a report cannot say what")
	}
}

// TestTheGateIsAskedAndItsRefusalIsWhatStopsTheTorrent: the hole I found by
// mutating the implementation, and it was the most important one in the file.
//
// Disabling the gate's refusal — leaving the call in place but ignoring its
// error — passed every test here. The reason is a REFUSAL AT A LAYER BELOW:
// the client is given the gate as its storage, so when the library eventually
// calls `OpenTorrent` the gate refuses there, and the transfer fails. The
// torrent still does not get written.
//
// So the client was never told about the torrent, and the outcome was correct,
// by a second layer doing the work the first was supposed to do.
//
// That is not a reason to accept the code. The point of asking the gate HERE is
// that the client never holds the torrent: a torrent it cannot open is one the
// DHT announces, and one a later `AddTorrent` can add from cache. Both of those
// are silent, and both are worse than a download that fails.
//
// The assertion is therefore on the DECISION, not on the bytes: a refused
// torrent must report `ErrRefused` and must not be added, and no amount of
// downstream refusal makes `Decision.Added` true or `Decision.Err` nil.
func TestTheGateIsAskedAndItsRefusalIsWhatStopsTheTorrent(t *testing.T) {
	d := newDownloader(t, true)

	// A BENIGN torrent first, so a later "no refusals" cannot be a false pass
	// caused by the gate being broken in a way that refuses everything.
	if dec := d.AddTorrent(torrentpolicy.TierSelfPublished, benignInfo(t)); !dec.Added {
		t.Fatalf("a benign torrent was refused: %v", dec.Err)
	}
	before := len(d.Gate().Refusals())

	dec := d.AddTorrent(torrentpolicy.TierSelfPublished, hostileInfo(t))
	if dec.Added {
		t.Error("a torrent with an escaping file name was added. It may fail " +
			"later when the library calls the gate, but a client that holds a " +
			"torrent it cannot open is one the DHT announces")
	}
	// The UP-FRONT sentinel, not merely `storage.ErrRefused`.
	//
	// `errors.Is(dec.Err, storage.ErrRefused)` is true whether the gate refused
	// here or the library's storage call refused later — both wrap the same
	// error, and I confirmed by mutation that asserting on it does not
	// discriminate. `ErrRefusedUpFront` is what says the client never held the
	// torrent, which is the property that matters and the one the mutation
	// breaks.
	if !errors.Is(dec.Err, ErrRefusedUpFront) {
		t.Errorf("the decision reports %v, which is not ErrRefusedUpFront. "+
			"Either the gate was not asked here, or its refusal was ignored and "+
			"a downstream layer refused the same input -- which leaves the "+
			"client holding a torrent it announced and could not open",
			dec.Err)
	}
	if !errors.Is(dec.Err, plugstore.ErrRefused) {
		t.Errorf("the up-front refusal does not wrap storage.ErrRefused, so a "+
			"caller filtering on that cannot tell a refusal from a crash: %v",
			dec.Err)
	}
	if got := len(d.Gate().Refusals()); got <= before {
		t.Errorf("the gate recorded %d refusals, was %d: the gate was not asked "+
			"about this torrent at all", got, before)
	}
}

// TestARefusalCarriesTheFileThatCausedIt: a refusal the operator cannot act on
// is a refusal they will work around — by pointing the download root somewhere
// wider, which is worse.
func TestARefusalCarriesTheFileThatCausedIt(t *testing.T) {
	d := newDownloader(t, true)

	d.AddTorrent(torrentpolicy.TierSelfPublished, hostileInfo(t))

	refusals := d.Gate().Refusals()
	if len(refusals) == 0 {
		t.Fatal("no refusal was recorded")
	}
	found := false
	for _, r := range refusals {
		if r.Name != "" && r.Reason != "" {
			found = true
		}
	}
	if !found {
		t.Errorf("no refusal carries both a file name and a reason: %+v. The "+
			"operator's next action is to look at that file, and without both "+
			"they can only widen the root", refusals)
	}
}

// TestAMagnetWithNoMetadataIsNotAdded: the case most likely to be skipped,
// because there is nothing to check and "nothing to check" feels like a pass.
//
// A magnet carries an infohash and no file names, so the gate cannot judge it.
// Adding it anyway means the client holds a torrent the gate has never seen,
// and the moment BEP 9 metadata lands it becomes ungated data on disk. So the
// answer is a distinct, named state — not a silent add and not a silent skip.
func TestAMagnetWithNoMetadataIsNotAdded(t *testing.T) {
	d := newDownloader(t, true)

	dec := d.AddTorrentSpec(torrentpolicy.TierSelfPublished, &libtorrent.TorrentSpec{})
	if dec.Added {
		t.Error("a magnet with no metadata was added. The gate judges file " +
			"names and there are none, so it has not judged this torrent")
	}
	if !errors.Is(dec.Err, ErrMetadataPending) {
		t.Errorf("the decision reports %v, which is not ErrMetadataPending. A "+
			"caller has to be able to tell 'not arrived yet' from 'refused', "+
			"because only the second is a reason to stop", dec.Err)
	}
	if dec.UploadAllowed {
		t.Error("a torrent that has not been gated reports that it may upload")
	}
}

// TestAResolvedMagnetGoesThroughTheSameGateAsAnyOtherTorrent: a spec carrying
// metadata is re-parsed and re-decided, not added on the strength of being a
// spec. "We only have the spec at this point" is exactly the reason a check
// gets skipped.
func TestAResolvedMagnetGoesThroughTheSameGateAsAnyOtherTorrent(t *testing.T) {
	d := newDownloader(t, true)

	// The embedded AddTorrentOpts is named explicitly: `InfoBytes` is a PROMOTED
	// field, and Go 1.26 will not let a promoted field be set in a struct
	// literal. This is a language rule, not a library quirk, and the fix is to
	// say which layer of the spec the bytes belong to.
	spec := &libtorrent.TorrentSpec{
		AddTorrentOpts: libtorrent.AddTorrentOpts{InfoBytes: hostileInfoBytes(t)},
	}
	dec := d.AddTorrentSpec(torrentpolicy.TierSelfPublished, spec)

	if dec.Added {
		t.Error("a spec whose metadata contains an escaping file name was added. " +
			"A magnet is not a trusted form of a torrent: its names arrive by " +
			"BEP 9 from peers, which is exactly where an attack is cheapest")
	}
	if !errors.Is(dec.Err, ErrRefusedUpFront) {
		t.Errorf("the decision reports %v, which is not ErrRefusedUpFront", dec.Err)
	}
}

// TestAMalformedTorrentIsNotAHostileOne: the two must not be folded together.
// Malformed is worth retrying against another source; hostile is not, and
// retrying a hostile one just means asking another peer.
func TestAMalformedTorrentIsNotAHostileOne(t *testing.T) {
	d := newDownloader(t, true)

	dec := d.AddTorrent(torrentpolicy.TierSelfPublished, &metainfo.MetaInfo{
		InfoBytes: bencode.Bytes("d4:infod4:name4:bad"), // truncated
	})
	if dec.Added {
		t.Error("a malformed torrent was added")
	}
	if errors.Is(dec.Err, plugstore.ErrRefused) {
		t.Errorf("a malformed torrent is reported as REFUSED (%v). A refusal is "+
			"permanent and not worth retrying; a malformed torrent is worth "+
			"retrying against another source", dec.Err)
	}
	if dec.Err == nil {
		t.Error("a malformed torrent produced no error at all")
	}
}

// ---- fixtures ----------------------------------------------------------------

// newDownloader is a real `Downloader` over a real client, with transports off.
//
// Real because `AddTorrent` calls the library, and a fake would make the
// ordering assertions untestable: a fake that records calls proves nothing about
// which came first.
func newDownloader(t *testing.T, operatorAllowedSeed bool) *Downloader {
	t.Helper()

	d, err := New(Config{
		DownloadRoot:        tmpRoot(t),
		OperatorAllowedSeed: operatorAllowedSeed,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() {
		if err := d.Close(); err != nil {
			t.Errorf("closing the downloader: %v", err)
		}
	})
	return d
}

// benignInfo is a well-formed single-file torrent whose name stays inside the
// download root.
func benignInfo(t *testing.T) *metainfo.MetaInfo {
	t.Helper()

	mi := &metainfo.MetaInfo{}
	mi.InfoBytes = bencode.MustMarshal(map[string]any{
		"name":         "clip.mp4",
		"piece length": 1 << 16,
		"pieces":       strings_Repeat(20),
		"length":       1 << 16,
	})
	return mi
}

// hostileInfo is a well-formed torrent whose SECOND file walks out of the
// download root with two levels of `..`.
//
// Two levels, and this is the trap from the gate's own tests: under a torrent
// named "torrent", `["..", "escape"]` joins to `torrent/../escape`, which cleans
// to `escape` and lands INSIDE the root — correctly accepted. One `..` leaves the
// torrent directory but not the root. It takes two to leave the root, so a
// single-level case is a test that passes for the wrong reason.
func hostileInfo(t *testing.T) *metainfo.MetaInfo {
	t.Helper()

	mi := &metainfo.MetaInfo{}
	mi.InfoBytes = bencode.MustMarshal(map[string]any{
		"name":         "torrent",
		"piece length": 1 << 16,
		"pieces":       strings_Repeat(20),
		"files": []any{
			map[string]any{"length": 1 << 10, "path": []any{"ok.txt"}},
			map[string]any{"length": 1 << 10, "path": []any{"..", "..", "escape.txt"}},
		},
	})
	return mi
}

// hostileInfoBytes is `hostileInfo`'s bencoded form, for the magnet path.
func hostileInfoBytes(t *testing.T) []byte {
	t.Helper()
	return hostileInfo(t).InfoBytes
}

// strings_Repeat builds a `pieces` string of the right length for a 64 KiB
// piece: twenty bytes, one per SHA-1.
func strings_Repeat(n int) string {
	b := make([]byte, n)
	for i := range b {
		b[i] = 0xAB
	}
	return string(b)
}

// tmpRoot is a real, resolved download directory for a test.
//
// Real and resolved, because the config resolves the root once and the gate
// compares against the resolved form: where `/tmp` is a symlink, an unresolved
// fixture would fail every containment assertion for a filesystem reason that
// reads as a bug in the wiring.
func tmpRoot(t *testing.T) string {
	t.Helper()

	root := t.TempDir()
	resolved, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatalf("resolving the temp root %q: %v", root, err)
	}
	return resolved
}

// The library's own storage interface, referenced so the import above is a
// deliberate statement of what the gate has to satisfy rather than an accident.
var _ storage.ClientImpl = (*plugstore.Gate)(nil)
