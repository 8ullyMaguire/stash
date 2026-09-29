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
| stash#2293 | Non-ASCII performers fail to be tagged with Auto Tag | **Already fixed upstream** — `getPathWords` truncates by runes, not bytes, and carries a `#2293` comment at the site. **No code changed.** What was missing was any evidence it stays fixed: the low-level `nameMatchesPath` had a `伏字` case, but `getPathWords` — the half that decides whether a performer is ever a *candidate* — had no test at all. Added as a regression guard. | `TestANonASCIIPathFragmentSurvivesWordExtraction`, `TestANonASCIIFragmentIsNotCorruptedByByteSlicing`, `TestAMultiRuneNameKeepsItsLeadingRunesIntact`, `TestNonASCIINamesBeyondCJKAlsoSurvive`, `TestAnASCIIPrefixOfANonASCIINameStillExtracts`, `TestSingleRuneWordsAreStillDropped`, `TestTheRuneThresholdIsCountedInRunesNotBytes` (pkg/match). Mutation harness `pkg/match/mutate_pathwords.py`: 3 killed, 0 survived — including two of my own tests that were false passes until the harness caught them. | `6e3e5e1e8` |
| stash#5237 | Naming issue for Taiwan in list of countries | The English label for `TW` was the library's *official* name, "Taiwan, Province of China". The library also stores "Taiwan" as an alternate name for the same code, so the wording the reporter asked for is one the data already had. An explicit one-entry override table now supplies it, applied in every locale, with a case- and whitespace-tolerant lookup. | `ui/v2.5/scripts/check-country-names.mjs` — 13 checks, including `EXACTLY ONE country is overridden -- the issue names one` and `no country the library names neutrally was made worse`. Mutation harness `ui/v2.5/scripts/mutate_country.py`: **8 killed, 0 survived**. `tsc --noEmit` and `biome check` clean; `go build ./...` unaffected. | `cae9a7f3c` |
| stash#5683 | High CPU / looping read access when loading a scene on a removed drive | `getTranscodeStream` wrote `200 OK` + `Content-Type: video/mp4` *before* knowing whether ffmpeg could produce anything. A transcode that died instantly — the source file is gone, e.g. the drive was removed or the file moved without a rescan — answered with a successful, empty stream. Firefox re-requests a stream that ends immediately, so every retry spawned another ffmpeg: 60-80% of a core and tens of thousands of log lines, sustained as long as the page stayed open. The handler now waits for the first byte before writing a status, so a transcode that produces nothing is a single 500 the client will not retry, and sets `Content-Type` only on the success path. | `TestATranscodeThatProducesNothingIsAnErrorNotAnEmptySuccess`, `TestAFailedTranscodeDoesNotAdvertiseAVideoContentType`, `TestANonEmptyResponseDoesNotCloakTheStatus`, `TestTheBytePeekedToDetectStartupIsNotLost`, `TestACancelledTranscodeIsNotReportedAsAServerFault` (pkg/ffmpeg, driving the real handler over a real `httptest.ResponseRecorder` with the encoder pointed at a stub script). Mutation harness `pkg/ffmpeg/mutate_transcode.py`: **7 killed, 0 survived**. `go vet`, `gofmt`, `go build` clean. | `de40efccf` |

---

## Not yet closed

450 issues are marked `planned` in `docs/UPSTREAM-ISSUES.md` and none of them
is started. The queue is the roster, in the order the roster lists.

When you close one, add the row **and** update the roster's verdict column, so
the two files cannot disagree.
