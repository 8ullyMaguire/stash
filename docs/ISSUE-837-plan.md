# PLAN — stash#837: the issues log

Implements `docs/ISSUE-837-spec.md`. Every step carries the exact command and
its expected output, so this plan can be followed by an LLM that has never seen
the repository.

**Branch:** `main` (the soft fork). Base upstream `stashapp/stash` `develop`.
**Prerequisite:** generate the binary before building or testing —
`make generate` then `go build ./...` (see `docs/GOAL-UPSTREAM.md` §"Before you
touch anything: the two build facts").

**Ground rules that are not optional here, because each one was paid for:**
- A gate that cannot be MADE TO FAIL is decoration. Every test below gets a
  mutation run before the issue is called done.
- Pin against golden bytes from the FORMAT, never this implementation's own output.
- Assert the value the defect corrupts, not an aggregate that happens to agree.

---

## Step 1 — migration

**File:** `pkg/sqlite/migrations/`

Add the next migration in sequence. Find the highest-numbered file first:

```sh
ls pkg/sqlite/migrations/ | sort -V | tail -3
```

Name it `<next>.sql` and put it there. The body:

```sql
CREATE TABLE issues (
    id          integer PRIMARY KEY AUTOINCREMENT,
    file_id     integer REFERENCES files(id) ON DELETE CASCADE,
    domain      text    NOT NULL,
    kind        text    NOT NULL,
    details     text    NOT NULL DEFAULT '',
    detected_at timestamp NOT NULL,
    resolved    boolean NOT NULL DEFAULT false,
    resolved_at timestamp
);

-- An issue is a FACT ABOUT THE LIBRARY, not an event. Re-finding the same
-- duplicate on every scan must not add a row, or the table is a log and the
-- UI becomes a log viewer -- which is the thing the issue complains about.
-- `resolved` is in the key so a dismissal sticks across later scans, while a
-- NEW finding of the same kind on the same file is recorded afresh.
CREATE UNIQUE INDEX idx_issues_unique_live
    ON issues (file_id, domain, kind, resolved);

-- The panel's default query: unresolved, newest first.
CREATE INDEX idx_issues_unresolved
    ON issues (resolved, detected_at DESC);
```

`file_id` is `ON DELETE CASCADE` on purpose: an issue about a file that no longer
exists is not an issue any more, and a dangling row would make the panel's file
name render as empty.

**Verify:**
```sh
go test -tags integration ./pkg/sqlite/ -run TestIssueTable -v
```
Expected: `ok  github.com/stashapp/stash/pkg/sqlite` — and the test must assert
the unique index EXISTS, not merely that the table exists. Reading
`sqlite_master` proves a constraint is present, not that it fires; the
idempotence test in step 3 is what proves it fires.

---

## Step 2 — model

**File:** `pkg/models/model_issue.go`

```go
package models

import "time"

// IssueDomain values. See spec §4.
const (
	IssueDomainFile  = "file"
	IssueDomainScan  = "scan"
	IssueDomainMeta  = "metadata"
)

// Issue kinds within IssueDomainFile. See spec §4.
const (
	IssueKindDuplicate     = "duplicate"
	IssueKindZeroDuration  = "zero_duration"
	IssueKindZeroSize      = "zero_size"
	IssueKindNoFiles       = "no_files"
)

type Issue struct {
	ID          int        `json:"id"`
	FileID      *FileID    `json:"file_id"`
	Domain      string     `json:"domain"`
	Kind        string     `json:"kind"`
	Details     string     `json:"details"`
	DetectedAt  time.Time  `json:"detected_at"`
	Resolved    bool       `json:"resolved"`
	ResolvedAt  *time.Time `json:"resolved_at"`
}

// IssueFilterType. Resolved defaults to UNRESOLVED at the store, not here: a
// caller that forgets must not silently get six months of dismissed rows.
type IssueFilterType struct {
	Domain   *string
	Kind     *string
	Resolved *bool
}
```

**Verify:**
```sh
go build ./... && go vet ./pkg/models/
```
Expected: no output.

---

## Step 3 — store: idempotence is the whole point

**File:** `pkg/sqlite/issue.go`

Two operations, and the second is the one with the subtlety.

```go
// Record inserts an issue, or refreshes detected_at if the identical finding is
// already live. Returns the row's id either way.
//
// The ON CONFLICT clause names the SAME column list as the unique index. If
// they ever diverge the statement fails at runtime with "ON CONFLICT clause
// does not match any PRIMARY KEY or UNIQUE constraint" -- which is the correct
// outcome, and why the list is written as a named constant rather than inline
// twice.
func (qb *IssueStore) Record(ctx context.Context, issue *models.Issue) error
```

```go
// Resolve dismisses an issue. IDEMPOTENT: resolving an already-resolved issue
// succeeds and leaves resolved_at alone, so a double-click in the UI does not
// rewrite the timestamp the user is shown as "found on".
func (qb *IssueStore) Resolve(ctx context.Context, id int) error
```

For `Resolve`, the update must be guarded:

```sql
UPDATE issues SET resolved = true, resolved_at = ?
WHERE id = ? AND resolved = false
```

The `AND resolved = false` is what makes it idempotent. Without it the second
call rewrites `resolved_at` and the UI's "dismissed on" date drifts every time
somebody re-clicks.

**Tests, `pkg/sqlite/issue_test.go`:**

| Test | Asserts |
|---|---|
| `TestIssueIsIdempotent` | recording the same (`file_id`, `domain`, `kind`, `resolved`) twice yields ONE row, and `detected_at` advanced |
| `TestDismissedIssueIsNotReraised` | record → resolve → record again → still one row, still resolved, `resolved_at` unchanged |
| `TestResolveIsIdempotent` | resolve twice → `resolved_at` identical after the second call |
| `TestANewFindingOfTheSameKindIsRecorded` | after a dismissal, a genuinely new finding (different `details`) is a NEW row — the spec's §3 promise |

Each of these must FAIL when the corresponding line of the store is mutated.

**Verify:**
```sh
go test -tags integration ./pkg/sqlite/ -run 'TestIssue|TestDismissed|TestResolve|TestANewFinding' -v
```
Expected: all PASS.

---

## Step 4 — detection, inside the scan

**File:** `pkg/sqlite/file_scan.go` (wherever the file row is written) and the
detectors in a new `pkg/sqlite/issue_detect.go`.

**Rule from spec §4: detection runs where the file row is written, and is NOT a
separate sweep.** A sweep re-stats every file — the expensive thing the scan just
avoided.

```go
// detectFileIssues records what is wrong with a file that has just been
// written. It never returns an error: a failure to LOG a problem must not fail
// the scan that found the problem. A caller that wants to know must check the
// issues table.
//
// This is why the signature has no error. Getting it wrong the other way —
// propagating — turns a zero-byte file into a failed import.
func detectFileIssues(ctx context.Context, f *models.File) 
```

Detectors, each with its own test:

| kind | check | test |
|---|---|---|
| `duplicate` | another file shares this checksum | `TestDetectDuplicate` — and its negative: two files with DIFFERENT checksums raise nothing |
| `zero_duration` | a video with duration ≤ 0 | `TestDetectZeroDuration` |
| `zero_size` | size == 0 | `TestDetectZeroSize` |

**Each detector needs a negative test.** A detector that fires on everything
produces a table nobody reads, which is the outcome the issue is complaining
about. `TestDetectDuplicate` asserting only the positive case is how a
`WHERE true` ships.

**Verify:**
```sh
go test -tags integration ./pkg/sqlite/ -run 'TestDetect' -v
```
Expected: all PASS, and the negatives prove they can decline.

---

## Step 5 — read surface

**REST:** `internal/api/issue.go` — `GET /issues?resolved=false&page=1&per_page=50`,
`POST /issues/{id}/resolve`.

**GraphQL:** `internal/api/graphql/model/issue.go` + a resolver — the panel
queries through GraphQL, as the rest of the UI does.

**Pagination is required and defaults to unresolved** (spec §5). A panel that
opens on 4000 dismissed rows is not usable.

**Verify:**
```sh
go test -tags integration ./internal/api/ -run TestIssue -v
```
Expected: PASS, including a test that the DEFAULT query excludes resolved rows.

---

## Step 6 — the panel

**Route:** `/issues`, its own route because #837 says *dedicated*.

**Files:** `ui/v2.5/src/components/Issues/` — `IssuesPanel.tsx`, `issues.gql`,
`useIssues.ts`.

It lists unresolved issues newest-first: the file's name, the domain, the kind
rendered as a sentence, and a dismiss button per row. **No bulk actions** — bulk
dismiss is how a library ends up with 900 silently dropped findings and no record
of why.

The build here is `node node_modules/vite/bin/vite.js build` (pnpm, hoisted
linker), NOT `pnpm build`.

**Verify:**
```sh
node node_modules/vite/bin/vite.js build
```
Expected: exit 0, and a non-empty `build/`. A stale `build/` renders every route
blank with HTTP 200, so rebuild before checking anything in a browser.

---

## Step 7 — the gates, all of them, together

```sh
go build ./... && go vet ./...
go test ./pkg/... 2>&1 | grep -v 'no test files'
go test -tags integration ./pkg/sqlite/ -count=1
node node_modules/vite/bin/vite.js build
python3 docs/check-issue-ledgers.py
python3 docs/goal-check.py
```

Expected: the Go builds and vet are silent; the suites print `ok`; the ledger
check prints `OK: header, table and log agree`; goal-check shows C8 with #837 no
longer in the remaining list.

**Then the mutation run**, and its result goes in the commit message, not just in
a file:

| Mutation | Must turn red |
|---|---|
| drop `resolved` from the unique index | `TestIssueIsIdempotent` |
| `Record`'s ON CONFLICT → plain INSERT | `TestIssueIsIdempotent` |
| `Resolve`'s `AND resolved = false` removed | `TestResolveIsIdempotent` |
| the `zero_duration` comparison → `>= 0` | `TestDetectZeroDuration` |
| `detectFileIssues` made unconditional | the negative half of a detector test |

---

## Step 8 — close it

- `docs/ISSUES.md`: the `| 837 |` row → `done`, with the commit and the test names.
- `docs/closed-issues.md`: a `| stash#837 |` row, prose included — the reasoning
  about why the unique constraint is the design, and what the detectors decided
  NOT to do.
- `docs/UPSTREAM-ISSUES.md`: the 837 row → `closed`, header counts reconciled.
- Verify: `python3 docs/check-issue-ledgers.py` → OK. It fails if the roster says
  `closed` without a log row, so this is the step that catches a half-finished
  close.
