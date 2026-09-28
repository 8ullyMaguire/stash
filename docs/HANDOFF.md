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
| **M4 public hosting: mode, 2FA, library grants** | **done** | `m4-public-hosting` |

## Verified state at this tag

- 39/39 unit packages, 0 failures
- 39/39 integration packages (`-tags integration`), 0 failures
- `go build ./...` and `go vet ./...` clean
- `internal/collab` at 177 top-level tests
- `pkg/auth` at 71 top-level tests, of which 9 are the 2FA *wiring* tests
- `internal/api` at 36, of which 19 cover the wizard's refusals
- `pkg/sqlite` adds 9 2FA store tests, one of which races 20 goroutines
- `internal/collab/mutate_consent.py`: **79 fixtures**, spanning collab,
  `pkg/auth`, the wizard handler, and the startup posture gate in `server.go`.
  One `MUTATION_TARGETS` list drives the preflight, the runner and the restore
  guard, and `main()` fails if the count the preflight promised is not the count
  that ran — a mutation group wired into two of three lists used to vanish with
  a clean-looking summary.

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

**8. Test a function at the seam it is connected by, not the one you can reach.**
The mutation "the scheme is assumed to be https" survived, because the scheme was
derived inline in `Start()` and every posture test passed a scheme in as an
*argument* — nothing could see where the argument came from. Extracting
`Server.scheme()` so the derivation is its own testable unit is what closed it.
The same shape as the typed-nil trap below.

**9. A nil `*T` in an `interface` is not a nil interface.** `checkInstancePosture`
took an `instancePostureStore` and began `if store == nil`. The caller passes
`s.manager.InstanceModeStore`, a `*sqlite.InstanceModeStore`: when that is nil the
interface is **non-nil holding a nil pointer**, so the check never fired and the
function called `Mode()` on a nil receiver — a panic at boot for every
non-StashForge deployment. The nil test has to be against the concrete pointer,
at the caller.

**10. A size limit on a JSON decoder does not limit the body.**
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
- **Library grants are now ENFORCED.** This was the long-standing blocker and
  it is closed. `library_id` is on all seven target tables (migration 105), a
  user id reaches the request context (`withRequestUserID`), and `allowMedia`
  gates every media route from the per-target `*Ctx` middlewares before any file
  is opened. `internal/api/mutate_media_gate.py` mutation-checks it.
  **Two things to know before changing it:**
  - The seventh target table is `groups`, not `movies` — migration 65 renamed
    it, and the plan, GOAL.md and migration 101's comment all still said
    `movies`. The first version of 105 failed with `no such table: movies`.
  - **A row with no library resolves to the DEFAULT library, not to
    "unrestricted".** Every newly-scanned row has `library_id` NULL, so
    refusing NULL outright would 404 the owner's own new files, and the fix
    shipped under that pressure is "make NULL mean allow". The default library
    is owned by the owner, who bypasses by ownership; everyone else still needs
    a grant row.
  - **A signed-URL request has no user id** and is therefore refused on a public
    instance. Deliberate: a device that cannot send a cookie cannot send a grant.
- **M4 IS COMPLETE.** The wizard screen exists at `/stashforge/wizard`
  (`ui/v2.5/src/components/Setup/StashForgeWizard.tsx`) — a route **separate
  from the upstream `/setup`**, which is the paths-and-credentials wizard.
  Sharing one screen would drop an operator re-running configuration into the
  sharing decision, which the server refuses with `wizard_already_completed`.
  It calls the HTTP endpoint, not GraphQL, because there is no session to send.
  **The mode is chosen once; changing a live mode is a separate authenticated
  operation that deliberately does not exist yet.** The only thing M4 does not
  have is a browser test, and `ui/v2.5` still has no test runner — the security
  property never lived in the client.
- ~~**GraphQL for 2FA, libraries, grants, consent.**~~ **Done** —
  `graphql/schema/types/hosting.graphql`,
  `internal/api/resolver_mutation_hosting.go`, `internal/api/models_hosting.go`,
  `internal/collab/library.go`, `pkg/sqlite/stashforge_libraries.go`.
  **Three things to know before changing any of it:**
  - **`collab.Library` is a DOMAIN type, not a row struct, because `is_private`
    is a three-state column and a Go `bool` cannot hold the third.** A library
    that defers to its owner's consent is not the same as one marked
    not-private, and gqlgen maps a nil pointer onto a non-null `Boolean` by
    returning `false` — which publishes a library nobody chose to publish.
  - **Libraries are per-user, not instance-wide** (101: `user_id` NOT NULL,
    names unique per owner). The first version of the schema described
    instance-wide libraries, which is a different data model wearing the same
    names.
  - **A 2FA mutation takes a code, never a user id.** `disableTOTP(userId:)` would
    be a mutation any account could point at the owner.
- **The mode is still chosen over HTTP, not GraphQL.** `POST /stashforge/wizard`
  is a one-time, instance-key-gated decision and refuses a second POST on purpose;
  changing a live mode afterwards needs a separate authenticated path, and
  deliberately does not exist yet rather than riding on the wizard endpoint.

**Four prose-versus-schema mismatches so far, all the same shape** — a COMMENT
states a constraint, the DDL does not create it, and the comment is believed:

1. The plan and GOAL.md list `movies` among the seven target tables. It is
   `groups`; migration 65 renamed it.
2. 101 says `UNIQUE per (owner, name)` and creates no such index. **Migration
   106** adds it — a user could previously create "Main" three times.
3. 105's partial unique index was on `(is_default)` alone — a UNIQUE constraint on
   a **constant**, so it enforced *one default on the whole instance*. The second
   user to create a library got a violation and **no default at all**, so every
   one of their unscanned rows resolved to "no library, no owner" and the media
   gate refused it. Now `(user_id, is_default)`.
4. 105's comment said NULL resolves to the refusal; the code resolves it to the
   default library. The code is right — NULL is the scanner's normal output, and
   refusing it 404s the owner's own new files.

**The rule: a comment in a migration is a claim about the schema, and the only
way to know whether it holds is to read the DDL in the same file.** Be suspicious
of a partial unique index whose `WHERE` clause is itself the constraint.

**`TestStashForgeStoreConstructorsAreActuallyWired`** now guards the fourth
"referenced != used": every `New*Store` in `pkg/sqlite` must be called from a
non-test file. It caught `sqlite.ConsentStore` — fully implemented, fully tested,
and built by nothing, so `setConsent` was dead code. It checks 30 constructors and
is mutation-checked. **If you add a store, wire it in `internal/manager/init.go`
after `Database.Open` and the test will tell you if you did not.**
- ~~**TLS enforcement.** Nothing calls it yet.~~ **Done** — `Server.Start`
  refuses to boot when the mode forbids the scheme
  (`internal/api/server.go`, `checkInstancePosture`). A public instance over
  plain HTTP no longer starts, and a store that cannot be read stops the boot
  rather than defaulting to a posture nobody chose.

**M5 is in progress: steps 5.0 and 5.0a are done.**

The downloader is at `plugins/p2pdownloader/`, its own module, and the seam is
proved by four tests in `internal/api/stashforge_p2p_seam_test.go` rather than
by a grep — because a package inside the core tree that imports nothing from
the core satisfies a grep trivially, so the original check would have been
green on something that cannot load and never runs.

**`go tool nm` does not work for "is this in the binary", and two other
mechanisms did not either.** It passed on a core that HAD the downloader linked
in: 113,767 symbols, none of them the plugin's. An uncalled function is
dead-code-eliminated, so `nm` cannot see it; an equally-unreachable string
constant is dropped by the compiler before the linker runs — measured on a
107 MB binary that did contain the downloader, where the marker was absent and
the module path was present. What survives reachability analysis is the module
path, in the binary's pclntab name table, so the test reads **bytes**.

**Three mutation harnesses, and the bugs they found in the harnesses
themselves:**

- `mutate_seam.py` (repo root) — 6/6 killed, and the bundled case is caught by
  the seam assertion at `seam_test.go:227`, not by the build guard behind it.
- `internal/collab/mutate_locator.py` — 14/14 killed.
- `plugins/p2pdownloader/mutate_rpc.py` — 23/23 killed.

Three classes of harness bug that each produced a **false green or a false
result**, and all three are worth checking for in any new one:

1. A mutation that **does not compile** is scored `broken`, not `killed`. Four
   of the locator mutations were `broken` on the first run — deleting a map key
   leaves a syntax error — and a harness that counted them as kills would have
   reported 14/14 while testing 10.
2. A **tautological mutation** is a no-op, so the test passes and the mutation
   is reported as a survivor. Two of the consent mutations were
   `x == nil || x != nil`, which is always true. Rewritten to actually change
   behaviour, both kill.
3. `mutate_seam.py` used `dirname(__file__)` as the repo root, so with the
   harness in `internal/api` it appended a `require` to `internal/api/go.mod` —
   **creating a nested module** — and built a second copy of the plugin. Its own
   "the core still builds" check caught it. A harness that writes into the repo
   must know exactly where the repo is.
4. `mutate_seam.py` restored with `git checkout`, which **cannot restore an
   untracked file** — and every file the seam mutations touch is untracked,
   because the plugin is work in progress. So it left `interface: raw` in the
   manifest and core's module path in the plugin's `go.mod`, and reported a clean
   tree while doing it. The go.mod one made the plugin unbuildable and read as a
   plugin bug. It now snapshots before mutating and **compares byte for byte
   afterwards**; a restore that cannot fail is not a restore.
5. `mutate_rpc.py` unpacked mutations with `entry[:4]`, which shifts every field
   left by one for the multi-edit form and put the file tag in the `expect` slot.
   The symptom was `unscored` with a message naming missing source text, which
   pointed at the code instead of at the harness. It now unpacks by shape.

**Two verdicts beyond killed/survived, both added after they were needed:**
`broken` (does not compile — NOT a kill) and `no-op` (changes no bytes — a
harness bug reported as a survivor). And some defects need **several edits** to
exist at all: dropping `file:` from the plugin's named refusals changes nothing
because the value is still refused as an unknown scheme, and accepting it in the
URL switch changes nothing because the prefix check fires first. Reported as two
mutations, both survive and both are reported as holes that are not holes.

**The consent gate is two problems, and the second is invisible from core.**
Core's half is `internal/collab/locator.go`; the plugin's is
`plugins/p2pdownloader/internal/rpc/consent.go`. The plugin holds the magnet in
memory whether or not core grants, so a refusal that does not end the transfer
is §7.1's failure with the gate working perfectly. Invariant: **no transfer
without a granted proposal for that exact locator**, and every way of not
having a grant refuses — nil answer, unreachable core, or a plugin with no gate
at all.

**M5 steps 5.1 and 5.2 are done. Step 5.3 has its seeding decision AND its
storage gate. 5.3's transfer surface and 5.4–5.5 remain**: BitTorrent
transfers, ed2k, library integration. **M6 is unstarted.**

### Seeding is derived from the tier, because uploading is not fetching

`docs/decisions/0002-seeding-policy.md`. `anacrolix/torrent` uploads
opportunistically by default — its own comment says so — and a permissive
default in a client pointed at a corpus of untracked, self-published material
means the box publishes strangers' work with nobody having decided it should.
Once the chunks are out, no later decision retracts them.

So §7.1 needed a third distinction. Storing a locator is writing it. Acting is
starting a transfer. **Seeding is a write to a library the operator never sees**,
and it is permitted only where a tier carries an assertion covering it:
`self_published` / `performer_claimed` / `third_party_permitted` may;
`unverified` may not; so may `quarantined`, `denied`, and **anything the build
does not recognise**.

`unverified` is where every object *starts*, so it is the common case — which is
what makes "nobody has objected" versus "somebody permitted this" the decision
that matters. And every permissive tier is a claim by an **identified** party,
so a value nobody can be identified for is not one.

`OperatorAllowedSeed` is an **outer bound, never an override**: it can narrow, it
cannot widen, and `TestTheOperatorCannotWidenThePolicy` pins that because it is
the direction a settings screen invites.

**The tier strings are duplicated** across the seam and a stale copy fails safe
and silent — every decision falls to the restrictive branch, seeding stops
everywhere, nothing errors. The drift test reads **both** files, both from
source: the first version listed this file's six by hand and compared against a
literal, and two mutations survived it. A test checking a hand-written copy of
what it is checking is the same mistake one level down.

### `anacrolix/torrent` v1.61.0 is adopted, and its path-safety function is not

`docs/decisions/0001-torrent-library.md`. DHT, BEP 47 v2 / BEP 9 magnet metadata
and rate limiting are all covered. Super-seeding and sparse are **not** — no
option for either exists — and neither is needed for a corpus of untracked,
self-published material.

**`storage.ToSafeFilePath` is documented as "ensuring the result won't escape
into parent directories" and is a string check.** 29 lines, and it tests whether
the *first* component of the joined path is `..`.

**Correction, 2026-09-28.** I previously wrote that `/etc/passwd` and
`..\..\windows` were reachable escapes, on the evidence that the function
returns them with a nil error. They are not. `filepath.Join(location, safeName)`
cleans the *concatenation*, so an absolute name is re-anchored under the
location and lands inside. Measured:

| Name | function | lands at | inside? |
|---|---|---|---|
| `["..","..","etc","passwd"]` | refused | — | safe |
| `["/etc/passwd"]` | accepted | `<root>/etc/passwd` | **safe** — `Join` re-anchors |
| `["..\..\windows"]` | accepted | `<root>/..\..\windows` | safe, one odd filename |

I asserted that from the function's return value without checking the caller's
join. The lesson is in the skill under "a documented guarantee is a claim".

**The escape that IS real is the symlink, and the library's own containment
check cannot see it.** `file-client.go:92` looks like a defence and
`isSubFilepath` looks like the check behind it, but `isSubFilepath` is
`filepath.Rel` plus `HasPrefix`, and `Rel` is pure string arithmetic.
Executed with the library's own functions, torrent directory a symlink:

```
file path     : <root>/downloads/innocent/passwd    (innocent -> outside)
isSubFilepath -> true
really is     : <root>/outside       (EvalSymlinks)
the decoy OUTSIDE the download root now reads: "PEERS CONTROLLED BYTES"
```

So `internal/paths.SanitizeJoin` is not a parallel implementation — it is the
only path safety this downloader has, and it must be enforced at *our* storage
layer because the library's cannot be. `internal/paths` runs `EvalSymlinks` on
both sides and compares with `filepath.Rel`, which is the filesystem-resolving
version of the check the library intended to write.

The **mmap** storage is worse: `grep -c isSubFilepath` is 0 for `mmap.go` and 1
for `file-client.go`, and it carries the TODO *"Support all the same native
filepath configuration that NewFileOpts provides"*. It also prepends
`BestName()` unconditionally where the classic path skips it for a nameless
(BEP 52 v2) torrent, so the first component `ToSafeFilePath` inspects is the
file's own. **This settles which storage implementation step 5.3 must use.**

The dependency also drags cgo sqlite in through the storage backends, which is
part of why the plugin ships as its own static binary. It must appear in the
plugin's `go.mod` and **not** in the core's.

### `internal/paths` — three findings the plan's seven cases did not cover

`..\..\windows` (the same attack, other separator), the Windows reserved names,
and **a symlink in a subdirectory** — the first-component case is obvious enough
that a check written for it looks complete.

`EvalSymlinks` on both sides, walking up to the first existing component: a
downloader resolves names for files that are not there, so resolving the whole
path fails constantly; and a *resolved* root against an *unresolved* child
rejects every legitimate file on macOS, where `/tmp` is a symlink.

Containment is `filepath.Rel`, never a prefix — `/data/downloads-evil` starts
with `/data/downloads`. And `Rel` has a subtlety worth knowing: a component
merely *beginning* with dots is not a traversal, so `HasPrefix(rel, "..")`
wrongly rejects `..leading.dots`. The legitimate-names test found that in the
test helper before it could reach the production check.

**`EnsureRoot` is asserted on the FILE still existing, not on the error.** A
plausible "fix" for "accepted a regular file" is `os.RemoveAll` then
`MkdirAll` — it compiles, returns nil for every input, and deletes a user's
file. That mutation is killed by
`TestEnsureRootNeverDestroysWhatIsAlreadyThere`.

### `internal/storage.Gate` — the library's only safe storage has no defence

The library needs a storage implementation whether or not the transfer code
exists yet, and *which* one is a decision:

| backend | containment check | verdict |
|---|---|---|
| `storage.NewFile` (classic) | `isSubFilepath` at `file-client.go:92` — a **string** check | unusable alone |
| `storage.NewMMap` | **none** (`grep -c isSubFilepath` → 0) | unusable |
| **`internal/storage.Gate`** | every raw component, then `paths.SanitizeJoin`, then the library's | **used** |

`Gate` wraps the classic backend and validates every file **before** handing the
torrent to the library, because the library's only extension points —
`FilePathMaker` and `TorrentDirFilePathMaker` — both return a bare `string` and
**cannot report an error**. A per-file refusal has nowhere to go, so it becomes a
sentinel filename and the transfer completes with the wrong file in it. Refusing
the whole torrent in `OpenTorrent` is the only place a real error can be
returned, and it is the only place one is used.

**The bug the tests caught while writing it, which is the whole reason to read
this.** The obvious up-front check validates the file's name as one string:

```go
name := filepath.Join(append([]string{info.BestName()}, file.BestPath()...)...)
paths.SanitizeJoin(root, name)   // <-- the traversal is already gone
```

`filepath.Join` **cleans** its result, so `["sub", "..", "..", "escape"]`
becomes the string `"escape"` before the gate sees it. `SanitizeJoin` is not
wrong — it is handed a name that no longer contains the attack. The gate
therefore checks each component **as the torrent supplied it** and never
pre-joins; the joined name is only an output, for the error message.

This is also why the library's `ToSafeFilePath` looks adequate and is not: it
joins first and checks the first component of the **result**, which is a
different question from "does any component of the input walk out".

The same trap bit a *test* later: a case using `{"..", "escape"}` under a
torrent named `torrent` becomes `torrent/../escape` → `escape`, which is inside
the root and correctly accepted — so the test passed for the wrong reason. Two
levels of `..` are needed: one to leave the torrent directory, one to leave the
root.

A second real bug the same tests caught: the backstop returned a bare
`.refused-by-stashforge` filename from a function whose sibling returns an
**absolute** path. The library joins it onto a base directory so it is harmless
in use, but `FilePathMaker` is a public extension point, and a relative filename
resolves against the *working directory* in any caller that uses it directly.

### A layered defence needs a `covered` verdict, or the harness lies to you

The gate has **three layers that all refuse a `..` walk** — per-component,
joined-name, and the torrent-directory check. So "remove layer 1" changes nothing
a test can see: layers 2 and 3 refuse the same input. That is **not a hole**,
and treating a surviving row as a defect sends you into the code to fix
something that is not broken.

So the harness has six verdicts, and the fifth is the one that matters:

```
killed    the named test failed
covered   no test noticed AND the whole suite still passed -- another layer
          refuses this input. Verified by re-running everything with the
          mutation applied, not assumed.
survived  the whole suite FAILED but the named test did not. A HOLE.
```

`covered` is not a pass; it is a *claim*, and the whole-suite re-run is what
makes it trustworthy. Only `survived` means a hole.

Two other things the harness needed: **compound mutations** (`old`/`new` as
equal-length lists, because disabling one layer is unobservable — and a single
edit labelled "COMPOUND" is a lie no harness can detect, which is why five rows
survived the first run), and **a timeout on every `run`** (a mutation that
removed a lock's `defer Unlock` deadlocked `go test` and hung the session for
five minutes).

And when a `survived` row is real: **apply the mutation, run `-v`, read the FAIL
lines**, and retarget. Five consecutive rows were the same mis-pointing — I had
named a test that passed while a different one caught the mutation.

### A test that asserts on your own decision cannot catch a broken wiring

The `internal/torrent` package exists to bind three decisions together: the
consent policy says whether a torrent may seed, the storage gate says where bytes
land, and `anacrolix/torrent` uploads opportunistically by default. Its first
test file had fourteen tests, all green, all asserting that the reported
`Decision` agreed with the policy.

I mutated the implementation to `spec.DisallowDataUpload = false` -- always
allow upload, the exact bug the package exists to prevent -- and **every one of
them still passed.** `Decision.UploadAllowed` is computed *from* the policy, so
asserting the two agree compares a value with its own source. The reported
decision was perfectly consistent with the policy and the client was being told
the opposite.

The fix is a read-back: `AppliedSpec` records what the client was *given*, which
`Decision` structurally cannot see. The first version of that also passed for a
second reason, and the reason generalises:

- `spec.Storage = nil` (bypassing the gate) also passed. Because
  `DefaultStorage` is the gate, the library called the gate anyway and the gate
  refused. The bytes were safe, the config was wrong, and nothing noticed.
- Ignoring the gate's error entirely also passed, for the same reason — a lower
  layer refused the identical input.

That is defence in depth, and it is worth having. It is **not** evidence the
wiring is tested, and a harness that reports those as kills is lying. They are
`covered`, and they are only `covered` if a whole-suite re-run passes with the
mutation applied.

**The rule:** a test whose subject is a *wiring* must observe the far side of the
wiring. A test that reads back your own return value is a test of your return
value.

### A sentinel you already have can hide a distinction you need

The gate refuses the same input in two places, and both wrapped
`storage.ErrRefused`:

- the up-front check, before the client is told the torrent exists
- the library's call to `OpenTorrent` during `AddTorrentSpec`

`errors.Is(err, storage.ErrRefused)` is true for both, so the test could not tell
them apart, and the only observable difference was the error **message**. The
first version of the test asserted on that message, which is a test that stops
testing the thing the moment someone improves the wording.

`ErrRefusedUpFront` now wraps `ErrRefused` and adds the distinction that matters:
whether the client ever held the torrent. The up-front refusal means nothing was
announced and nothing is cached; the later one means cleanup. A caller acts
differently on each, and a sentinel is the only way to say so.

The general shape: **`errors.Is` on a shared sentinel is a category, not an
outcome.** When one error can arise in two places with different consequences, it
is two errors.

### `x/time/rate` nil is not a library's "unlimited" idiom everywhere

`NewDefaultClientConfig` sets `UploadRateLimiter` to an *unlimited* limiter, so
"leave it nil" was never available — and setting it to nil would have **panicked**:
`config.go:278` calls `cfg.UploadRateLimiter.Burst()` with no nil check, on every
`NewClient`. The download side does handle nil explicitly
(`EffectiveDownloadRateLimit`), which is what makes the asymmetry easy to assume
away.

My test asserted the upload limiter was nil, on the reasoning that a client-wide
upload limit is a permission-shaped knob. The *reasoning* was right and the
*assertion* was wrong: it failed, and the fix was `rate.Inf`, not nil.

### There is no `Client.Listen`, and I documented one for two commits

`internal/torrent`'s comments said reachability was "deferred to `Listen`" —
that a downloader that has not been told to listen cannot announce itself, and
that UPnP belongs "at `Listen`" rather than in the config. **There is no
`Listen` method in `anacrolix/torrent` v1.61.0.** The sockets are created inside
`NewClient` (`client.go:385-420`) and the port forwarder is started there too:

```go
if !cfg.NoDefaultPortForwarding {
    go cl.forwardPort()
}
```

So reachability is decided **entirely by the config**, and the design was right
while the stated mechanism was invented. That is the failure mode this project's
notes keep hitting, in a new costume: I had `go doc`-ed `Client` and read the
`Listeners()` method next to it, and read a `Listen` into the gap.

Measured, with the real client:

| DHT | TCP | uTP | listeners |
|---|---|---|---|
| off | off | off | **0** |
| **on** | off | off | **0** |
| on | on | off | 2 (`0.0.0.0:42069`, `[::]:42069`) |
| on | on | on | 4 |

The middle row is the one worth keeping. A live DHT with both transports off binds
**nothing**: the box is findable by peers and cannot serve them. It advertises
interest, earns leech credit it cannot return, and disappoints everyone it
attracts — strictly worse than never having joined. So the DHT is off with the
transports, and the comment records the measurement so the "harmless" reading has
something to check against.

### A test that asserts a property of the DEPENDENCY is a tautology

The first version of the DHT test built a DHT-only client and asserted
`len(Listeners()) == 0`. It passed — and passed because of how the *library*
behaves, not because of anything this package does. Turning the DHT on in
`ConfigFor` left it green.

This is the "asserting your own return value" trap from the last commit, one level
up: the subject was the dependency, so no mutation to my code could ever fail it.
The fix was to assert `cfg.NoDHT` — a field this package sets — and keep the
listener measurement in a *comment* as the reason the field matters.

The general rule: **name the layer the assertion is about, and if a mutation to
your own code cannot change the result, the assertion is about something else.**
The socket test (`TestTheClientBindsNoSockets`) passes this bar — enabling TCP or
uTP in either `ConfigFor` or `New` makes it fail, and both sites are in the
harness.

### The library's `AddMagnet` is a bare path to the client, and the fix is a grep

`Client.AddMagnet` is four lines:

```go
func (cl *Client) AddMagnet(uri string) (T *Torrent, err error) {
    spec, err := TorrentSpecFromMagnetUri(uri)
    if err != nil { return }
    T, _, err = cl.AddTorrentSpec(spec)
    return
}
```

No policy, no gate, no metainfo check, no upload control. Anything added that way
is a torrent this code has never heard of, from a string a stranger put in a
database. So the magnet path lives here, and
`TestAddMagnetOnTheLibraryIsNotReachableFromHere` greps the package's own
non-test source for `.AddMagnet(` so it cannot be reintroduced by accident.

**The needle is `.AddMagnet(`, not `AddMagnet(`.** The bare name matches this
package's own declaration — the safe path the file exists to provide — so the
first version of the guard failed on its own remedy. A check that fails the
moment you write the thing it asks for is a check that gets deleted.

### A magnet cannot be gated, and saying so is the design

A magnet carries an infohash, a display name and trackers. No `files`, no
`length` — those arrive by BEP 9 from whichever peer answers first. So
`checkMetainfo` has nothing to check, the gate has nothing to resolve, and a
"check" on that input would be a check that always passes.

The gap is a real window in which the library holds a torrent nobody has
examined. It is acceptable for exactly one reason: **`OnMetadata` runs the full
sequence again on arrival and DROPS the torrent if the names escape the root.**
`Decision.Gated` and `GatedAfterMetadata` exist so a caller can tell which side
of that window it is looking at, and `Gated` is false for a magnet without
exception.

Dropping rather than marking is deliberate: a client that still holds a torrent
it cannot open keeps announcing it on the DHT, and a later `AddTorrent*` for the
same infohash succeeds from its own cache.

**The attack, and getting the fixture wrong hides it.** A magnet for *benign*
metadata followed by *hostile* metadata is not an attack — the hashes differ, so
the library rejects the mismatch. The dangerous case is a magnet whose infohash
**is** the hash of metadata naming an escaping path: the locator looks fine at
every point where a magnet can be checked, and the data is hostile when it lands.

### Two bugs a test that reads the wrong key cannot see

**The drop assertion was vacuous.** It looked the torrent up with
`client.Torrent(hash)` — and the fixture built the magnet from benign metadata,
so the magnet's infohash and the hostile metadata's were different, the lookup
missed, and "not found" looked identical to "dropped". It passed with `d.drop`
deleted. Asserted on `len(client.Torrents())` instead, which cannot be wrong that
way: 1 before the arrival, 0 after.

**The zero-infohash check was never reached.** Every URI in the test table was
rejected by `ParseMagnetUri` *itself* — "missing v1 infohash", "unexpected
scheme", "unhandled xt parameter encoding" — so the mutation disabling my
`IsZero` branch survived. The case that reaches it is
`magnet:?xt=urn:btih:0000…0000`: a 40-hex all-zero hash is well formed, the
parser accepts it, `AddTorrentSpec` does not object, and the client ends up
holding a torrent identified by nothing.

General form: **when a test's URIs are all rejected upstream, the test is
measuring the upstream.** Find the one input that reaches your branch.

### The same tautology, written twice

`AppliedSpec` exists because `Decision` reports intent and not what the client
was given. On the magnet path I wrote the record as:

```go
DisallowDataUpload: !upload,     // recomputed from the policy variable
```

which is the identical tautology in a second place — a mutation setting
`spec.DisallowDataUpload = false` leaves that line untouched, so the record still
agreed with the policy and the test passed on a torrent the client was being
told to upload. Now `spec.DisallowDataUpload`, read back off the spec.

Six of thirteen magnet-path mutations survived the first run. Every one was a
missing read-back or a test that observed the wrong thing, and all six are now
killed. The lesson generalises past this package: **a second path through the
same decision needs its own read-back**, because copying the first path's
structure copies its observability too — and its gaps.

### A build-error detector that is missing one form reports its own defect as a hole

The last survivor in the magnet run was scored `SURVIVED — a different test
failed`. The mutation was:

```go
-   spec, err := libtorrent.TorrentSpecFromMagnetUri(uri)
-   if err != nil {
+   var spec *libtorrent.TorrentSpec
+   var err error
+   if false {
```

which does not compile: `err redeclared in this block`. My `BUILD_ERRORS` list
had `build failed`, `cannot use`, `undefined:` and several others, but not
**`redeclared`** — so a probe's own defect was reported as a hole in the tests,
which is the exact inversion the `SKIP` verdict exists to prevent. The harness was
confidently wrong about its own instrument.

**The build-error list has to be complete, not representative.** A missing entry
does not produce a wrong verdict on one row; it produces a *false hole* that sends
the next person into the tests. Extended to twelve forms, and the row rewritten
to compile (`spec, _ := ...` with the branch dead), where it correctly reports
`covered` — the spec builder's own error is unreachable for a URI my upstream
`ParseMagnetUri` check did not already refuse.

### A real bug the tier test found

`AddMagnet` never called `remember`, so `tierOf` returned `""` for every magnet
and `OnMetadata` decided every arriving torrent as an unrecognised tier.
Restrictive, so nothing was ever published and **the downloader was inert rather
than broken** — no error, no log line, every test green. The association is now
made at add time on both paths; it is the second time it has been missing from
one of them.

### Nine mutation harnesses

```bash
python3 mutate_seam.py                                  # 6
python3 internal/collab/mutate_locator.py               # 14
(cd plugins/p2pdownloader && python3 internal/paths/mutate_paths.py)   # 12
(cd plugins/p2pdownloader && python3 internal/policy/mutate_policy.py) # 14
(cd plugins/p2pdownloader && python3 internal/storage/mutate_gate.py)  # 21
(cd plugins/p2pdownloader && python3 mutate_rpc.py)                    # 23
```

228 mutations across nine harnesses, 0 survivors. The earlier figure said
"seven harnesses" and omitted `mutate_seam.py`, `mutate_consent.py` and
`mutate_media_gate.py` — the plugin harnesses were being counted as the whole
set, which is the same scope error as the source-scanning test that walked
`..` from `internal/api`.

Run them **serially**. They edit real files and restore them.

The push destination is still unset: the only remote is `upstream` =
`github.com/stashapp/stash`, which is the upstream project. Everything here is
committed locally and unpushed, by instruction.
