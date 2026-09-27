# Handoff

Where StashForge is, what is verified, and what the next person should know
before touching anything. Written for a cold start: no context from the session
that produced it.

Last updated: M3 (metadata sharing), tag `m3-metadata-sharing`.

---

## What this is

A fork of [stash](https://github.com/stashapp/stash) where the **library stays
local and private** and the **metadata is shared** through a self-governed
commons. Users are registered accounts, edits are proposals with reputation
weighting, and the published field set is disclosed to each user before anything
leaves their host.

The governing documents are `docs/GOAL.md` (milestone state) and
`docs/specs/2026-09-27-stashforge-{spec,plan}.md` (what to build and why).

## Milestone state

| Milestone | State | Tag |
|---|---|---|
| M0 baseline | done | — |
| M1 accounts, sessions, invites | done | — |
| M2 governance: proposals, vocabulary, apply | done | — |
| M2b governance v2: roles, weighted ballots | done | — |
| M2c identity clustering (plan 2.4b) | done | `m2c-identity-clustering` |
| **M3 metadata sharing (consent, exporter, federation)** | **done** | `m3-metadata-sharing` |
| **M4 public hosting: mode, 2FA, library grants** | **server side done, no UI** | — |

## Verified state at this tag

- 39/39 unit packages, 0 failures
- 39/39 integration packages (`-tags integration`), 0 failures
- `go build ./...` and `go vet ./...` clean
- `internal/collab` at 177 top-level tests
- `pkg/auth` at 71 top-level tests, of which 9 are the 2FA *wiring* tests
- `internal/api` at 36, of which 19 cover the wizard's refusals
- `pkg/sqlite` adds 9 2FA store tests, one of which races 20 goroutines
- `internal/collab/mutate_consent.py`: **74 applied, 74 killed, 0 survived,
  0 broken** (collab, `pkg/auth`, `internal/api`)

Counting convention, because the docs previously mixed two and it looked like a
1000-test regression: `go test ... -v | grep -c '^--- PASS'` counts top-level
tests only. Subtest-inclusive is 1481 unit / 2875 integration on the same tree.

## Running the checks

    export GOFLAGS=-mod=mod
    cd ~/code-local/go/stash

    go build ./...
    go vet ./...
    go test ./...                          # unit, 39 packages
    go test -tags integration -count=1 ./...   # integration, 39 packages
    python3 internal/collab/mutate_consent.py # the consent/exporter/federation guards

`GOFLAGS=-mod=mod` is required; without it the module will not resolve.

**The integration tag is not optional.** `TestStashForge_SchemaVersionMatchesAppSchemaVersion`
lives there, and it is the only thing that catches a migration committed without
the `appSchemaVersion` bump — which means the migration is never applied and the
symptom appears in some unrelated test as "no such table".

`go generate ./cmd/stash` regenerates the GraphQL layer. The generated files are
gitignored, so a fresh clone must run it, and it must succeed: a failure there
is invisible to every other check.

## Five things that will bite you

**1. Adding a migration means bumping `appSchemaVersion` in the same commit.**
`pkg/sqlite/database.go`. golang-migrate stops at the recorded version, so a
migration without the bump is silently never applied. The comment above the
variable says this; the integration test enforces it.

**2. Test fixtures create a dedicated instance per fixture.** Never a fixed or
shared row name, and never a fixed id — a fixed id passes exactly once and then
dies in the full suite, because the dev database persists between runs.

**3. A fake proves the logic; only a real database proves the SQL.** This bit
twice in one session. `internal/collab`'s suite passed 100% while the sqlite
adapter did not compile, because the fake had been written to match the interface
instead of the driver. If a type crosses a package boundary, there must be a test
that constructs the real one.

**4. A probe must carry a case it is EXPECTED to succeed.** A sweep probe meant
to disprove "absorb never merges" returned "0 of 80 merged" — agreement with its
own hypothesis, and vacuous, because its fixture helper seeded a 1-D vector that
cosine geometry correctly refuses. If nothing is ever admitted, the probe is
broken, not the system.

**5. Write the outside-construction test first.** Against the real
implementation of whatever the module will be handed. Both times a seam was
missed in this project, the half that existed was thoroughly tested and the
mismatch was invisible because no file imported both packages.

**6. A comment claiming a mechanism is on a path is not evidence that it is.**
The 2FA replay guard shipped broken: `checkSecondFactor` called
`collab.VerifyTOTP` with a nil spent-step set and returned nil, above a comment
saying "the store owns the spend record, so two concurrent logins cannot both be
accepted". `SpendTOTPStep` was never called on that path. The store was correct,
thoroughly tested, and not invoked. Every test before the fix called the session
store's methods directly with a fake verifier, so all of them passed.

Two things caught it, and both are now permanent:

- **Test through the constructor, not the method.** `TestWiring_*` builds via
  `Factory.Build` and logs in. A verifier that is implemented and never attached
  fails there.
- **A replay guard cannot be tested with a fake**, because the guard lives inside
  the component. Those tests drive the real arithmetic and take their *codes*
  from `pquerna/otp` — a second RFC 6238 implementation. Codes from the library
  under test prove only self-consistency; hand-rolled codes are a third
  implementation, which can agree with a broken one.

`internal/collab/mutate_consent.py` now mutates `pkg/auth/totp.go`,
`pkg/auth/session.go` and `internal/api/stashforge_wizard.go` too, so removing
the spend, ignoring the fresh flag, failing open on a store error, dropping the
instance-key check, re-deciding a decided wizard, or accepting an oversized body
each fail a test.

**7. A test table whose entries are all the same value tests one case.** The
wizard's empty-key test had a map with two keys and two values that were both `""`,
so both subtests took the same branch and the `len(want) == 0` guard was executed
by nothing. It looked like three cases and was one. The mutation that flips that
guard survived, which is how it was found.

**8. A size limit on a JSON decoder does not limit the body.**
`json.Decoder` stops at the end of the JSON value, so a small valid object
followed by megabytes of trailing whitespace decodes cleanly and the cap never
fires — measured, a 4105-byte body returned 200 with `MaxBytesReader` "in place".
The second attempt (probe-read after decoding) was wrong the other way: the
decoder buffers ahead, so the probe sees the padding, not EOF, and a body of
`limit-1` was refused. The limit belongs on the READER
(`io.ReadAll(http.MaxBytesReader(...))`), not on the decoded value.

## Where the design decisions are written down

Not in the code alone. Each of these has a comment at the decision, because each
is a place where the obvious implementation is wrong:

- `internal/collab/consent.go` — why absence means opted-in, why a corrupt value
  is an error, why an opted-out user is never re-prompted
- `internal/collab/exporter.go` — why the field list is a whitelist, why
  `BuildPayload` does not check consent, and what the path guard can and cannot
  detect
- `internal/collab/federation.go` — why publish and consume are separate flags,
  why the library id is a parameter to the content check rather than read from
  the payload
- `pkg/sqlite/migrations/101_libraries.up.sql` — why a library is a sharing scope
  and not a file collection

## Not done

**M4 has no UI.** Everything in M4 is server-side and tested; nothing is reachable
from a browser. In order of what a user would notice first:

- **The wizard has no UI.** The gate is now escapable —
  `GET/POST /stashforge/wizard` and `GET /stashforge/mode`, guarded by the
  instance key on the POST — but there is no screen. `curl` works; a browser
  gets JSON.
- **2FA enrolment.** The store, the encryption, the login gate and the replay
  guard all exist and are wired. There is no screen to scan a QR code, and no
  recovery codes. `collab.TOTPURIA` produces the provisioning URI, so a resolver
  is a small addition — but until it exists, an account cannot be enrolled, and
  an account that *is* enrolled (by direct DB write) can only be unenrolled the
  same way.
- **Library grants are not enforced, and cannot be yet.** `LibraryAccessStore`
  grants, revokes and authorises, and the 404-vs-403 distinction is tested. But
  `Decide(ctx, mode, userID, libraryID)` takes a **library id that no content
  carries**: `library_id` appears in `libraries` and `user_library_access` and in
  no target table. `scenes`, `images`, `galleries`, `performers`, `tags`,
  `studios` and `movies` have no library column, and no request context carries a
  user id. So there is no "a private library" for the gate to refuse — the word
  exists in a comment and nowhere else.
  Verified, not inferred:
  `grep -rln 'library_id' pkg/sqlite/migrations/*.sql` returns only 101.
  Migration 101's own rationale claims "every target row hangs off a library" —
  that was aspirational, written before the columns existed, and it is the one
  sentence in the file that is false.
  The fix is a `library_id` column on the seven target tables plus a user id in
  the request context, and a `Decide` call in `imageRoutes.serveImage`
  (`internal/api/routes_image.go:135`) and the scene stream path. That is a
  schema change across every write path for those tables, so it is deliberately
  not started mid-milestone — it wants its own migration (105) and its own
  review, and step 4.3 should not be read as done until it lands.
- **TLS enforcement.** `collab.RequiresTLS` is implemented and tested; nothing at
  the listener level calls it yet.

**M5 and M6 are unstarted.**

The push destination is still unset: the only remote is `upstream` =
`github.com/stashapp/stash`, which is the upstream project. Everything here is
committed locally and unpushed, by instruction.
