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
| M4 public hosting | not started | — |

## Verified state at this tag

- 39/39 unit packages, 0 failures
- 39/39 integration packages (`-tags integration`), 0 failures
- `go build ./...` and `go vet ./...` clean
- `internal/collab` at 137 top-level tests (241 including subtests)
- `internal/collab/mutate_consent.py`: **22 applied, 22 killed, 0 survived,
  0 broken**

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

M4 (public hosting) is the next milestone and is unstarted. The M3 pieces M4 will
build on — `PeerRegistry`, `CommonsRead`, `PayloadSink` — are interfaces with no
sqlite implementation yet, so wiring them is real work rather than a lookup.

The push destination is still unset: the only remote is `upstream` =
`github.com/stashapp/stash`, which is the upstream project. Everything here is
committed locally and unpushed.
