#!/usr/bin/env python3
"""Mutation check for the consent guard (M3 step 3.1).

The plan's warning is the reason this file exists:

    This is the milestone where a mistake is irreversible -- a published private
    library cannot be unpublished from users' copies. Mutation-check the opt-out:
    make ShareOptedIn always return true and confirm
    TestPublish_RefusedWhenOptedOut fails.

So the mutation that matters most is the one that silently turns every user into
a consenting one. If that mutation SURVIVES, this harness is telling you
nothing about the guard, and the number below is decoration.

Every mutation here must be KILLED. There is no EXEMPT list, and that is
deliberate: a survivor in this file is either a missing test or a bug, and both
are worth stopping for.
"""

import atexit
import pathlib
import re
import shutil
import signal
import subprocess
import sys
import tempfile

ROOT = pathlib.Path(__file__).resolve().parent.parent.parent
CONSENT = ROOT / "internal/collab/consent.go"
EXPORTER = ROOT / "internal/collab/exporter.go"
MODE = ROOT / "internal/collab/mode.go"
TOTP = ROOT / "internal/collab/totp.go"
FEDERATION = ROOT / "internal/collab/federation.go"
AUTH_TOTP = ROOT / "pkg/auth/totp.go"
AUTH_SESSION = ROOT / "pkg/auth/session.go"

# Which file each mutation applies to, and the test selection that must kill it.
AUTH_TEST_RE = ("Wiring_|Login_|TOTP")

TEST_RE = ("Consent|Disclosure|PublishedField|SetConsent|Export|Publish|Payload|"
           # M4 step 4.2: the replay guard, the counter, and the truncation.
           "TOTP|"
           # M4 step 4.4: the wizard gate.
           "RequireWizard|WizardIncomplete|"
           "SubmissionID|AssertNo|Federation|Commons|SignSubmission|MakeSubmission|"
           "SummarizePeers|RedactPeerKey|"
           # M4: the mode rules. CheckStartup and the media refusal, the two
           # fail-closed defaults, and RequestScheme.
           "Mode|CheckStartup|MediaNotFound|RequestScheme")

# (file, label, old, new) -- each removes or inverts one safety property.
CONSENT_MUTATIONS = [
    (
        "absent row treated as opted OUT (the default inverted)",
        "		return true, nil\n	}\n	return choice == ChoiceOptedIn, nil",
        "		return false, nil\n	}\n	return choice == ChoiceOptedIn, nil",
    ),
    (
        "opt-out user reported as opted in (the reverse)",
        "	return choice == ChoiceOptedIn, nil",
        "	_ = choice\n	return true, nil",
    ),
    (
        "a failed consent query is swallowed and reads as opted in",
        "	if err != nil {\n		return \"\", false, fmt.Errorf(\"querying consent for user %d: %w\", userID, err)\n	}",
        "	if err != nil {\n		return ChoiceOptedIn, true, nil\n	}",
    ),
    (
        "a failed consent query is swallowed and reads as opted OUT",
        "	if err != nil {\n		return \"\", false, fmt.Errorf(\"querying consent for user %d: %w\", userID, err)\n	}",
        "	if err != nil {\n		return ChoiceOptedOut, true, nil\n	}",
    ),
    (
        "corrupt metadata_share defaults to opted in instead of erroring",
        "	if !parsed.Valid() {\n		return \"\", false, fmt.Errorf(\"consent for user %d has unknown metadata_share %q\", userID, choice)\n	}",
        "	if !parsed.Valid() {\n		return ChoiceOptedIn, true, nil\n	}",
    ),
    (
        "opted-out user is re-prompted (a prompt is a Share button)",
        "	if choice == ChoiceOptedOut {\n		// Declined. Stays declined, and stays un-prompted, until a human says\n		// otherwise. See the comment above.\n		return false, nil\n	}",
        "	if choice == ChoiceOptedOut {\n		return true, nil\n	}",
    ),
    (
        "disclosure staleness inverted, so a stale answer looks current",
        "	return version < current, nil",
        "	return version >= current, nil",
    ),
    (
        "an invalid choice is written instead of refused",
        "	if !choice.Valid() {\n		return fmt.Errorf(\"consent choice %q is not one of %q or %q\", choice, ChoiceOptedIn, ChoiceOptedOut)\n	}",
        "",
    ),
    (
        "never-asked user is not prompted",
        "	if !found {\n		return true, nil\n	}\n	if choice == ChoiceOptedOut {",
        "	if !found {\n		return false, nil\n	}\n	if choice == ChoiceOptedOut {",
    ),
]


def run(cmd, cwd):
    return subprocess.run(
        cmd, cwd=cwd, capture_output=True, text=True, shell=isinstance(cmd, str)
    )


EXPORTER_MUTATIONS = [
    (
        "exporter: an undisclosed field is passed through",
        "		if allowed[k] {\n			out[k] = v\n		}",
        "		_ = allowed\n		out[k] = v",
    ),
    (
        "exporter: the dry-run default is flipped to a real publish",
        "		// The first export after consent writes to disk and sends nothing\n		// (spec §6.3). The caller may clear this once the user has seen the\n		// disclosure and asked for a real publish.\n		DryRun: true,",
        "		DryRun: false,",
    ),
    (
        "exporter: a path-shaped value is no longer refused",
        "	if i := strings.Index(v, \"/\"); i >= 0 && i < len(v)-1 {",
        "	if i := -1; i >= 0 && i < len(v)-1 {",
    ),
    (
        "exporter: a non-content fingerprint type is published",
        "		if !allowedFingerprintTypes[k] {\n			continue\n		}",
        "",
    ),
    (
        "exporter: the submission id ignores the entries (content-blind)",
        "		fmt.Fprintf(h, \"entry=%s/%d\\n\", e.TargetType, e.TargetID)",
        "		_ = e",
    ),
]

FEDERATION_MUTATIONS = [
    (
        "federation: the constant-time compare becomes a byte-by-byte one",
        "	if !hmac.Equal([]byte(want), []byte(signature)) {",
        "	if want != signature {",
    ),
    (
        "federation: the content-address check is skipped (signature alone accepted)",
        "	if err := VerifySubmissionContent(p, libraryID); err != nil {\n		r.audit(ctx, peer, \"federation_rejected_content_mismatch\", err)\n		return err\n	}",
        "",
    ),
    (
        "federation: the consume flag is not checked on receive",
        "	if !peer.ConsumeFrom {\n		return fmt.Errorf(\"%w: peer %d (%s)\", ErrPeerConsumeNotEnabled, peer.ID, peer.Name)\n	}",
        "",
    ),
    (
        "federation: the publish flag is not checked when signing",
        "	if !peer.PublishTo {\n		return SignedSubmission{}, fmt.Errorf(\"%w: peer %d (%s)\", ErrPeerPublishNotEnabled, peer.ID, peer.Name)\n	}",
        "",
    ),
    (
        "federation: an empty signature is accepted as if it verified",
        "	if signature == \"\" {\n		return ErrUnsignedSubmission\n	}",
        "",
    ),
    (
        "federation: the commons read treats a missing authorizer as allowed",
        "	if !allowed {\n		return Payload{}, ErrCommonsNotFound\n	}",
        "	_ = allowed",
    ),
    (
        "federation: the domain separator is dropped from the MAC",
        "	mac.Write([]byte(\"stashforge/federation/v1\"))\n	mac.Write([]byte{0})",
        "",
    ),
    (
        "federation: a peer without a key is allowed to sign",
        "	if len(peer.Key) == 0 {\n		// A peer with no key cannot sign, and an unsigned submission is refused\n		// on the receiving end anyway -- so failing here gives the operator a\n		// useful message instead of a rejection from a peer they cannot debug.\n		return SignedSubmission{}, fmt.Errorf(\"federation: peer %d (%s) has no key configured\", peer.ID, peer.Name)\n	}",
        "",
    ),
]


MODE_FIXTURES = [
    # The whole point of step 4.1: public over plain HTTP must refuse to start.
    ("public mode no longer requires TLS",
     "func (m Mode) RequiresTLS() bool { return m == ModePublic }",
     "func (m Mode) RequiresTLS() bool { return false }"),
    ("public mode served over http",
     "func ModeErrors(m Mode, scheme string) error {\n\tif !m.Valid() {",
     "func ModeErrors(m Mode, scheme string) error {\n\tif true {"),
    ("any scheme counts as secure",
     '\treturn strings.EqualFold(strings.TrimSpace(scheme), "https")',
     '\treturn true'),
    ("contribute also refuses plain http (over-refusal is still a change)",
     "func (m Mode) RequiresTLS() bool { return m == ModePublic }",
     "func (m Mode) RequiresTLS() bool { return true }"),
    ("contribute starts serving media",
     "func (m Mode) ServesMedia() bool { return m == ModePublic }",
     "func (m Mode) ServesMedia() bool { return m != ModePrivate }"),
    ("private mode accepts anonymous proposals",
     "func (m Mode) AcceptsAnonymousProposals() bool { return m == ModePublic }",
     "func (m Mode) AcceptsAnonymousProposals() bool { return true }"),
    # A grant is what authorises media in public mode; dropping it is a leak.
    ("media served without a grant",
     "\tif !hasGrant {\n\t\treturn ErrMediaNotServed\n\t}",
     "\tif false {\n\t\treturn ErrMediaNotServed\n\t}"),
    ("private and contribute serve media to a grantee",
     "\tif !m.ServesMedia() {",
     "\tif false {"),
    # Undifferentiated 404: distinguishing them discloses that the file exists.
    ("no-grant refusal names itself",
     "var ErrMediaNotServed = errors.New(\"not found\")",
     "var ErrMediaNotServed = errors.New(\"no library access grant for this file\")"),
    # Fail closed. Every one of these flips a default to permissive.
    ("an absent mode defaults to public",
     "\treturn ModePrivate\n}",
     "\treturn ModePublic\n}"),
    ("an invalid mode on the context is taken at face value",
     "\tif m, ok := ctx.Value(ModeKey{}).(Mode); ok && m.Valid() {\n\t\treturn m\n\t}",
     "\tif m, ok := ctx.Value(ModeKey{}).(Mode); ok {\n\t\treturn m\n\t}"),
    # The wizard's veto over an unchosen public mode.
    ("the wizard no longer gates a public mode",
     "\tif mode == ModePublic && !wizardCompleted {",
     "\tif false {"),
    ("the wizard gate applies to every mode",
     "\tif mode == ModePublic && !wizardCompleted {",
     "\tif !wizardCompleted {"),
    # Store-side fail-closed: a missing row must not mean \"public\".
    ("an absent context mode defaults to public",
     "\treturn ModePrivate\n}",
     "\treturn ModePublic\n}"),
]


TOTP_FIXTURES = [
    # THE REPLAY GUARD. This is the whole point of step 4.2: RFC 6238 accepts the
    # same code any number of times inside its window, and for a login second
    # factor that is a real weakness.
    ("a replayed code is accepted",
     "\tif used[matched] {\n\t\treturn matched, ErrTOTPReplay\n\t}",
     "\tif false {\n\t\treturn matched, ErrTOTPReplay\n\t}"),
    ("a replay is reported as a distinct error (a confirmation oracle)",
     "\tif errors.Is(err, ErrTOTPReplay) {\n\t\t// Folded into the generic error on purpose. See ErrTOTPInvalid.\n\t\treturn ErrTOTPInvalid\n\t}",
     "\tif errors.Is(err, ErrTOTPReplay) {\n\t\treturn ErrTOTPReplay\n\t}"),
    # THE COUNTER. int64(TOTPStep) is nanoseconds, so this bug makes every code
    # come from counter 0 -- a constant that no real authenticator produces.
    ("the counter divides by nanoseconds instead of seconds",
     "return at.UTC().Unix() / int64(TOTPStep/time.Second)",
     "return at.UTC().Unix() / int64(TOTPStep)"),
    ("the counter is fixed at zero",
     "return at.UTC().Unix() / int64(TOTPStep/time.Second)",
     "return 0"),
    # THE TRUNCATION. Masking a 32-bit word with 0x7fffffff also clears the high
    # bits of three other bytes, so it disagrees with RFC 4226 most of the time.
    ("dynamic truncation masks the whole word instead of the top byte",
     "value := (int64(h[offset]&0x7f) << 24) |\n\t\t(int64(h[offset+1]) << 16) |\n\t\t(int64(h[offset+2]) << 8) |\n\t\tint64(h[offset+3])",
     "value := int64(binary.BigEndian.Uint32(h[offset:offset+4]) & 0x7fffffff)"),
    ("the dynamic-truncation offset uses the wrong mask",
     "offset := h[len(h)-1] & 0x0f",
     "offset := h[len(h)-1] & 0x07"),
    # THE CONCURRENCY GUARD. Check-then-record split across two locks is exactly
    # the race "single-use" exists to prevent, and is invisible single-threaded.
    # The check and the record are ONE critical section. Moving the check to a
    # separate pass (rather than into the same loop) is the realistic mistake,
    # and it is exactly what TestTOTP_ConcurrentReplayIsRefused exists to catch.
    ("the spend check and the record are not atomic",
     "\tfor k, v := range g.used {\n\t\tif k < oldest {\n\t\t\tdelete(g.used, k)\n\t\t} else if v && k == step {\n\t\t\treturn false\n\t\t}\n\t}\n\tg.used[step] = true",
     "\tfor k := range g.used {\n\t\tif k < oldest {\n\t\t\tdelete(g.used, k)\n\t\t}\n\t}\n\tif g.used[step] {\n\t\treturn false\n\t}\n\tg.used[step] = true"),
    ("the spend guard is unlocked",
     "\tg.mu.Lock()\n\tdefer g.mu.Unlock()",
     "\t// unlocked"),
    # DRIFT TOLERANCE.
    ("the skew window is removed entirely",
     "for delta := int64(-TOTPSkew); delta <= TOTPSkew; delta++ {",
     "for delta := int64(0); delta <= 0; delta++ {"),
    # DISCLOSURE.
    ("the secret is printed by String()",
     'func (s TOTPSecret) String() string {\n\tif s == "" {\n\t\treturn ""\n\t}\n\treturn "[REDACTED TOTP SECRET]"\n}',
     'func (s TOTPSecret) String() string { return string(s) }'),
    # MarkTOTPStep must return a copy: a shared map is how a replay gets through.
    ("MarkTOTPStep mutates the caller's map",
     "next := make(map[int64]bool, len(used)+1)",
     "next := used"),
    # Purge: dropping a step that is still acceptable reopens the window.
    ("the purge drops steps that are still inside the window",
     "\toldest := totpCounter(now) - TOTPSkew",
     "\toldest := totpCounter(now) - 1000"),
]

# M4 step 4.4: the wizard gate. "The wizard cannot be skipped" is only true
# while these comparisons are true. These belong to mode.go, NOT totp.go -- and
# they were originally appended to TOTP_FIXTURES, which meant every one of them
# would have mutated the wrong file and reported BROKEN forever.
MODE_WIZARD_FIXTURES = [
    ("the wizard gate lets an incomplete instance through",
     "\t\t// that was never asked.\n\t\treturn ModePrivate, ErrWizardIncomplete{}\n\t}",
     "\t\t// that was never asked.\n\t\treturn ModePrivate, nil\n\t}"),
    ("the wizard gate is inverted",
     "\tif !done {",
     "\tif done {"),
    # The gate must refuse BEFORE reading the mode, so an unchosen mode never
    # becomes an answer to a question nobody asked.
    ("the gate reads the mode before checking the wizard",
     "func RequireWizard(ctx context.Context, g Gate) (Mode, error) {\n\tdone, err := g.WizardCompleted(ctx)",
     "func RequireWizard(ctx context.Context, g Gate) (Mode, error) {\n\tif m, mErr := g.Mode(ctx); mErr == nil && m == ModePublic {\n\t\treturn m, nil\n\t}\n\tdone, err := g.WizardCompleted(ctx)"),
    # The gate must return private on refusal, never the row's value.
    ("the wizard refusal returns the instance's real mode",
     "\t\treturn ModePrivate, ErrWizardIncomplete{}",
     "\t\treturn ModePublic, ErrWizardIncomplete{}"),
    # A database failure reported as "wizard incomplete" sends the operator to
    # the setup screen when the real problem is a broken database.
    ("a database failure is reported as the wizard refusal",
     "func RequireWizard(ctx context.Context, g Gate) (Mode, error) {\n\tdone, err := g.WizardCompleted(ctx)\n\tif err != nil {\n\t\treturn ModePrivate, err\n\t}",
     "func RequireWizard(ctx context.Context, g Gate) (Mode, error) {\n\tdone, err := g.WizardCompleted(ctx)\n\tif err != nil {\n\t\treturn ModePrivate, ErrWizardIncomplete{}\n\t}"),
    ("an invalid mode from the gate is not an error",
     "\tm, err := g.Mode(ctx)\n\tif err != nil {\n\t\treturn ModePrivate, err\n\t}\n\treturn m, nil\n}",
     "\tm, err := g.Mode(ctx)\n\tif err != nil {\n\t\treturn ModePrivate, nil\n\t}\n\tif !m.Valid() {\n\t\treturn ModePublic, nil\n\t}\n\treturn m, nil\n}"),
]


# pkg/auth: the 2FA boundary. These exist because the replay guard was ONCE
# implemented, tested in isolation, and not called by the login path -- a comment
# claimed the store owned the spend while the spend was never made. Every
# mutation below removes or weakens one specific link in the chain, so the next
# time that chain is broken by accident, a test fails instead of a reviewer
# wondering why the comment and the code disagree.
AUTH_TOTP_FIXTURES = [
    # THE REPLAY SPEND. Removing this is precisely the bug that shipped.
    ("the login path stops spending the step it just verified",
     "\t\tfresh, err := s.totpStore.SpendTOTPStep(ctx, userID, step)",
     "\t\tfresh, err := true, error(nil)\n\t\t_ = step"),
    # The spend's verdict is what refuses a replay; ignoring it accepts one.
    ("a spent step is treated as fresh",
     "\t\tif !fresh {",
     "\t\tif false {\n\t\t\t_ = fresh"),
    # Failing OPEN when the store cannot record the spend: the single-use rule
    # silently stops applying whenever the database is busy.
    ("a store that cannot record the spend is ignored",
     "\t\tif err != nil {\n\t\t\t// A store that cannot record the spend has not verified anything, so\n\t\t\t// the login is refused. Failing open here would mean the single-use\n\t\t\t// rule silently stops applying the first time the database is busy.\n\t\t\treturn fmt.Errorf(\"%w: %v\", ErrTOTPStoreUnavailable, err)",
     "\t\tif err != nil {\n\t\t\t_ = err\n\t\t\t_ = fresh"),
    # The arithmetic itself, on this path.
    ("the login verifies no code at all",
     "\t\tstep, err := collab.VerifyTOTPDetailed(collab.TOTPSecret(secret), code, s.now(), nil)",
     "\t\tstep, err := int64(0), error(nil)"),
    # THE PROMPT. Without this, an enrolled user with no code gets a session.
    ("an enrolled user is not asked for a code",
     "\t\tif code == \"\" {\n\t\t\treturn ErrTOTPRequired",
     "\t\tif false {\n\t\t\treturn ErrTOTPRequired"),
    # THE POLICY. An unavailable store must not read as "no 2FA".
    ("a failed policy check reads as not-required",
     "\t\t\tpolicyRequired, pErr := s.totp.Required(ctx, userID)",
     "\t\t\tpolicyRequired, pErr := false, error(nil)"),
    # THE LOCKOUT GUARD. Required-but-not-enrolled is a lockout, not a free pass.
    ("a required but unenrolled account is let in",
     "\t\tif secret == \"\" {",
     "\t\tif false {"),
]


# pkg/auth/session.go: where a 2FA failure becomes indistinguishable from a wrong
# password. The guarantee is the FOLD, not the marker it folds -- and my first
# attempt mutated the marker inside checkSecondFactor and SURVIVED, correctly,
# because the fold already hides it. Nothing observable changed, so there was
# nothing for a test to catch. Mutating a comment's subject instead of the
# mechanism is how a harness earns a meaningless pass.
AUTH_SESSION_FIXTURES = [
    ("a 2FA failure is not collapsed into the invalid-credentials error",
     "\t\tif errors.Is(err, ErrTOTPRequired) {\n\t\t\treturn nil, err\n\t\t}",
     "\t\tif errors.Is(err, ErrTOTPRequired) {\n\t\t\treturn nil, err\n\t\t}\n\t\tif true {\n\t\t\treturn nil, err\n\t\t}"),
    # The prompt is distinguishable ON PURPOSE -- the password is already proven,
    # so there is nothing left to disclose. If this stops being the only
    # distinguishable case, the fold has a hole and the form becomes an oracle.
    ("a wrong code is distinguishable from a wrong password",
     "\t\tif errors.Is(err, ErrTOTPRequired) {\n\t\t\treturn nil, err\n\t\t}",
     "\t\tif true {\n\t\t\treturn nil, err\n\t\t}"),
]


def preflight():
    """Verify every fixture before running any of them.

    Added after three consecutive runs reported BROKEN with an anchor not found.
    Each was a real mistake -- a one-line function written as a braced block, a
    comment inside a branch, and a list of mode.go fixtures appended to the TOTP
    list -- and each cost a full sweep to discover, because BROKEN is only
    reported after every mutation has been applied and tested.

    A broken anchor is a fixture that scores nothing, and a fixture that scores
    nothing is worse than no fixture: the count says 53 while 6 of them did not
    run. So the anchors are checked up front, and a mismatch is an error before
    any file is touched.
    """
    originals = {p: p.read_text() for p in (CONSENT, EXPORTER, FEDERATION, MODE, TOTP, AUTH_TOTP, AUTH_SESSION)}
    problems = []
    total = 0
    for path, fixtures in (
        (CONSENT, CONSENT_MUTATIONS),
        (EXPORTER, EXPORTER_MUTATIONS),
        (FEDERATION, FEDERATION_MUTATIONS),
        (MODE, MODE_FIXTURES),
        (MODE, MODE_WIZARD_FIXTURES),
        (TOTP, TOTP_FIXTURES),
        (AUTH_TOTP, AUTH_TOTP_FIXTURES),
        (AUTH_SESSION, AUTH_SESSION_FIXTURES),
    ):
        for label, old, new in fixtures:
            total += 1
            if old not in originals[path]:
                problems.append(f"{path.name}: {label} -- anchor not found")
            elif old == new:
                problems.append(f"{path.name}: {label} -- replacement is a no-op")
    if problems:
        print("PREFLIGHT FAILED -- no file was touched:", file=sys.stderr)
        for p in problems:
            print(f"  {p}", file=sys.stderr)
        return 2
    print(f"preflight ok: {total} fixtures, every anchor lands")
    return 0


def _install_restore_guard(originals):
    """Make the restore happen even if this process is killed mid-run.

    try/finally handles an exception, but NOT SIGINT or SIGTERM: the default
    disposition of both terminates immediately without unwinding, and SIGKILL
    cannot be handled at all. The interrupted run that prompted this left
    `return true, nil` in consent.go and `DryRun: false` in exporter.go sitting
    in the working tree -- the two mutations in the project that would publish a
    user who declined.

    So the restore is registered three ways: a finally block, an atexit hook, and
    a signal handler. A SIGKILL still defeats all three, which is why
    `verify_restored` is also run before anything is committed.
    """
    def restore():
        for path, text in originals.items():
            try:
                if path.read_text() != text:
                    path.write_text(text)
                    print(f"restored {path.name}", file=sys.stderr)
            except OSError:
                pass

    atexit.register(restore)
    for sig in (signal.SIGINT, signal.SIGTERM):
        try:
            signal.signal(sig, lambda s, f: (restore(), sys.exit(130)))
        except (ValueError, OSError):
            pass  # not on the main thread; the finally block still applies
    return restore


def verify_restored(originals):
    """Fail loudly if the tree is still mutated. Called after the run and by
    verify_tree.py before any commit."""
    dirty = [p.name for p, t in originals.items() if p.read_text() != t]
    if dirty:
        print(
            "FATAL: these files are not at their original content: "
            + ", ".join(dirty)
            + "\nRun: git checkout -- internal/collab/",
            file=sys.stderr,
        )
        return 2
    return 0


def main():
    if preflight() != 0:
        return 2
    COLLAB_RUN = ("./internal/collab/", TEST_RE)
    AUTH_RUN = ("./pkg/auth/", AUTH_TEST_RE)

    work = (
        [(CONSENT, m, COLLAB_RUN) for m in CONSENT_MUTATIONS]
        + [(EXPORTER, m, COLLAB_RUN) for m in EXPORTER_MUTATIONS]
        + [(FEDERATION, m, COLLAB_RUN) for m in FEDERATION_MUTATIONS]
        + [(MODE, m, COLLAB_RUN) for m in MODE_FIXTURES]
        + [(MODE, m, COLLAB_RUN) for m in MODE_WIZARD_FIXTURES]
        + [(TOTP, m, COLLAB_RUN) for m in TOTP_FIXTURES]
        + [(AUTH_TOTP, m, AUTH_RUN) for m in AUTH_TOTP_FIXTURES]
        + [(AUTH_SESSION, m, AUTH_RUN) for m in AUTH_SESSION_FIXTURES]
    )
    originals = {p: p.read_text() for p in (CONSENT, EXPORTER, FEDERATION, MODE, TOTP, AUTH_TOTP, AUTH_SESSION)}
    killed, survived, broken = [], [], []
    _install_restore_guard(originals)

    try:
        for path, (label, old, new), (pkg, sel) in work:
            original = originals[path]
            if old not in original:
                # A replacement that does not land reports as a survivor that
                # means nothing. Distinguish it, or the number lies.
                broken.append(f"{label} -- ANCHOR NOT FOUND (mutation never applied)")
                print(f"BROKEN   {label} (anchor not found)")
                continue

            mutated = original.replace(old, new, 1)
            if mutated == original:
                broken.append(f"{label} -- replacement was a no-op")
                print(f"BROKEN   {label} (no-op replacement)")
                continue

            path.write_text(mutated)
            proc = run(
                ["go", "test", pkg, "-run", sel, "-count=1"],
                ROOT,
            )
            out = proc.stdout + proc.stderr

            # A mutation that does not compile kills no test, and a guard that
            # only looks for a test failure will score it SURVIVED -- a lie
            # about the suite. Detect the build error explicitly.
            if re.search(r"^(\[|.*\] )?# ", out, re.M) or "cannot use" in out or "undefined:" in out:
                print(f"BROKEN   {label} (does not compile -- scores nothing)")
                broken.append(f"{label} -- does not compile")
            elif proc.returncode != 0:
                killed.append(label)
                print(f"KILLED   {label}")
            else:
                print(f"SURVIVED {label}  <-- investigate")
                survived.append(label)

    finally:
        for path, text in originals.items():
            path.write_text(text)
        _restore = None

    # Confirm the restore actually took, so an interrupted run cannot leave the
    # tree mutated for the next commit.
    if verify_restored(originals) != 0:
        return 2

    total = len(work)
    print(f"\napplied {total} / killed {len(killed)} / survived {len(survived)} / broken {len(broken)}")

    if broken:
        print("\nBROKEN means the harness itself failed, not that the code is weak:", file=sys.stderr)
        for b in broken:
            print(f"  {b}", file=sys.stderr)
        return 2
    if survived:
        print(
            "\nA survivor in this file is either a missing test or a bug in the guard.",
            file=sys.stderr,
        )
        return 1
    return 0


if __name__ == "__main__":
    sys.exit(main())
