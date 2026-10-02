#!/usr/bin/env python3
"""Mutation gate for relaycrypto: R090, end-to-end encryption over the relay.

WHY THIS GATE IS FOR ONE SMALL PACKAGE
======================================

Because R090's claim is an ABSENCE -- a relay must not be able to read what it
carries -- and an absence is exactly what a round-trip test cannot check. A round trip
passes against a plaintext relay, which is the failure this step exists to close. So
the gate's job is to break each thing the design decided and confirm something notices.

Every mutant here is an INVERSION of a stated decision. There is no mutant that would
be surprising if it survived, which is the property worth having in a gate this small.

RUN
===

    cd plugins/p2pdownloader && python3 internal/relaycrypto/mutate_relaycrypto.py

Exit 0 means every mutant was killed. Exit 1 means at least one survived, named.
"""

from __future__ import annotations

import pathlib
import subprocess
import sys

HERE = pathlib.Path(__file__).resolve().parent
PLUGIN = HERE.parents[1]

CRYPTO = HERE / "relaycrypto.go"
AEAD = HERE / "aead.go"

MUTANTS: list[tuple[str, pathlib.Path, str, str, str]] = [
    (
        # THE ONE THAT MATTERS MOST. AES-CTR is a real cipher and encrypts
        # correctly -- it is simply UNAUTHENTICATED, so a flipped bit yields
        # different plaintext instead of an error. The manifest hash catches it
        # eventually, after 4 GiB of transfer.
        #
        # A round-trip test cannot see this at all: correct and mutated code agree
        # on every input the round trip uses. Only the tamper test distinguishes
        # them, which is why that test is written against the AEAD's own error.
        "authenticated-cipher-to-ctr",
        AEAD,
        "	plain, err := aead.Open(nil, nonce[:], sealed, aad)\n"
        "	if err != nil {\n"
        "		return nil, fmt.Errorf(\"%w: %v\", ErrAuth, err)\n"
        "	}",
        "	// MUTANT: unauthenticated CTR. Decrypts a flipped bit into different\n"
        "	// plaintext instead of refusing it.\n"
        "	plain := make([]byte, len(sealed)-16)\n"
        "	ctrStream := make([]byte, len(plain))\n"
        "	for i := range ctrStream {\n"
        "		ctrStream[i] = byte(nonce[4+(i/64)%8]) ^ byte(i)\n"
        "	}\n"
        "	for i := range plain {\n"
        "		plain[i] = sealed[i] ^ ctrStream[i]\n"
        "	}",
        "TestAFlippedBitIsRefusedRatherThanDecrypted -- an unauthenticated cipher " +
        "decrypts a flipped bit to DIFFERENT plaintext, and the manifest hash does " +
        "not catch it until 4 GiB has moved",
    ),
    (
        # The two directions sharing a nonce namespace. ChaCha20-Poly1305 with a
        # reused (key, nonce) pair leaks the XOR of the two plaintexts, so a peer's
        # request and a reply of similar length cancel out.
        "both-directions-share-one-namespace",
        CRYPTO,
        "	if isInitiator {\n		return dirOut, dirIn\n	}\n	return dirIn, dirOut",
        "	// MUTANT: one namespace for both directions.\n	return dirOut, dirIn",
        "TestBothDirectionsCarryTheirOwnContent -- one counter namespace for both " +
        "directions means a reused (key, nonce) pair, and ChaCha20-Poly1305 leaks " +
        "the XOR of the two plaintexts",
    ),
    (
        # Variable-length frames. A relay that only counts bytes would then infer
        # chunk boundaries and approximate the content's shape -- a size oracle with
        # no key, which is what FrameSize exists to close.
        "frame-size-ceiling-removed",
        CRYPTO,
        "	if n == 0 || n > FrameSize {",
        "	if n == 0 {",
        "TestAFrameLargerThanFrameSizeIsRefused -- a caller sending 1 MiB in one " +
        "call produces a 1 MiB frame, and the relay learns the caller's chunk size",
    ),
    (
        # Zero padding on the final frame. That makes the plaintext length a function
        # of the content length, which is the same size oracle by another route.
        "final-frame-padded-with-zeros",
        CRYPTO,
        "	if pad := FrameSize - n; pad > 0 {\n"
        "		if _, err := io.ReadFull(rand.Reader, plain[n:]); err != nil {\n"
        "			return 0, fmt.Errorf(\"relaycrypto: padding the final frame: %w\", err)\n"
        "		}\n"
        "	}",
        "	// MUTANT: deterministic padding, so length leaks content length.\n"
        "	for i := n; i < FrameSize; i++ {\n		plain[i] = 0\n	}",
        "TestAShortFinalFrameIsRandomlyPadded -- zero padding makes the plaintext " +
        "length a function of the content length, which is the oracle FrameSize closes",
    ),
    (
        # The counter no longer authenticated as additional data. It is carried in
        # the clear so the receiver can pick the right nonce without a round trip,
        # and a clear header is only safe because the tag covers it.
        "counter-not-authenticated",
        AEAD,
        "	sealed := aead.Seal(nil, nonce[:], plain, aad)",
        "	// MUTANT: the header is no longer covered by the tag.\n"
        "	sealed := aead.Seal(nil, nonce[:], plain, nil)",
        "TestAFlippedBitIsRefusedRatherThanDecrypted (header half) -- a clear " +
        "counter is only safe while the tag covers it, or anyone can re-point a " +
        "frame at a different slot",
    ),
    (
        # The all-zero peer key check. X25519 accepts it and yields the all-zero
        # shared secret, which both parties can compute without knowing anything --
        # and the failure is silent.
        "auth-error-distinguishes-its-cause",
        AEAD,
        "		return nil, fmt.Errorf(\"%w: %v\", ErrAuth, err)",
        "		return nil, fmt.Errorf(\"%w: OPEN FAILED (key or tampering): %v\", ErrAuth, err)",
        "TestAWrongKeyAndATamperedFrameAreTheSameError -- a caller must not be able " +
        "to provoke different answers and learn something about the peer",
    ),
]

TEST_RUNNER = ["go", "test", "./internal/relaycrypto/", "-count=1", "-run"]

# ONE PATTERN, USED THREE TIMES (baseline and both mutant runs), asserted by
# TestTheGateNamesEveryRelayCryptoTest which EXTRACTS it from this file.
SUITE_PATTERN = (
    "TestBothDirectionsCarryTheirOwnContent|"
    "TestARecordingRelayCannotReadTheContent|"
    "TestEqualLengthPlaintextsProduceEqualLengthCiphertexts|"
    "TestAFlippedBitIsRefusedRatherThanDecrypted|"
    "TestAReplayedFrameIsRefused|"
    "TestAnAllZeroPeerKeyIsRefused|"
    "TestAWrongKeyAndATamperedFrameAreTheSameError|"
    "TestAShortFinalFrameIsRandomlyPadded|"
    "TestAFrameLargerThanFrameSizeIsRefused|"
    "TestASessionRefusesWorkAfterClose|"
    "TestTheGateNamesEveryRelayCryptoTest|"
    "TestEveryMutantCatcherNamesATestThatExists"
)

GO_ENV = {"GOFLAGS": "-mod=mod", "PATH": "/usr/bin:/bin:/usr/local/bin", "HOME": "/home/alvaro"}


def run_tests(pattern: str) -> bool:
    r = subprocess.run(
        TEST_RUNNER + [pattern],
        cwd=str(PLUGIN),
        capture_output=True,
        text=True,
        env=GO_ENV,
    )
    return r.returncode == 0


def main() -> int:
    print("relaycrypto mutation gate: %d mutants\n" % len(MUTANTS))

    if not run_tests(SUITE_PATTERN):
        print("FATAL: the suite does not pass BEFORE mutation.")
        print("A gate run against a red suite reports every mutant as a survivor,")
        print("which reads as a finding about the code and is a finding about the gate.")
        return 1
    print("baseline: green\n")

    survivors = []
    for name, path, old, new, catcher in MUTANTS:
        original = path.read_text()
        if old not in original:
            print("  %-38s SKIPPED -- the pattern is not in the file." % name)
            print("     A mutant that cannot be APPLIED is not a test of anything;")
            print("     reporting it as survived would be the failure mode above.")
            survivors.append((name, "PATTERN NOT FOUND"))
            continue

        path.write_text(original.replace(old, new, 1))
        if new not in path.read_text():
            print("  %-38s ERROR -- applied but not found afterwards." % name)
            survivors.append((name, "SUBSTITUTION DID NOT LAND"))
            path.write_text(original)
            continue

        killed = not run_tests(SUITE_PATTERN)
        path.write_text(original)
        if path.read_text() != original:
            print("  %-38s ERROR -- restore failed, tree left mutated!" % name)
            survivors.append((name, "RESTORE FAILED"))
            continue

        if killed:
            print("  %-38s killed  <- %s" % (name, catcher.split(" --")[0]))
        else:
            print("  %-38s SURVIVED" % name)
            print("     expected catcher: %s" % catcher)
            survivors.append((name, catcher))

    print()
    if survivors:
        print("SURVIVORS: %d of %d" % (len(survivors), len(MUTANTS)))
        for name, why in survivors:
            print("  %-38s %s" % (name, why))
        return 1

    print("all %d mutants killed." % len(MUTANTS))
    return 0


if __name__ == "__main__":
    sys.exit(main())
