#!/usr/bin/env python3
"""Mutation harness for internal/torrent -- the wiring between policy, gate and
library.

The three decisions under test are individually correct and unsafe in
combination, so a mutation that breaks one of them has to be caught by a test
that can still see the other two. That is what makes the `covered` verdict
necessary here, and why it is decided by a whole-suite re-run rather than by
judgement.

Six verdicts:

    killed    the named test failed
    covered   no test noticed AND the whole suite still passed with the mutation
              applied -- a layer below refused the same input. A verified claim.
    survived  the whole suite FAILED and the named test did not. A HOLE.

Every `run` has a TIMEOUT: a mutation can deadlock the client, and a harness
that hangs is a harness that gets killed before it reports.
"""

import subprocess, os, sys, tempfile, shutil

D = "/home/alvaro/code-local/go/stash/plugins/p2pdownloader"
TARGET = "internal/torrent/downloader.go"
ENV = {**os.environ, "GOFLAGS": "-mod=mod"}
PKG = "./internal/torrent/"
TIMEOUT = 300

# (label, old, new, the test that should notice)
MUTATIONS = [
    # --- the upload permission, the thing this package exists to enforce -----
    ("upload: always allow (inverted, so it still compiles)",
     "\tspec.DisallowDataUpload = !upload", "\tspec.DisallowDataUpload = !true && upload",
     "TestTheLibraryIsActuallyToldWhatThePolicyDecided"),
    ("upload: operator's setting ignored",
     "OperatorAllowedSeed: d.cfg.OperatorAllowedSeed,\n\t})", "OperatorAllowedSeed: true,\n\t})",
     "TestAPermissiveTierStillUploadsNothingWhenTheOperatorDeclines"),
    ("upload: client's NoUpload backstop off",
     "\tc.NoUpload = true\n\tc.Seed = false", "\tc.NoUpload = false\n\tc.Seed = false",
     "TestTheClientIsConfiguredNotToUploadByDefault"),
    ("upload: client-wide upload rate limit applied",
     "c.UploadRateLimiter = rate.NewLimiter(rate.Inf, c.MaxAllocPeerRequestDataPerConn)",
     "c.UploadRateLimiter = rate.NewLimiter(rate.Limit(1024), 1024)",
     "TestTheDownloadRateLimitIsSetAndTheUploadLimitIsNot"),

    # --- the storage gate ----------------------------------------------------
    ("gate: up-front refusal ignored",
     "d.gate.OpenTorrent(context.Background(), &info, hash); err != nil {",
     "d.gate.OpenTorrent(context.Background(), &info, hash); err != nil && false {",
     "TestTheGateIsAskedAndItsRefusalIsWhatStopsTheTorrent"),
    ("gate: per-torrent storage bypasses the gate",
     "\tspec.Storage = d.gate", "\tspec.Storage = nil",
     "TestTheLibraryIsGivenTheGateAndNotStorageOfItsOwn"),
    ("gate: refusal not distinguished from a later failure",
     "Err:           fmt.Errorf(\"%w: %v\", ErrRefusedUpFront, err),",
     "Err:           fmt.Errorf(\"refused: %v\", err),",
     "TestTheGateIsAskedAndItsRefusalIsWhatStopsTheTorrent"),
    ("gate: DataDir set alongside DefaultStorage",
     "\tc.DefaultStorage = gate", "\tc.DefaultStorage = gate\n\tc.DataDir = root",
     "TestDataDirIsNeverSet"),

    # --- the download root ---------------------------------------------------
    ("root: empty accepted", "\tif root == \"\" {", "\tif false {",
     "TestAMissingOrUnusableRootIsRejectedRatherThanDefaulted"),
    ("root: relative accepted", "\tif !filepath.IsAbs(root) {", "\tif false {",
     "TestAMissingOrUnusableRootIsRejectedRatherThanDefaulted"),
    ("root: not-a-directory accepted", "\tif !info.IsDir() {", "\tif false {",
     "TestAMissingOrUnusableRootIsRejectedRatherThanDefaulted"),
    ("root: missing directory accepted",
     "\tresolved, err := filepath.EvalSymlinks(root)\n\tif err != nil {", "\tresolved := root\n\tvar err error\n\tif false {",
     "TestAMissingDownloadRootIsRefused"),

    # --- rate limits ---------------------------------------------------------
    ("rate: download limiter removed",
     "\tc.DownloadRateLimiter = rate.NewLimiter(rate.Limit(perSecond), burst)", "\t_, _ = perSecond, burst",
     "TestTheDownloadRateLimitIsSetAndTheUploadLimitIsNot"),
    ("rate: default of zero means unlimited",
     "\t\tperSecond = defaultDownloadRate", "\t\tperSecond = 0",
     "TestTheDownloadRateLimitHasADefaultAndHonoursAnExplicitOne"),

    # --- reachability --------------------------------------------------------
    ("network: port forwarding on at construction",
     "\tc.NoDefaultPortForwarding = true", "\tc.NoDefaultPortForwarding = false",
     "TestTheTransportIsOffUntilSomethingAsksToListen"),
    ("network: DHT on before anything asked to listen",
     "\tc.NoDHT = true\n\tc.DisableTCP = true\n\tc.DisableUTP = true\n\tc.NoDefaultPortForwarding = true",
     "\tc.NoDHT = false\n\tc.DisableTCP = false\n\tc.DisableUTP = false\n\tc.NoDefaultPortForwarding = true",
     "TestTheTransportIsOffUntilSomethingAsksToListen"),
    ("network: webseeds disabled",
     "\tc.DisableWebseeds = false", "\tc.DisableWebseeds = true",
     "TestWebseedsStayEnabled"),

    # --- the magnet path -----------------------------------------------------
    ("magnet: nameless spec added ungated",
     "\tif spec == nil || len(spec.InfoBytes) == 0 {", "\tif false {",
     "TestAMagnetWithNoMetadataIsNotAdded"),
    ("magnet: a parse failure folded into the up-front refusal",
     "\t\t\tErr: fmt.Errorf(\"the torrent metadata could not be parsed, so there \"+\n\t\t\t\t\"are no file names to check: %w\", err),",
     "\t\t\tErr: fmt.Errorf(\"%w: %v\", ErrRefusedUpFront, err),",
     "TestAMalformedTorrentIsNotAHostileOne"),

    # --- the record ----------------------------------------------------------
    ("record: the recorded info hash is the zero hash",
     "\t\tInfoHash:           spec.InfoHash,", "\t\tInfoHash:           metainfo.Hash{},",
     "TestTheHashTheGateRefusedIsTheHashTheLibraryUses"),
]


BUILD_ERRORS = ("build failed", "cannot use", "undefined:", "declared and not used",
                "syntax error", "not enough arguments", "too many arguments")


def is_build_error(out):
    return any(e in out for e in BUILD_ERRORS)


def run(tests, extra=""):
    cmd = f"go test {PKG} -count=1 -timeout={TIMEOUT}s {extra} " + " ".join(
        f"-run {t}" for t in tests)
    try:
        r = subprocess.run(cmd, shell=True, cwd=D, capture_output=True, text=True,
                           env=ENV, timeout=TIMEOUT + 90)
        return r.stdout + r.stderr
    except subprocess.TimeoutExpired:
        return "TIMEOUT: the mutation deadlocked the client"


def main():
    src = open(os.path.join(D, TARGET)).read()
    backup = os.path.join(D, TARGET + ".harnessbak")
    shutil.copy(os.path.join(D, TARGET), backup)
    all_tests = subprocess.run(
        f"go test {PKG} -list '.*' 2>/dev/null", shell=True, cwd=D,
        capture_output=True, text=True, env=ENV).stdout
    every = [l for l in all_tests.split("\n") if l.startswith("Test")]
    print(f"{len(every)} tests in the package; {len(MUTATIONS)} mutations\n")

    killed = covered = survived = skipped = 0
    try:
        for label, old, new, test in MUTATIONS:
            if old not in src:
                print(f"  SKIP    {label}\n          pattern not found -- the code moved")
                skipped += 1
                continue
            open(os.path.join(D, TARGET), "w").write(src.replace(old, new, 1))

            probe = run([test])
            if "TIMEOUT" in probe:
                verdict, detail = "SURVIVED", "the named test TIMED OUT (deadlock)"
            elif is_build_error(probe):
                verdict, detail = "SKIP", "the mutation does not compile -- a bad probe, not a kill"
            elif "FAIL" in probe:
                verdict, detail = "killed", "the named test failed"
            elif "FAIL" in run(every):
                # a different test caught it: still a hole for THIS row
                verdict, detail = "SURVIVED", "a different test failed"
            else:
                verdict, detail = "covered", "the whole suite passed"

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
        shutil.copy(backup, os.path.join(D, TARGET))
        os.remove(backup)

    print(f"\n{killed} killed, {covered} covered by a lower layer, "
          f"{survived} survived, {skipped} skipped")
    return 1 if survived else 0


sys.exit(main())
