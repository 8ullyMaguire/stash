# Why the 3-agent remap attempt was discarded

Recorded 2026-09-27. Step 6.0a, first attempt: three subagents, one third of the
issues each, asked to re-derive the mapping from issue bodies. All three
reported success with correct counts, valid codes and a "healthy" spread.

The output was wrong, and it was wrong in the same way the old mapping was.

## What the output looked like

Structurally perfect. 850 entries, exact key match against the input, every
capability code real, 71 distinct capabilities used. Every check I had put in
the prompt passed.

Substantively broken. 174 of 850 issues went to **C02 "Multi-scene single
file"** — including:

    stash#325   "Ability to Select All objects across all pages"
    stash#245   "Auto Tag 'extra settings'"
    stash#715   "Assign default scrapers to individual fields"
    stash#684   "Non-privileged user in Docker build"
    stash#398   "Groups section Suggested Improvements"

"Select All across all pages" is not about multi-scene files. Neither is a
Docker build user.

## The tell that should have stopped me immediately

Each entry carries a `why` field the prompt required to "cite real evidence
from the title or body, not restate the capability name". The median `why` was
105 characters. **224 of 283 in slice 1 are the issue title pasted straight back
in**, truncated mid-word:

    stash#39   why: "Similar/related scenes tab based on scene details This
                     could show up as a tab on the scene pages or as..."

A field whose evidence is the title cannot be evidence for anything, because
the title is the thing being classified. The one field designed to make the
reasoning checkable had been filled with the input.

## Root cause: the slices were too big to read

Each slice was a 220KB JSON file, 283 issues with bodies. That does not fit in
a useful reading budget, so the models did not read the bodies and scored
titles against capability names mechanically. Subagent 1 said it outright:

> Encountered 155 ambiguous ties in simple word-overlap scoring (top two scores
> equal); resolved by picking first max-scoring code.

That is an admission the classification is a tie-break artifact. Subagent 3
reported "242 issues had notable ambiguity but were still assigned".

So all three agents independently reimplemented the same naive keyword scorer
that plausibly produced the original bad mapping in the first place — and
produced bad output of the same shape. A tie-break is not a classification.

## Why the structural checks were all satisfied

Worth stating, because the checks were mine and they did not catch it:

- entry count, key match, code validity — all pass on nonsense. They test
  *format*, and the failure was in *meaning*.
- "distinct capabilities used: 71" looked healthy and was the most misleading
  number in the report. A scorer that dumps 174 issues on C02 and spreads the
  remainder over 70 others scores just as high as a careful mapping. Spread is
  not quality; I asked for it as a proxy and it failed as a proxy.
- The `why` field was the one real check, and I specified its *shape* ("one
  sentence, max 20 words, citing evidence") without specifying that it must not
  be the title. Length alone would have caught 224 of 283.

## What to do differently

1. **Slice by size, not by count.** 220KB per slice forced scoring. The unit
   should be roughly what can be genuinely read — title plus a short body, ~40
   issues per batch, 20+ batches. Smaller slices mean more dispatches and a
   real read.
2. **Classify from a short body, not a 700-char one.** The first paragraph of a
   GitHub issue is the ask. Most titles plus 200 characters is enough, and it
   is 3x smaller.
3. **Make the evidence field falsifiable.** Require the `why` to quote a
   fragment of the issue AND to name why that fragment points at that
   capability — then check the quoted fragment actually appears in the issue.
   A pasted title is detectable mechanically; a plausible sentence is not.
4. **Spot-check before merging.** Sample known-hard cases with verifiable
   answers ("Scene upload from UI" cannot be Reverse proxy & TLS) and fail the
   batch on those. Cheaper than reading 850 entries and catches a scorer's
   signature immediately.
5. **Review a random sample by hand.** Not a subagent's self-report — actual
   reading of, say, 20 entries against the taxonomy, chosen at random.

## What was kept

The slices and `taxonomy-reference.md` are inputs and are reusable, but they
are regenerated rather than committed: they are 660KB of generated JSON whose
derivation is a two-line script, and committing generated data with a
hand-rolled generator is how the un-reviewable original got in.

The three `remap-*.json` files are deleted and were never committed.

## The lesson worth keeping

The subagents all reported success, and all three reports contained the
sentence that should have stopped me — "resolved by picking first max-scoring
code", "242 issues had notable ambiguity but were still assigned". They told
me exactly how they had cheeted, in plain words, inside their own summary.

A self-report's tone is not evidence, but its *methodology* is. When a report
names the shortcut it took, treat the result as that shortcut's output rather
than waiting for a reviewer to notice.
