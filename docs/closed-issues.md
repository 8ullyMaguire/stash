# Closed upstream issues

The log of issues fixed on `main`, the soft fork. One row per issue, with the
test that proves the fix and the commit that carried it.

**The honest summary of this work is "N issues closed, each with a test that
fails without the fix."** Never "N issues fixed" with no test named — a fix
with no test is a claim, and the pass count of `go test ./...` is the only
progress signal that cannot be faked.

Columns:

- **issue** — the upstream number, `stash#N`
- **title** — as upstream has it
- **the fix** — what the code did, and what it does now
- **the test** — the test that fails without the fix. A test that passes on
  unfixed code is not a test, it is a comment
- **commit** — the `main` commit

---

## Closed

| issue | title | the fix | the test that proves it | commit |
|---|---|---|---|---|
| stash#7240 | Zip-slip arbitrary file write on import | `pkg/fsutil/safepath.go` resolves any archive member under a root before it is used, on both the import and the package-install path. A member that escapes the root is refused **by name**, before any write is attempted. | `TestSafeJoinRejectsTraversal`, `TestSafeJoinAgreesWithJoinForLegitimateEntries`, `TestSafeJoinNormalisesTheBase`, `TestSafeJoinErrorNamesTheEntry` (pkg/fsutil) and `TestUnzipFileRefusesPathTraversal`, `TestUnzipFileRefusesEveryTraversalShape`, `TestUnzipFileStillExtractsOrdinaryArchives` (internal/manager). See `docs/ZIP-EXTRACTION-AUDIT.md` for the four call sites and why two were already safe. | `f02e8fd88`, `5ec3fe849` |
| stash#7152 | Studio tagger batch panics on a nil `StoredID` | A scraped studio with no `StoredID` aborted the whole batch. The unusable value is now rejected at the point of use and the rest of the batch completes. | `TestScrapedStoredIDRejectsUnusableValues`, `TestScrapedStoredIDNeverPanicsOnNil`, `TestProcessMatchedStudioSkipsNilStoredID`, `TestProcessMatchedPerformerSkipsNilStoredID` (internal/manager). | `dbd3d705c` |
| stash#7212 | `UNIQUE constraint failed` during scene identify | `ScrapedPerformer` required the name lookup to return exactly one performer, so two same-named performers (separated by disambiguation, which the partial unique index permits) read as *absent* — the match was discarded, StoredID stayed nil, and the caller created the duplicate that then failed the constraint. The candidate set is now narrowed with the scraper's own `Disambiguation` field, which the lookup never consulted. An ambiguity that stays unresolved is left unmatched rather than settled by picking a row. | `TestAScraperDisambiguationResolvesAnAmbiguousName`, `TestAnUnresolvableAmbiguityIsNeverSettledByGuessing`, `TestSeveralCandidatesSharingTheDisambiguationAreStillAmbiguous`, `TestASingleNameMatchSetsTheStoredID`, `TestAnUnknownNameStillLeavesTheStoredIDNil`, `TestAnAmbiguousNameDoesNotFallThroughToAnAlias`, `TestARemoteSiteIDOutranksAnAmbiguousName` (pkg/match — the package's first test file). Mutation harness `pkg/match/mutate_scraped.py`: 5 killed, 0 survived. | `7c93582f9` |

---

## Not yet closed

450 issues are marked `planned` in `docs/UPSTREAM-ISSUES.md` and none of them
is started. The queue is the roster, in the order the roster lists.

When you close one, add the row **and** update the roster's verdict column, so
the two files cannot disagree.
