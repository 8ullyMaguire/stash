# SPEC — stash#837: log potential issues with files, show them in a dedicated UI

**Status:** specified, not yet built
**Issue:** [stash#837](https://github.com/stashapp/stash/issues/837) — "Log potential issues with files, show in a dedicated UI"
**Roster row:** `docs/UPSTREAM-ISSUES.md` §Planned — by signal, `| 837 | … | open |`

## 1. What the issue asks for

A user opens a library and finds something wrong: a duplicate, a file that was
never scanned, a video whose duration is zero, a gallery whose members did not
import. Today the only way to find that out is to read the log. #837 asks for two
things, and the second is the load-bearing one:

1. **Log** potential issues with files as they are found.
2. **Show** them in a dedicated UI.

A log nobody can read back is not a feature; the UI is what makes the log worth
keeping. So this spec treats the table as the product and the UI as its face.

## 2. Scope — and what is explicitly out

**In scope.** Detection during the scan, persistence of what was found, a read
surface (API + GraphQL), and a dedicated panel that lists them and lets the user
dismiss one.

**Out of scope, deliberately, and the reason for each:**

| Not doing | Why |
|---|---|
| A general-purpose rule engine | #837 says "potential issues with **files**". A user-configurable rule language is a different (much larger) issue, and building it here would produce a table nobody can reason about. |
| Automatic repair | Recording that a duplicate exists is the request. Deleting or merging files on the strength of a heuristic is a data-loss risk that needs its own design and its own consent. |
| Per-user issues | One library, one set of findings. Multi-tenancy is not in this codebase's model. |
| Severity / warning levels | Every row found by this spec is the same kind of thing: a fact about the library that a human may want to look at. A severity axis needs a second axis of judgement to be worth storing. |

## 3. The data model

One table, `issues`:

| Column | Type | Notes |
|---|---|---|
| `id` | integer PK | |
| `file_id` | integer | FK → `files.id`, **nullable**: an issue can be about the library as a whole (a scan that produced nothing) |
| `domain` | text | which subsystem raised it — `file`, `scan`, `metadata`. See §4. |
| `kind` | text | the specific finding within a domain, e.g. `duplicate`, `zero_duration`, `not_indexed` |
| `details` | text | free-form, human-readable; NOT parsed by anything |
| `detected_at` | timestamptz | when it was found |
| `resolved_at` | timestamptz | NULL until the user dismisses it |
| `resolved` | boolean | redundant with `resolved_at` but indexed; a finding is either live or dismissed |

**Identity — the part that decides whether this is useful or noise.** An issue is
a *fact about the library*, not an event in the log. Finding the same duplicate
on every scan must not create a second row. So:

> UNIQUE (`file_id`, `domain`, `kind`, `resolved`)

`resolved` is in the key deliberately, so a dismissed issue stays dismissed across
later scans of the same file, while a *new* finding of the same kind (the file was
re-imported and is broken a different way) is recorded afresh. A finding that is
live and then re-detected updates `detected_at` in place rather than inserting.

This constraint is the load-bearing part of the design. Without it the table is a
log, and the UI is a log viewer — which is what the issue is complaining about.

## 4. Detection

`domain` and the `kind`s within it, each with the check that raises it:

| domain | kind | raised when |
|---|---|---|
| `file` | `duplicate` | the file's checksum equals another file's |
| `file` | `zero_duration` | a video file's duration is 0 or NULL |
| `file` | `zero_size` | the file's size on disk is 0 |
| `scan` | `no_files` | a scan finished having added no files |

**Where detection runs.** Inside the scan, at the point the file row is written,
so a finding costs one indexed lookup and no extra pass over the filesystem. Not
as a separate sweep: a sweep would have to re-stat every file, which is the
expensive thing the scan just avoided.

**What detection must NOT do.** It must not log an issue for a file the user
dismissed (§3), and it must not raise two issues for one fact. Both are enforced
by the unique constraint rather than by a check in the caller, so a future caller
cannot get it wrong.

## 5. Read surface

- `GET /issues?resolved=false` — list, filterable by `domain` and `kind`.
- GraphQL `issues(filter: IssueFilterType): [Issue!]!` — for the UI panel.
- `POST /issues/{id}/resolve` — dismiss. Idempotent.

**The list is paginated and defaults to unresolved.** A panel that opens on 4000
dismissed rows from six months ago is not usable, and the default is the thing
that decides whether anyone uses it.

## 6. The UI panel

A dedicated route (`/issues`) and a link to it, because #837 says *dedicated* —
not a tab inside an existing page. It shows unresolved issues, newest first, with
the file's name, the domain, the kind rendered as a sentence ("this file is a
duplicate of another file"), and a dismiss button per row. No bulk actions: bulk
dismiss is how a library ends up with 900 silently dropped findings and no record
of why.

## 7. Verification — each of these must be a real command with real output

1. The migration applies and the table exists with the unique constraint:
   `go test -tags integration ./pkg/sqlite/ -run TestIssueTable -v` → PASS
2. The same finding twice is ONE row: `… -run TestIssueIsIdempotent` → PASS
3. A dismissed issue is not re-raised by a later scan of the same file:
   `… -run TestDismissedIssueIsNotReraised` → PASS
4. Detection fires for a real duplicate checksum, and only for it:
   `… -run TestDetectDuplicate` → PASS
5. The API paginates and defaults to unresolved: `… -run TestIssueAPI` → PASS
6. The full sqlite suite and `go vet ./...` stay green.

**Mutation coverage, because a detection rule that cannot fail is decoration.**
Mutate each of: the unique constraint, the `resolved` predicate, the
`zero_duration` comparison, and the `details` construction. Each must turn a test
red. Recorded in `docs/closed-issues.md` when the issue closes.

## 8. Definition of done

- [ ] migration + model + store, with the unique constraint
- [ ] four detectors, each with its own test
- [ ] idempotence and dismiss-persistence tests
- [ ] REST + GraphQL read surface
- [ ] `/issues` panel
- [ ] mutation run: zero survivors
- [ ] `docs/ISSUES.md` row → done, `docs/closed-issues.md` row added, roster row → `closed`
- [ ] `python3 docs/check-issue-ledgers.py` → OK, `python3 docs/goal-check.py` → C8 green

## Changelog

- **2026-10-02** — v1. Spec written before any code, per the standing workflow.
  The §3 uniqueness constraint and the §4 "no separate sweep" decision are the
  two choices worth arguing about; both are recorded here with their reasoning so
  a later reader can tell a decision from a default.
