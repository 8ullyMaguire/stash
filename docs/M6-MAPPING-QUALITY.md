# M6 finding: the issue matrix is substantially mis-mapped

Recorded 2026-09-27, during the first capability pass of M6. Found while
reading the issues behind C17 (the plan's named first unblocker) before
implementing it, and stopped there rather than build on it.

**This blocks capability-by-capability work.** It does not block the milestone;
it means the milestone's unit of work — the capability — is currently defined by
a mapping that does not reliably point at the issue it claims to close.

## What the check was

`docs/research/map_final.json` maps all 850 open issues to 70 capability codes.
The mapping was checked three ways, all reproducible from the committed files:

1. **Keyword overlap** between the issue title and its capability's name
   (`taxonomy.py`'s short names). 603 of 850 share no keyword. This is a weak
   signal on its own — "X-Ray Style Performer Overlay in Fullscreen Viewer" vs
   "Image & gallery sets" is a real mapping that shares no word.
2. **Domain-object probe.** For the largest capabilities, a list of nouns the
   capability is *not* about, then count how many of its issues are about
   several of them anyway.
3. **Reading the issue bodies** for the cases that looked wrong, to confirm.

## What it found

Confirmed mis-mappings, quoted from the matrix and the upstream issue list:

| Issue | Title | Mapped to | Should be about |
|---|---|---|---|
| stash#13 | Scene upload from UI | C77 Reverse proxy & TLS | file upload |
| stash#284 | Navigation using next/previous buttons for performers | C77 Reverse proxy & TLS | navigation UI |
| stash#342 | Ability to upload sanitized logs to external pastebin | C77 Reverse proxy & TLS | logging |
| stash#6455 | SQL Query Improvments for Larger DB | C77 Reverse proxy & TLS | database |
| stash#6283 | Ability to arbitrarily group/organize scrapers | C77 Reverse proxy & TLS | scraper organisation |
| stash#4332 | Make "exclusions" regex field with more user friendly UI | C77 Reverse proxy & TLS | scraper UI |
| stash#5987 | Upgrade React + Dependencies | C36 Leaderboards & badges | dependencies |
| stash#6864 | Move disableAnimation lightbox option from GraphQL to UI config | C36 Leaderboards & badges | configuration |
| stash#7152 | Studio Tagger batch update panics when scraped studio has nil StoredID | C36 Leaderboards & badges | scraper, and a **crash** |
| stash#118 | Alphabetical performer list inside sidebar | C33 Typed field voting | sidebar UI |
| stash#7165 | Plugin settings should be able to add connect-src CSP sources | C33 Typed field voting | plugin settings, and a **CSP** control |
| stash#6871 | Add paths and behavior flags to findFolders()/findFiles() | C33 Typed field voting | plugin API |
| stash#4556 | Prevent installing multiple themes simultaneously | C17 Background job engine | themes |
| stash#6875 | Enter troubleshooting mode via ENV argument | C17 Background job engine | diagnostics |
| stash#2305 | Dedicated Saved Filter list for tagger view | C17 Background job engine | saved filters |

**C17 — the plan's own first choice of unblocker — is 8/14 coherent.** Eight
issues are genuinely about the task queue (`stash#1445` resume interrupted
queue, `stash#2080` backup concurrency, `stash#4207` queue priority, …). Six
are not, including one about themes and one about a command-line flag.

**C63 "Confirmation on cancel" held `stash#7240`, a labelled arbitrary-file-write
vulnerability.** That issue was mapped to a UI concern and would never have been
picked up by anyone reading C63's capability description. It is now fixed — see
the commit `fix(security): stash#7240`.

**C20 "Storage accounting" is 6/12 about the scene duplicate checker**, which is
a distinct feature. Only `stash#7194` (free disk space on the statistics page)
and `stash#5646` (a separate /temp/ path) match the capability name.

## Why this is not a small fix

`docs/research/taxonomy.py` contains only the **vocabulary** — a table of 91
capability ids with names, spec sections and one-line intents. It has no
classifier: no matching rules, no keywords, no scoring. The mapping was produced
by something that is not in the repository, and the only committed artefact is
its output. So there is no rule to correct; a re-derivation is needed, and the
quality of that re-derivation is the whole basis for measuring M6.

A partial patch is worse than none. Fixing C17's six wrong rows without
re-deriving the rest leaves 40-odd capabilities that still look authoritative,
and the milestone's completion count stays meaningless while appearing
meaningful.

## What I did about it

- **Did not build on the current mapping.** C17 was read and understood before
  any code was written, which is the only reason this was caught early.
- **Fixed the one issue whose mis-mapping hid a security defect**
  (`stash#7240`), because that defect is live in this tree regardless of how the
  mapping is later corrected. It is a real arbitrary-file-write, and it was
  unpatched.
- **Did not silently re-map anything.** A corrected matrix that looks equally
  authoritative but was produced by a method nobody reviewed is a worse failure
  than a visibly-wrong one.

## The decision this needs

M6's step 6.0 asks for a test that the matrix covers every open issue. That test
now exists and passes (`internal/matrix/issue_matrix_test.go`, commit
`c56cd0263`) — but it checks *coverage*, not *accuracy*, because accuracy is not
mechanically checkable against a document.

The options, and my recommendation:

1. **Re-derive the mapping from the issue bodies, with review** (recommended).
   The `open_issues.json` snapshot has every issue's full body — 933KB of real
   descriptions. Classify against the taxonomy, and record the *reason* per
   issue so a reviewer can spot a bad call rather than re-deriving 850 of them.
   This is its own piece of work, and it should be a numbered step (6.0a) before
   any per-capability implementation, not something done inside a capability
   ticket.
2. **Shrink the claim.** Report M6 as "issues enumerated and triaged, with N
   capabilities built", stop calling it "850 issues", and build capabilities
   chosen for value rather than for mapping coverage. Cheapest, and honest.
3. **Build per-capability from the capability definition, and record which
   issues each change actually closes as it goes** — so the mapping emerges from
   the work rather than gating it. Slower to report on, but never depends on a
   document being right.

Option 1 with option 3's recording discipline is the combination I would build:
re-derive, then let each capability's implementation record its own closures as
the authoritative record, with the matrix as a checked-but-not-trusted index.

## Reproducing this finding

```bash
cd ~/code-local/worktrees/m6
# the mis-mapped C77 and C63 rows, straight from the committed matrix
rg -n '\| C77 ' docs/research/matrix.md
rg -n '\| C63 ' docs/research/matrix.md
# the security issue that was hidden there
rg -n 'stash#7240' docs/research/matrix.md
```

`stash#13` is the clearest single example: "Scene upload from UI" mapped to
"Reverse proxy & TLS". Its body is a one-line link to a StashServer issue, so
there is no hidden nuance to rescue the mapping.
