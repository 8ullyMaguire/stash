#!/usr/bin/env python3
"""Mutation harness for internal/torrent.

THREE files are mutated, because the package's invariants live in three: the
WIRING is in `downloader.go`, the VALIDATION in `metainfo_check.go`, and the
BEP 9 ARRIVAL path in `magnet.go`. A harness that walks one file reports the
others as untested, which is the scope error this repo keeps making in one form or
another -- and did make here: the first version of this harness walked two files
and reported the magnet path as covered by tests that never touched it.

Verdicts, and the two that cost the most:

    killed    the named test failed
    covered   no test noticed AND the whole suite still passed with the mutation
              applied -- another layer refuses the same input. A verified claim,
              not a pass.
    survived  the whole suite FAILED and the named test did not. A HOLE.
    SKIP      the pattern is not in the file, or the mutation does not compile.
              A build failure is NOT a kill: it proves nothing.
    STALE     the pattern no longer matches, so the row would test nothing.
              Reported BEFORE the run rather than discovered in the output.

Every `run` has a TIMEOUT: a mutation can deadlock the client, and a harness that
hangs is a harness that gets killed before it reports.

A SURVIVING MUTATION IS NOT AUTOMATICALLY A HOLE. It is one only if the code is
right and the test cannot see it. The v1/v2 check was the counter-example: the
mutation that disabled it survived because the code was WRONG and removing the
check made it right. Read the diff before going to the source.
"""

import subprocess, os, sys

D = "/home/alvaro/code-local/go/stash/plugins/p2pdownloader"
ENV = {**os.environ, "GOFLAGS": "-mod=mod"}
PKG = "./internal/torrent/"
TIMEOUT = 300

TARGET = "internal/torrent/downloader.go"
META = "internal/torrent/metainfo_check.go"
MAG = "internal/torrent/magnet.go"
TARGETS = (TARGET, META, MAG)

# (label, target, old, new, the test that should notice)
#
# Every `old` below was verified against the real source when the row was added.
# A pattern that no longer matches tests nothing, and reporting that as a kill or
# a hole is worse than not having the row -- so the harness says STALE and stops.
MUTATIONS = [
    ('metainfo: zero piece length accepted',
     META,
     '\tcase info.PieceLength == 0:',
     '\tcase false:',
     'Malformed'),
    ('metainfo: negative piece length accepted',
     META,
     '\tcase info.PieceLength < 0:',
     '\tcase false:',
     'Malformed'),
    ('metainfo: piece-length ceiling removed',
     META,
     '\tcase info.PieceLength > maxPieceLength:',
     '\tcase false:',
     'Ceilings'),
    ('metainfo: piece table not a multiple of 20',
     META,
     '\tif len(info.Pieces)%20 != 0 {',
     '\tif false {',
     'Named'),
    ('metainfo: empty piece table accepted',
     META,
     '\tif len(info.Pieces) == 0 {',
     '\tif false {',
     'Named'),
    ('metainfo: missing name accepted',
     META,
     '\tif info.BestName() == "" {',
     '\tif false {',
     'Named'),
    ('metainfo: total-length ceiling removed',
     META,
     '\tif total > MaxTorrentBytes {',
     '\tif false {',
     'Named'),
    ('metainfo: piece coverage not checked',
     META,
     '\t\tif have := int64(len(info.Pieces) / 20); have < needed {',
     '\t\tif have := int64(len(info.Pieces) / 20); have < 0 {',
     'Partial'),
    ('metainfo: negative total accepted',
     META,
     '\tif total < 0 {',
     '\tif false {',
     'Named'),
    ('metainfo: unknown meta version accepted',
     META,
     '\tcase 0, 1, 2:',
     '\tcase 0, 1, 2, 3:',
     'Named'),
    ('metainfo: the check removed from AddTorrent',
     TARGET,
     '\tif err := checkMetainfo(&info); err != nil {\n\t\treturn Decision{',
     '\tif err := error(nil); err != nil {\n\t\treturn Decision{',
     'BeforeTheGate'),
    ('metainfo: a malformed torrent reported as a path refusal',
     TARGET,
     '\t\t\tErr:           err,\n\t\t}\n\t}\n\n\t// The spec, built BEFORE',
     '\t\t\tErr:           fmt.Errorf("%w: %v", storage.ErrRefused, err),\n\t\t}\n\t}\n\n\t// The spec, built BEFORE',
     'NotAPathRefusal'),
    ('upload: always allow (inverted, so it still compiles)',
     TARGET,
     '\tspec.DisallowDataUpload = !upload',
     '\tspec.DisallowDataUpload = !true && upload',
     'ActuallyTold'),
    ("upload: operator's setting ignored",
     TARGET,
     'OperatorAllowedSeed: d.cfg.OperatorAllowedSeed,\n\t})',
     'OperatorAllowedSeed: true,\n\t})',
     'OperatorDeclines'),
    ("upload: client's NoUpload backstop off",
     TARGET,
     '\tc.NoUpload = true\n\tc.Seed = false',
     '\tc.NoUpload = false\n\tc.Seed = false',
     'NotToUploadByDefault'),
    ('upload: client-wide upload rate limit applied',
     TARGET,
     'c.UploadRateLimiter = rate.NewLimiter(rate.Inf, c.MaxAllocPeerRequestDataPerConn)',
     'c.UploadRateLimiter = rate.NewLimiter(rate.Limit(1024), 1024)',
     'UploadLimitIsNot'),
    ('gate: per-torrent storage bypasses the gate',
     TARGET,
     '\tspec.Storage = d.gate',
     '\tspec.Storage = nil',
     'GivenTheGate'),
    ('gate: refusal not distinguished from a later failure',
     TARGET,
     'Err:           fmt.Errorf("%w: %v", ErrRefusedUpFront, err),',
     'Err:           fmt.Errorf("refused: %v", err),',
     'RefusalIsWhatStops'),
    ('gate: DataDir set alongside DefaultStorage',
     TARGET,
     '\tc.DefaultStorage = gate',
     '\tc.DefaultStorage = gate\n\tc.DataDir = root',
     'DataDirIsNeverSet'),
    ('root: empty accepted',
     TARGET,
     '\tif root == "" {',
     '\tif false {',
     'RejectedRatherThan'),
    ('root: relative accepted',
     TARGET,
     '\tif !filepath.IsAbs(root) {',
     '\tif false {',
     'RejectedRatherThan'),
    ('root: not-a-directory accepted',
     TARGET,
     '\tif !info.IsDir() {',
     '\tif info.Mode()&os.ModeDir == 0 && !info.IsDir() {',
     'RejectedRatherThan'),
    ('root: missing directory accepted',
     TARGET,
     '\tresolved, err := filepath.EvalSymlinks(root)\n\tif err != nil {',
     '\tresolved := root\n\tvar err error\n\tif false {',
     'MissingDownloadRoot'),
    ('rate: download limiter removed',
     TARGET,
     '\tc.DownloadRateLimiter = rate.NewLimiter(rate.Limit(perSecond), burst)',
     '\t_, _ = perSecond, burst',
     'UploadLimitIsNot'),
    ('rate: default of zero means unlimited',
     TARGET,
     '\t\tperSecond = defaultDownloadRate',
     '\t\tperSecond = 0',
     'HasADefault'),
    ('network: port forwarding on',
     TARGET,
     '\tc.NoDHT = true\n\tc.DisableTCP = true\n\tc.DisableUTP = true\n\tc.NoDefaultPortForwarding = true\n\n\t// Webseeds',
     '\tc.NoDHT = true\n\tc.DisableTCP = true\n\tc.DisableUTP = true\n\tc.NoDefaultPortForwarding = false\n\n\t// Webseeds',
     'PortForwarder'),
    ('network: DHT on with the transports',
     TARGET,
     '\tc.NoDHT = true\n\tc.DisableTCP = true\n\tc.DisableUTP = true\n\tc.NoDefaultPortForwarding = true\n\n\t// Webseeds',
     '\tc.NoDHT = false\n\tc.DisableTCP = true\n\tc.DisableUTP = true\n\tc.NoDefaultPortForwarding = true\n\n\t// Webseeds',
     'DhtIsOff'),
    ('network: TCP on (binds sockets at NewClient)',
     TARGET,
     '\tc.NoDHT = true\n\tc.DisableTCP = true\n\tc.DisableUTP = true\n\tc.NoDefaultPortForwarding = true\n\n\t// Webseeds',
     '\tc.NoDHT = true\n\tc.DisableTCP = false\n\tc.DisableUTP = true\n\tc.NoDefaultPortForwarding = true\n\n\t// Webseeds',
     'BindsNoSockets'),
    ('network: uTP on (binds sockets at NewClient)',
     TARGET,
     '\tc.NoDHT = true\n\tc.DisableTCP = true\n\tc.DisableUTP = true\n\tc.NoDefaultPortForwarding = true\n\n\t// Webseeds',
     '\tc.NoDHT = true\n\tc.DisableTCP = true\n\tc.DisableUTP = false\n\tc.NoDefaultPortForwarding = true\n\n\t// Webseeds',
     'BindsNoSockets'),
    ('network: webseeds disabled',
     TARGET,
     '\tc.DisableWebseeds = false',
     '\tc.DisableWebseeds = true',
     'WebseedsStayEnabled'),
    ('magnet: nameless spec added ungated',
     TARGET,
     '\tif spec == nil || len(spec.InfoBytes) == 0 {',
     '\tif false {',
     'MagnetWithNoMetadata'),
    ('record: the recorded info hash is the zero hash',
     TARGET,
     '\t\tInfoHash:           spec.InfoHash,',
     '\t\tInfoHash:           metainfo.Hash{},',
     'HashTheGateRefused'),
    ('magnet: parse error swallowed',
     MAG,
     'metainfo.ParseMagnetUri(uri)\n\tif err != nil {',
     'metainfo.ParseMagnetUri(uri)\n\tif false {',
     'NoInfoHash'),
    ('magnet: zero infohash accepted',
     MAG,
     '\tif m.InfoHash.IsZero() {',
     '\tif false {',
     'NoInfoHash'),
    ('magnet: per-torrent upload control dropped',
     MAG,
     '\tspec.DisallowDataUpload = !upload\n',
     '\tspec.DisallowDataUpload = false\n',
     'Magnet'),
    ('magnet: gate not attached as storage',
     MAG,
     '\tspec.Storage = d.gate\n\tspec.ChunkSize = 0',
     '\tspec.ChunkSize = 0',
     'Magnet'),
    ('magnet: tier not remembered',
     MAG,
     '\td.remember(m.InfoHash, tier)\n',
     '',
     'TierIsRemembered'),
    ('magnet: lastMagnet recomputes the upload flag',
     MAG,
     '\t\tDisallowDataUpload: spec.DisallowDataUpload,',
     '\t\tDisallowDataUpload: !upload,',
     'Magnet'),
    ('magnet: the spec-builder error is ignored',
     MAG,
     '\tspec, err := libtorrent.TorrentSpecFromMagnetUri(uri)\n\tif err != nil {',
     '\tspec, _ := libtorrent.TorrentSpecFromMagnetUri(uri)\n\tif false {',
     'NoInfoHash|Magnet'),
    ('arrival: gate never runs',
     MAG,
     '\tif _, err := d.gate.OpenTorrent(context.Background(), &info, hash); err != nil {',
     '\tif _, err := d.gate.OpenTorrent(context.Background(), &info, hash); false && err != nil {',
     'EscapingName'),
    ('arrival: gate-refusal drop removed',
     MAG,
     '\tif _, err := d.gate.OpenTorrent(context.Background(), &info, hash); err != nil {\n\t\td.drop(tr)',
     '\tif _, err := d.gate.OpenTorrent(context.Background(), &info, hash); err != nil {\n\t\t_ = tr',
     'EscapingName'),
    ('arrival: metainfo-refusal drop removed',
     MAG,
     '\tif err := checkMetainfo(&info); err != nil {\n\t\td.drop(tr)',
     '\tif err := checkMetainfo(&info); err != nil {\n\t\t_ = tr',
     'Malformed'),
    ('arrival: metainfo check skipped',
     MAG,
     '\tif err := checkMetainfo(&info); err != nil {\n\t\td.drop(tr)',
     '\tif err := error(nil); err != nil {\n\t\td.drop(tr)',
     'Malformed|Magnet'),
    ('arrival: policy hardcoded instead of the recorded tier',
     MAG,
     'Tier:                d.tierOf(tr),\n\t\tOperatorAllowedSeed: d.cfg.OperatorAllowedSeed,\n\t})\n\n\tif tr == nil {',
     'Tier:                "",\n\t\tOperatorAllowedSeed: true,\n\t})\n\n\tif tr == nil {',
     'Magnet'),
    ('arrival: DisallowDataUpload not re-applied',
     MAG,
     '\tif !upload {\n\t\ttr.DisallowDataUpload()\n\t}',
     '\tif false {\n\t\ttr.DisallowDataUpload()\n\t}',
     'Magnet'),
]

# Every form the Go compiler emits for a probe that does not compile. The list
# has to be COMPLETE: a build error this misses is scored `survived`, which
# reports the probe's own defect as a hole in the tests -- the exact inversion
# the SKIP verdict exists to prevent. "redeclared" was missing when a magnet row
# produced `err redeclared in this block`, and the harness confidently called it
# a hole.
BUILD_ERRORS = ("build failed", "cannot use", "undefined:", "declared and not used",
                "syntax error", "not enough arguments", "too many arguments",
                "assignment mismatch", "redeclared", "no new variables",
                "declared and not", "imported and not used", "missing return",
                "typecheck", "all declarations of", "shadows declaration")


def is_build_error(out):
    return any(e in out for e in BUILD_ERRORS)


def run(tests):
    cmd = f"go test {PKG} -count=1 -timeout={TIMEOUT}s " + " ".join(
        f"-run {t}" for t in tests)
    try:
        r = subprocess.run(cmd, shell=True, cwd=D, capture_output=True, text=True,
                           env=ENV, timeout=TIMEOUT + 90)
        return r.stdout + r.stderr
    except subprocess.TimeoutExpired:
        return "TIMEOUT: the mutation deadlocked the client"


def main():
    # Snapshot EVERY target byte for byte, and restore from the snapshot.
    #
    # `git checkout` cannot restore an untracked file and the plugin is work in
    # progress, so a git-based restore leaves a mutated source behind and reports
    # a clean tree while doing it. That already happened once here: mutate_seam.py
    # restored with git checkout and put `interface: raw` in a manifest and core's
    # module path in the plugin's go.mod, which made the plugin unbuildable and
    # read as a plugin bug for twenty minutes.
    srcs = {t: open(os.path.join(D, t)).read() for t in TARGETS}
    every = [l for l in subprocess.run(
        f"go test {PKG} -list '.*' 2>/dev/null", shell=True, cwd=D,
        capture_output=True, text=True, env=ENV).stdout.split("\n")
        if l.startswith("Test")]
    print(f"{len(every)} tests in the package; {len(MUTATIONS)} mutations\n")

    gone = [l for l, t, o, _, _ in MUTATIONS if o not in srcs[t]]
    for label in gone:
        print(f"  STALE    {label}\n           the pattern is no longer in the file")

    killed = covered = survived = skipped = 0
    try:
        for label, target, old, new, test in MUTATIONS:
            src = srcs[target]
            if old not in src:
                print(f"  SKIP     {label}\n           pattern not found in {target}")
                skipped += 1
                continue
            open(os.path.join(D, target), "w").write(src.replace(old, new, 1))

            probe = run([test])
            if "TIMEOUT" in probe:
                verdict, detail = "SURVIVED", "the named test TIMED OUT (deadlock)"
            elif is_build_error(probe):
                verdict, detail = "SKIP", ("the mutation does not compile -- a bad "
                                           "probe, not a kill")
            elif "FAIL" in probe:
                verdict, detail = "killed", "the named test failed"
            elif "FAIL" in run(every):
                verdict, detail = "SURVIVED", "a different test failed"
            else:
                verdict, detail = "covered", "the whole suite passed"

            # Restore IMMEDIATELY, not at the end: a later row's pattern search
            # must never see an earlier row's mutation.
            open(os.path.join(D, target), "w").write(src)

            if verdict == "killed":
                killed += 1
            elif verdict == "covered":
                covered += 1
            elif verdict == "SKIP":
                skipped += 1
            else:
                survived += 1
            print(f"  {verdict.upper():8} {label}\n           {detail}")
    finally:
        for t, s_ in srcs.items():
            open(os.path.join(D, t), "w").write(s_)

    print(f"\n{killed} killed, {covered} covered by a lower layer, "
          f"{survived} survived, {skipped} skipped")
    if gone:
        print(f"  ({len(gone)} rows are STALE and were not run)")
    return 1 if survived else 0


sys.exit(main())
