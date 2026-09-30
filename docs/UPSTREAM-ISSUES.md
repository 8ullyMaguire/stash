# Upstream issues — the soft-fork work list

**Generated 2026-09-29** from `stashapp/stash` via the GitHub API.
**675 issues: 427 planned, 221 not planned, 27 closed (see `docs/closed-issues.md`).** This file is the input to
`/goal continue solving open issues from stash repo`; see `docs/GOAL-UPSTREAM.md`.

Every issue in the repository appears exactly once below, with a verdict
and the reason for it. The counts reconcile to 675 or the generator fails.

## The rules, in order

| Rule | Count | What it removes and why |
|---|---|---|
| R1 | 39 | R1 upstream labels it a plugin idea, not core |
| R2 | 9 | R2 a client app or extension, not this codebase |
| R3 | 5 | R3 a new first-class object: schema, GQL, UI, scan |
| R4 | 2 | R4 object sync: an architecture, not a fix |
| R5 | 7 | R5 translating the UI, not fixing it |
| R6 | 1 | R6 a product decision, not an issue |
| R7 | 14 | R7 one issue implies a whole subsystem |
| R9 | 107 | the 107 upstream explicitly marked (bug report, help wanted, bounty) are **kept unconditionally** |
| R10 | 132 | lowest-signal feature requests, cut to reach two thirds |

**427 planned, 221 not planned, 27 closed, 675 total.**

`not planned` is the **combined** bucket: the 128 rows marked `not-planned` plus the
93 marked `deferred`. The table has four status values but the tally has three, so
"not planned" is not a column you can read off — recompute it as
`not-planned + deferred`, or the sum stops matching the total. `docs/check-issue-ledgers.py`
enforces this (it was written after that invariant broke once).

## How to read `planned`

`planned` is a **work queue, not a priority order**. It says an issue has a
home in this codebase and is worth doing. It does not say what is most
valuable — that is a product judgement the owner has not delegated. Work
them in the order they are implemented and recorded in
`docs/closed-issues.md`.

When one planned issue turns out to need a subsystem, **stop and record it**
rather than half-building it. M6's own log records the cost of not doing
that: 850 issues mapped to 70 capabilities produced a diff no maintainer
could review.

## Planned — upstream-marked (107)

These carry a label the maintainers themselves applied.

| # | Title | Why | Verdict |
|---|---|---|---|
| 571 | Support for multiple performer images | upstream-marked (bounty). **The issue's PREMISE is wrong, measured against a real database** — storage already supports many images: `performers_images` (migration 13) is a many-to-many join table, performer_id + image_id, both indexed, both ON DELETE CASCADE, and filesystem autotag already writes it via `image.AddPerformer` (`internal/autotag/performer.go:85`). Trusting the issue would have meant designing a migration for a table that exists. **The real defect is narrower and different: a performer carries an image through TWO systems.** (1) A single-blob COLUMN on `performers`, written by `blobJoinQueryBuilder.UpdateImage` (`UPDATE performers SET <blob> = ? WHERE id = ?`) and read by `performerResolver.ImagePath` via `HasImage`. (2) The `images` TABLE + `performers_images` join, read by `performerResolver.ImageCount` via `image.CountByPerformerID`. The GraphQL mutation writes (1) and never touches (2); the counter reads (2). So **`image_count` is 0 for any performer created through the API with an image** — the field a client would use to decide whether to fetch one. Not 'always broken': correct for autotagged rows. **Decision on the fix (delegated to me by the owner 2026-09-30): add `images: [String!]` with REPLACE semantics**, matching how `alias_list`, `urls` and `tag_ids` already behave, writing the join table, and **leaving the blob column alone** so no existing user loses their single image — additive in effect though the field replaces. Not started: it is a schema change plus codegen plus resolver plus a store method, and the goal file says to record a subsystem-sized issue rather than half-build it. | planned |
| 2049 | Request for Submissions: Stash Logo | upstream-marked (help wanted) | planned |
| 684 | Non-privileged user in Docker build | upstream-marked (bounty) | closed |
| 398 | Groups section Suggested Improvements | upstream-marked (help wanted) | planned |
| 13 | Scene upload from UI | upstream-marked (bounty) | planned |
| 4336 | Renaming and presentation of all tagging/scraper components and relate | upstream-marked (help wanted) | planned |
| 3530 | Support multiple scenes in a single file | upstream-marked (bounty) | planned |
| 3065 | Make Stash more suitable for JAV | upstream-marked (help wanted) | planned |
| 2824 | Slow scanning with huge amounts of videos | upstream-marked (bug report) | closed |
| 2122 | Filter Functionality UI/UX Refactor Discussion | upstream-marked (help wanted) | planned |
| 5731 | Hardware decoding in generation tasks | upstream-marked (help wanted) | planned |
| 5683 | High CPU / looping read access when loading scene associated with remo | upstream-marked (bug report) | closed |
| 5538 | Performer Image Malformed via Local Image URL when API/Creds Enabled | upstream-marked (bug report) | closed |
| 5317 | Sometimes images are placed outside the viewport in lightbox on Androi | upstream-marked (bug report) | planned |
| 5237 | Naming issue for Taiwan in list of countries | upstream-marked (bug report) | closed |
| 5002 | Plugin settings UI/UX | upstream-marked (help wanted) | planned |
| 4136 | Can't cast any video to Chromecast | upstream-marked (bug report) | planned |
| 3722 | pHash Improvement for Short Durations | **CLOSED** `63635bcc0` — see closed-issues.md | closed |
| 3318 | Studio Code display improvement | upstream-marked (help wanted) | planned |
| 3299 | Native Remote UI | upstream-marked (help wanted) | planned |
| 3172 | Stash icon almost invisible on windows 10 dark mode | upstream-marked (help wanted) | planned |
| 3171 | Synology NAS and folders table | upstream-marked (bug report) | planned |
| 2833 | `e` keyboard shortcut collides with subpages that use the same shortcu | upstream-marked (bug report) | planned |
| 2540 | Image HTTP request referrer behavior | upstream-marked (help wanted) | closed |
| 2293 | Non-ASCII performers fail to be tagged with Auto Tag | **CLOSED** `6e3e5e1e8` — already fixed upstream; regression guard added | closed |
| 2149 | Phash validation | upstream-marked (bug report) | closed |
| 1961 | Performers sub-page and performer cards include objects from other stu | upstream-marked (bug report) | planned |
| 647 | Add keyboard shortcuts to focus selector fields | upstream-marked (help wanted) | planned |
| 7247 | VR videos get super bright and washed out when played in VR mode | upstream-marked (bug report) | planned |
| 7236 | Heavy load time on Safari for Scene pages | upstream-marked (bug report) | closed |
| 7212 | `UNIQUE constraint failed` errors during scene identify | **CLOSED** `7c93582f9` — see closed-issues.md | closed |
| 7202 | Tagger should not cut off vertical cover images (for the local scene) | upstream-marked (bug report) | planned |
| 7187 | No way to return "No images found" from scraper script? | upstream-marked (bug report) | planned |
| 7173 | Generate failing make previews | upstream-marked (bug report) | planned |
| 7154 | Lightbox back button behavior cause unsaved modal to show | upstream-marked (bug report) | planned |
| 6978 | HEVC Video in MKV playback freezes when skipped to another part of the | upstream-marked (bug report) | planned |
| 6897 | Sub-Groups without scenes not displaying initial page-load properly | upstream-marked (bug report) | planned |
| 6732 | HEIC/HEIF Image Format Support with Live Photo Pairing | upstream-marked (help wanted) | planned |
| 6577 | No way to prevent .webm files from being categorized as "videos"/scene | upstream-marked (bug report) | planned |
| 6526 | Player bottom controls are clipped (fullscreen missing) + menus overla | upstream-marked (bug report) | planned |
| 6466 | Unsaved entries intermittently lost in Scene Edit Tags or Performers | upstream-marked (bug report). **FIXED.** The reporter's two clues — “more likely with a large maxOptionsShown” and “does not reproduce when the video is paused” — both point away from the select box and at the real trigger: a **10-second timer**. `track-activity.ts` runs a 1s interval and every `sendInterval = 10` calls `sendActivity()`, which awaits `sceneSaveActivity`/`sceneIncrementPlayCount`; Apollo normalises those mutation results back into the cache, so `data` changes identity and Scene.tsx's `useLayoutEffect(… setScene(data?.findScene) …, [data, loading])` sets a brand-new `scene` object. The editor’s DRAFTS were synced from it with `useEffect(() => setPerformers(scene.performers ?? []), [scene.performers])` and the same in `tagsEdit.tsx` — every `scene.performers`-shaped value is a fresh array on the new object, so the effect re-ran and **overwrote the unsaved draft with the saved values**. Both boxes, matching “both may be lost”. The large dropdown size is only an **amplifier**: a slower select query keeps the input focused with an unsaved entry for longer. **`useInitialState` already existed and already documented exactly this** (“only updated if the current state is unchanged from the initial state”) — these two call sites were not using it. All five drafts now use it; a pristine draft still syncs so a real server change lands, and the explicit post-save/cancel resets use the plain setter. **A hypothesis I had to discard:** a stale-response race in FilterSelect’s `debounceLoadOptions`. lodash debounce genuinely cancels no in-flight request, but react-select guards it — `if (request !== lastRequest.current) return;` (`useAsync-c64f5536.esm.js:119`) — so that path was a plausible fix in the wrong file. | closed |
| 6456 | bfcache not used because WebSocket connection is not closed | upstream-marked (bug report) | planned |
| 6452 | Tagger View Jumps Position | upstream-marked (bug report) | planned |
| 6246 | Scene Tagger Navbar floats out of position | upstream-marked (bug report) | planned |
| 5987 | Upgrade React + Dependencies | upstream-marked (help wanted) | planned |
| 5850 | Thumbnails generated as JPEG drop transparency resulting in black thum | upstream-marked (bug report) | closed |
| 5709 | Zombie process left (Python defunct) | upstream-marked (bug report) | planned |
| 5681 | Support Hardware Acceleration (Intel Integrated Graphics) When Buildin | upstream-marked (help wanted) | planned |
| 5178 | A>B Loop Controls do not work on Apple Touch Devices | upstream-marked (bug report) | planned |
| 5033 | Scene Tagger/Scrape with.../Scrape query for stash-box parses comma se | upstream-marked (bug report) | planned |
| 4560 | Blob remains in use and prevents performer image from being replaced/d | upstream-marked (bug report) | planned |
| 4536 | Casting the video never loads or plays while the casting is activated | upstream-marked (bug report) | planned |
| 4415 | Scrapers that build a queryURL | upstream-marked (bug report) | planned |
| 4163 | Deleting a duplicated gallery makes all images of the remaining galler | upstream-marked (bug report) | planned |
| 3849 | Wrong order of images in galleries on identical files | upstream-marked (bug report). **Reproduced against a real database — the defect is live.** The gallery FILTER restricts membership via `galleries_images` (image_filter.go:255-262), but the ORDER BY joins `images_files`+`files` with no such restriction (image.go:1057-1070), so the sort KEY is read off an arbitrary row of that join rather than the one the filter matched. Same shape as #571: a many-to-many whose far side is unconstrained. Measured: SQLite takes the FIRST `images_files` row it reaches, so the winner depends on INSERTION ORDER — which is exactly why the title says 'intermittently'. Reproduced with the reporter's own setup (003.jpg in gallery_99.zip, the same bytes as 006.jpg in gallery_01.zip, **separate folders**, since the key is the full path): gallery 99 returns `[shared 001 002]` instead of `[001 002 shared]`, i.e. the reported “006, 001, 002”. Two findings worth carrying. (1) `img.Path` is the **PRIMARY** file (`images_files.primary = 1`), so asserting on the returned path proves nothing about the sort — the observable is the ORDER of the ids. (2) The asymmetry: gallery 01 comes out CORRECT, because the row the sort reaches is 006.jpg, which agrees with that gallery. Only the gallery whose archive is not the first row reached misorders. `title` sorts correctly here only because the fixture images have empty titles, so COALESCE ties and the secondary `folders.path` decides. **Fix NOT started**: correlating the sort key to the file row the gallery filter matched is a rewrite of the sort builder, so per the goal file it is recorded, not half-built. | planned |
| 3741 | Scene detail screen loads full size thumbnails of all items in the que | upstream-marked (bug report) | planned |
| 3664 | Long filenames cause cryptic deletion errors | upstream-marked (bug report) | planned |
| 3496 | Adding Hashes to Galleries for Potential Stash-Box Integration | upstream-marked (help wanted) | planned |
| 3426 | Anamorphic videos previews are not normalized | upstream-marked (bug report) | planned |
| 3159 | Improve filtering in presence of NULL values | upstream-marked (help wanted) | planned |
| 2773 | Stash is unaware when an image has been replaced if the name is the sa | upstream-marked (bug report) | planned |
| 2765 | Lightbox image changes on rating/o-counter value change | upstream-marked (bug report) | planned |
| 2464 | Change default setting of PHash generation to ON for Scans | upstream-marked (help wanted) | planned |
| 7263 | Mapped scrapers assign wrong attributes to sub-objects when values rep | upstream-marked (bug report) | closed |
| 7256 | Input file buttons in firefox unresponsive | upstream-marked (bug report) | closed |
| 7240 | [security] Zip-Slip arbitrary file write in import and package install | upstream-marked (bug report) | closed |
| 7239 | Hardware transcode: [InitHWSupport] Supported HW codecs [0] gives no a | upstream-marked (bug report) | planned |
| 7238 | paths.funscript should use signed URLs when authentication is enabled | upstream-marked (bug report) | planned |
| 7234 | Details for Performers being shown below Picture on Safari | upstream-marked (bug report) | closed |
| 7231 | Can't scrape any male or trans performers using freeones and all other | upstream-marked (bug report) | planned |
| 7229 | Preview generation fails when video stream has a non-zero start offset | upstream-marked (bug report) | planned |
| 7222 | Studios page extremely slow on large image libraries | upstream-marked (bug report) | planned |
| 7217 | Scene preview videos play with 20-30s delay / choppy in Firefox on Lin | upstream-marked (bug report) | planned |
| 7216 | Windows FFmpeg download uses the "essentials" build, which has no libd | upstream-marked (bug report) | planned |
| 7209 | Freeones: `Could not parse career length 2016-now` | upstream-marked (bug report) | planned |
| 7198 | Updating scrapers does not install new requirements | **CLOSED** `c2bfd44ce` via PR #7199 — see closed-issues.md | closed |
| 7179 | `.nogallery` does not remove an existing folder-based gallery during C | **CLOSED** `68192aa59` — see closed-issues.md | closed |
| 7155 | Stale sprite/preview/cover/transcode after a same path file content ch | upstream-marked (bug report) | planned |
| 7152 | Studio Tagger batch update panics when scraped studio has nil StoredID | upstream-marked (bug report) | closed |
| 7149 | Panning images in lightbox with the mouse wheel can result in navigati | upstream-marked (bug report) | planned |
| 7148 | Drags that overshoot the image boundaries result in the lightbox/image | upstream-marked (bug report) | planned |
| 7147 | Fast dragging motions in the lightbox/image-viewer navigate to the nex | upstream-marked (bug report) | planned |
| 7145 | Remote image downloader sends no User-Agent, causing HTTP 418 | upstream-marked (bug report) | planned |
| 7142 | Scroll position is lost when navigating back to a list | upstream-marked (bug report) | planned |
| 7136 | JSON based scrapers fail with Chrome CDP | **CLOSED** `3f6678e73` — see closed-issues.md | closed |
| 7135 | Unmentioned/bugged max password length | upstream-marked (bug report) | planned |
| 7133 | Using "excludes" or "is not" with the disambiguation filter hides non- | upstream-marked (bug report) | planned |
| 7130 | App not responding on RClone drive - "Loading ..." forever | upstream-marked (bug report) | planned |
| 7106 | Deleting an image in a zip gallery silently fails | R9/R10 lowest-signal feature request. **Root-caused and FIXED.** `file.Destroy` deleted the DB row, then SKIPPED the filesystem delete for zip members (correct — a member's Path is synthetic, `/lib/gallery.zip/inner.jpg`, and `os.Rename` on it would fail) and returned nil. So the ARCHIVE kept the member, nothing recorded the pending removal, and the scanner re-walks a zip whenever it is new / fingerprint-changed / rescan / handler-required (task_scan.go:407) — which is exactly the reporter's 'image reappears after rescan'. The bug was the SILENCE, not the skip. **Upstream PR #7107 existed and was CLOSED AS STALE, not rejected** — 731 lines rewriting the archive without the member at `Deleter.Commit`, with rollback support and the non-UTF8 entry-name decoding preserved. Applied, plus two defects I found in it: (1) `os.CreateTemp` creates 0600 and the caller REPLACES the archive, so every rewritten zip silently became owner-only — a media library served by another user loses group/other read after one delete; now the original's mode is captured and applied. (2) no `fsync` before the replace, so a crash right after the rename could leave a present-but-empty archive where the user's only copy was; now synced. Checked and NOT defects: cross-device rename loss (SafeMove tries `os.Rename` first and the temp is in the zip's own directory, so the common path is an atomic same-fs replace) and `f.Base().ZipFile` being unset (the main file query LEFT JOINs the zip file and folder, so it carries a real path — worth checking, because `DirEntry.ZipFile` is documented 'transient, not persisted'). | closed |
| 7028 | Inconsistent File Info shortcut on gallery/image pages conflicts with  | upstream-marked (bug report) | planned |
| 6949 | Animated Images using VideoFile in GQL | upstream-marked (bug report) | planned |
| 6939 | Phash Generation task does not generate for videos classified as image | upstream-marked (bug report) | planned |
| 6814 | Queue gets stuck on fileless scenes | upstream-marked (bug report) | planned |
| 5979 | Top navigation bar cut off on mobile devices with notch/floating islan | upstream-marked (bug report) | planned |
| 5953 | Deleting a file while generating for it locks up scan/generate | upstream-marked (bug report) | planned |
| 5758 | Scene save activity causing re-queries for plugin | upstream-marked (bug report) | planned |
| 5430 | Count Sub-tag scenes in the Tag View when "Display subtag content" is  | upstream-marked (bug report) | planned |
| 5336 | Edit Modal - Improving bulk editing workflow | upstream-marked (help wanted) | planned |
| 5329 | Improving the merge modal window layout | upstream-marked (help wanted) | planned |
| 5111 | GIF files in ZIP archives are images vs video | upstream-marked (bug report) | planned |
| 5036 | Rescanning windows on *nix paths breaks galleries | upstream-marked (bug report) | planned |
| 4815 | Aliases not changed on performer scrape before alias uniqueness is tes | upstream-marked (bug report) | planned |
| 4667 | Popovers may appear outside the viewport | upstream-marked (bug report) | planned |
| 4549 | Video controls and filters do not reset on queue change | upstream-marked (bug report) | planned |
| 4233 | Rotation information is ignored for determining if a video is portrait | upstream-marked (bug report) | planned |
| 3692 | Improve log settings | upstream-marked (help wanted) | planned |
| 3333 | Saved Filters: Tag-item badge below toolbar not updating | upstream-marked (bug report) | planned |

## Planned — by signal (343)

Ranked by discussion volume and age, among issues with no maintainer label.

| # | Title | Why | Verdict |
|---|---|---|---|
| 1029 | Folder-like structure for organizing content | R9/R10 lowest-signal feature request | planned |
| 1010 | "Search All" functionality for scene tagger view | R9/R10 lowest-signal feature request | planned |
| 691 | SOCKS5 proxy support for scraping | R9/R10 lowest-signal feature request | planned |
| 591 | XPath scraper shouldn't remove newlines for Details field | R9/R10 lowest-signal feature request | planned |
| 422 | Enhanced performer aliases with studio association | R9/R10 lowest-signal feature request | planned |
| 4510 | Suggestions for UI Plugin API improvements | R9/R10 lowest-signal feature request | planned |
| 4300 | Docker container overhaul | R9/R10 lowest-signal feature request | planned |
| 3122 | Create All/New/Missing on Scene Tagger page | R9/R10 lowest-signal feature request | planned |
| 2976 | Include performers and tags in scene keyword searching | R9/R10 lowest-signal feature request | planned |
| 2973 | Give a measure of importance to tags | R9/R10 lowest-signal feature request | planned |
| 2633 | Tag edit option to hide tag from dropdown lists | R9/R10 lowest-signal feature request | planned |
| 2521 | Ability to bulk submit scenes to stash-box | R9/R10 lowest-signal feature request | planned |
| 1867 | Duplicate Checker tool for Images | R9/R10 lowest-signal feature request | planned |
| 1790 | Generalized support for external IDs | R9/R10 lowest-signal feature request | planned |
| 1586 | Folder View mode for browsing | R9/R10 lowest-signal feature request | planned |
| 1463 | Ability to map scraped tag to multiple Stash tags | R9/R10 lowest-signal feature request | planned |
| 1253 | Separating tags for higher-level objects | R9/R10 lowest-signal feature request | planned |
| 1030 | Add "Media" tab which combines scenes and images | R9/R10 lowest-signal feature request | planned |
| 1024 | Performer-specific Tag-field in Scene Edit tab | R9/R10 lowest-signal feature request | planned |
| 1017 | One-click button to create all missing objects in "Scene Scrape Result | R9/R10 lowest-signal feature request | planned |
| 972 | "Watch Preview" Button | R9/R10 lowest-signal feature request | planned |
| 966 | Support external playback via STRM file | R9/R10 lowest-signal feature request | planned |
| 899 | Custom headings in the "List" view and easier sorting | R9/R10 lowest-signal feature request | planned |
| 894 | Allow to change encoder for FFmpeg -c:v parameter in Configuration for | R9/R10 lowest-signal feature request | planned |
| 872 | Add "set this image as..." in image operations menu | R9/R10 lowest-signal feature request | planned |
| 821 | Support for multiple 'is missing' filters | R9/R10 lowest-signal feature request | planned |
| 779 | Mark Scene as Full Movie | R9/R10 lowest-signal feature request | planned |
| 765 | Ability to attach performer to markers | R9/R10 lowest-signal feature request | planned |
| 761 | Common field post-processing scraper enhancement | R9/R10 lowest-signal feature request | planned |
| 758 | Automated client-side update mechanism | R9/R10 lowest-signal feature request | planned |
| 715 | Assign default scrapers to individual fields and add 'Scrape From All  | R9/R10 lowest-signal feature request | planned |
| 710 | Store and display corrupt files | R9/R10 lowest-signal feature request | planned |
| 634 | Ability for previews to skip the intro of scenes from specific sources | R9/R10 lowest-signal feature request | planned |
| 552 | Calculate average performer rating from rated scenes | R9/R10 lowest-signal feature request | planned |
| 517 | List view overhaul; rich tables for easier bulk and individual metadat | R9/R10 lowest-signal feature request | planned |
| 484 | Support date with Auto Tag task | R9/R10 lowest-signal feature request | planned |
| 342 | Ability to upload sanitized logs to external pastebin | R9/R10 lowest-signal feature request | planned |
| 325 | Ability to "Select All" objects across all pages | R9/R10 lowest-signal feature request | planned |
| 314 | Add functionality to prevent cleaning on particular drives (Hot swap d | R9/R10 lowest-signal feature request | planned |
| 260 | Use markers to create scene chapters/bookmarks | R9/R10 lowest-signal feature request | planned |
| 245 | Auto Tag "extra settings" | R9/R10 lowest-signal feature request | planned |
| 226 | Custom scenes and playlists | R9/R10 lowest-signal feature request | planned |
| 185 | Query syntax | R9/R10 lowest-signal feature request | planned |
| 118 | Alphabetical performer list inside sidebar | R9/R10 lowest-signal feature request | planned |
| 47 | Implement ajax load/infinite scroll pagination functionality | R9/R10 lowest-signal feature request | planned |
| 39 | Similar/related scenes tab based on scene details | R9/R10 lowest-signal feature request | planned |
| 7204 | Add Sex/Genitals Field for Performers | R9/R10 lowest-signal feature request | planned |
| 7036 | Add "Primary Source" as default/top of list in scrape dialog | R9/R10 lowest-signal feature request | planned |
| 6795 | Create and manage Custom Fields to be used across all Scenes | R9/R10 lowest-signal feature request | planned |
| 6669 | Merge tags on alias collision  | R9/R10 lowest-signal feature request | planned |
| 6454 | Ability to cutomize the prefix for path links on File Info tab | R9/R10 lowest-signal feature request | planned |
| 6421 | Rename o-counter concept into "Hearts" | R9/R10 lowest-signal feature request | planned |
| 6390 | Improving pagination performance by caching results of larger queries | R9/R10 lowest-signal feature request | planned |
| 6185 | Create automatic backup upon installing a new plugin to protect from m | R9/R10 lowest-signal feature request | planned |
| 6134 | Add image support to identify task | R9/R10 lowest-signal feature request | planned |
| 6080 | Enable installation as Windows service | R9/R10 lowest-signal feature request | planned |
| 6071 | Support immersive VR video from browser via WebXR API | R9/R10 lowest-signal feature request | planned |
| 5769 | Support hardware acceleration for phash/sprite/preview generation task | R9/R10 lowest-signal feature request | planned |
| 5762 | Configure max memory usage | R9/R10 lowest-signal feature request | planned |
| 5326 | Add icon to performer profile/card to indicate performer has passed aw | R9/R10 lowest-signal feature request | planned |
| 5238 | Sort URLs alphabetically with optional ability manually re-order | R9/R10 lowest-signal feature request | planned |
| 4619 | Tags Page > Tag Item > Additional Performers (Not named like that) Nav | R9/R10 lowest-signal feature request | planned |
| 4274 | Add dedicated page that tracks Watch History | R9/R10 lowest-signal feature request | planned |
| 4089 | Allow scraping scene index (Group Scene Number) by scene scrapers | R9/R10 lowest-signal feature request | planned |
| 4053 | Support TIF/TIFF image format | R9/R10 lowest-signal feature request | planned |
| 3867 | Option to search subfolders when generating galleries | R9/R10 lowest-signal feature request | planned |
| 3859 | New Scene View Mode TikTok style | R9/R10 lowest-signal feature request | planned |
| 3790 | Update Filters in Search View, Add IsHaving along with isMissing and m | R9/R10 lowest-signal feature request | planned |
| 3781 | Display filtered object counts in subpages | R9/R10 lowest-signal feature request | planned |
| 3751 | Option to set default gender for new performer | R9/R10 lowest-signal feature request | planned |
| 3617 | Ability to Manually Link Performers and show direct link to their prof | R9/R10 lowest-signal feature request | planned |
| 3469 | Add the ability to group Tags into Tag Groups | R9/R10 lowest-signal feature request | planned |
| 3422 | Ability to upload and set scene cover from remote server | R9/R10 lowest-signal feature request | planned |
| 3400 | Rewamp tagging system to support tag attributes | R9/R10 lowest-signal feature request | planned |
| 3350 | Ability to play scene directly from the scenes grid page | R9/R10 lowest-signal feature request | planned |
| 3296 | Ability to select multiple studios | R9/R10 lowest-signal feature request | planned |
| 3281 | Smarter Tagging Overhaul | R9/R10 lowest-signal feature request | planned |
| 3272 | Sort scenes by studio or performer rating from scenes page | R9/R10 lowest-signal feature request | planned |
| 3232 | Merge tag to scene button & functionality for performers and studios t | R9/R10 lowest-signal feature request | planned |
| 3220 | Option to convert scene to fileless scene if attached primary file is  | R9/R10 lowest-signal feature request | planned |
| 3200 | Tag colors | R9/R10 lowest-signal feature request | planned |
| 3130 | Scene/Performer Tagger Configuration UI Refactor | R9/R10 lowest-signal feature request | planned |
| 3078 | Add Quit Stash option from the Web UI | R9/R10 lowest-signal feature request | planned |
| 3074 | Content recommendations based on recent history | R9/R10 lowest-signal feature request | planned |
| 2954 | Generate marker preview immediately after marker is created | R9/R10 lowest-signal feature request | planned |
| 2944 | Add user interface to move files using `moveFiles` | R9/R10 lowest-signal feature request | planned |
| 2914 | Ability to to configure delay/rate limit on per scraper basis | R9/R10 lowest-signal feature request | planned |
| 2913 | Queue Looping | R9/R10 lowest-signal feature request | planned |
| 2905 | Ability to pre-trancode video file and store it for playback in lower  | R9/R10 lowest-signal feature request | planned |
| 2903 | Ability to check stash-box matches based on last updated date in scene | R9/R10 lowest-signal feature request | planned |
| 2879 | Add settings option to hide scene tabs sidebar by default | R9/R10 lowest-signal feature request | planned |
| 2866 | Show full path for scenes in scene tagger | R9/R10 lowest-signal feature request | planned |
| 2858 | Save selected scraper inside saved filter on scene tagger | R9/R10 lowest-signal feature request | planned |
| 2836 | Add O-Counter to Galleries and allow Sorting Galleries by O-Counter | R9/R10 lowest-signal feature request | planned |
| 2816 | Query in Scene Tagger using StashID | R9/R10 lowest-signal feature request | planned |
| 2814 | Follow XDG directory specifications | R9/R10 lowest-signal feature request | planned |
| 2808 | Add additional flag to objects to safeguard against deleting favorite  | R9/R10 lowest-signal feature request | planned |
| 2780 | Transfer metadata between linked galleries and scenes | R9/R10 lowest-signal feature request | planned |
| 2736 | Allow creating a tag from anywhere tags can be added | R9/R10 lowest-signal feature request | planned |
| 2723 | Create second database within the program and fast switch between data | R9/R10 lowest-signal feature request | planned |
| 2680 | Option to Scope Scene Filename Parser to Specific Studio | R9/R10 lowest-signal feature request | planned |
| 2651 | Ability to Merge existing Performers in Scene Scrape Results modal | R9/R10 lowest-signal feature request | planned |
| 2647 | Ability to create a playlist from multiple markers | R9/R10 lowest-signal feature request | planned |
| 2636 | Backup the Stash config | R9/R10 lowest-signal feature request | planned |
| 2626 | Add Missing Scraper Fields | R9/R10 lowest-signal feature request | planned |
| 2542 | Allow `Esc` key to close any and all modal windows | R9/R10 lowest-signal feature request | planned |
| 2538 | Support mobile controls inside lightbox | R9/R10 lowest-signal feature request | planned |
| 2523 | Ability to change default quality when using live transcode | R9/R10 lowest-signal feature request | planned |
| 2511 | Create virtual files for compilations | R9/R10 lowest-signal feature request | planned |
| 2507 | Support performer alias(es) in Auto Tag task | R9/R10 lowest-signal feature request | planned |
| 2494 | Option to reset metadata on an object | R9/R10 lowest-signal feature request | planned |
| 2492 | Sort scenes by date when they were rated | R9/R10 lowest-signal feature request | planned |
| 2359 | [Meta] Update Stash to be inline with Stash-Box | R9/R10 lowest-signal feature request | planned |
| 2297 | Include studio value in Scrape query search string | R9/R10 lowest-signal feature request | planned |
| 2186 | Show stats for selected items | R9/R10 lowest-signal feature request | planned |
| 2185 | Set Image for Performers via images/gallery | R9/R10 lowest-signal feature request | planned |
| 2182 | Save Filter as Tag | R9/R10 lowest-signal feature request | planned |
| 2178 | Add language selector to the setup process | R9/R10 lowest-signal feature request | planned |
| 2170 | Set Scene Image using linked gallery images | R9/R10 lowest-signal feature request | planned |
| 2165 | Auto-populate metadata between scene/gallery relationship | R9/R10 lowest-signal feature request | planned |
| 2160 | Ability to flip/mirror scenes in player on horizontal axis | R9/R10 lowest-signal feature request | planned |
| 2123 | Improve Gallery Tab in Scene Details Page | R9/R10 lowest-signal feature request | planned |
| 2121 | Add markers tab to performer page | R9/R10 lowest-signal feature request | planned |
| 2082 | Ability to upload favicons to studios | R9/R10 lowest-signal feature request | planned |
| 2078 | Improve and add more classnames to elements in card-sections | R9/R10 lowest-signal feature request | planned |
| 2055 | Extend Auto Tag task sources by title and description | R9/R10 lowest-signal feature request | planned |
| 2048 | Custom URL mapping for a single URL | R9/R10 lowest-signal feature request | planned |
| 2045 | Scraper option to choose the encoding of the URL | R9/R10 lowest-signal feature request | planned |
| 1981 | Update performer scraper interface to parity with scenes | R9/R10 lowest-signal feature request | planned |
| 1723 | Show Breadcrumb for Nested Tags | R9/R10 lowest-signal feature request | planned |
| 1680 | Calculate automatic ratings for Performers, Tags & Groups based on rel | R9/R10 lowest-signal feature request | planned |
| 1656 | Ability to mark scenes as not duplicate for scene duplicate checker | R9/R10 lowest-signal feature request | planned |
| 1624 | Add oragnized flag to groups (aka movies) | R9/R10 lowest-signal feature request | planned |
| 1599 | Ability to Merge existing tags in Scene Scrape Results modal | R9/R10 lowest-signal feature request | planned |
| 1545 | Organize Scene Filter Dropdown with added category sections | R9/R10 lowest-signal feature request | planned |
| 1508 | Support Multiple Sorts and apply them consecutively | R9/R10 lowest-signal feature request | planned |
| 1495 | Split performer MEASUREMENTS value into distinct BUST, CUP, HIP, and W | R9/R10 lowest-signal feature request | planned |
| 1460 | Ability to add a tag to a specific performer on object pages instead o | R9/R10 lowest-signal feature request | planned |
| 1446 | Allow in-app renaming of scrapers | R9/R10 lowest-signal feature request | planned |
| 1400 | Ratings for Tags | R9/R10 lowest-signal feature request | planned |
| 1367 | Extend Performer/Scene Tagger for other scrapers | R9/R10 lowest-signal feature request | planned |
| 1365 | Contextual filtering across all objects | R9/R10 lowest-signal feature request | planned |
| 1341 | Global option to only accept StashID in Scene Tagger | R9/R10 lowest-signal feature request | planned |
| 1335 | Persistent scene filters settings | R9/R10 lowest-signal feature request | planned |
| 1317 | Global Filters (aka Libraries) | R9/R10 lowest-signal feature request | planned |
| 1296 | Ability to parse a list of tags to add them in bulk to an object | R9/R10 lowest-signal feature request | planned |
| 1280 | Ability to configure default tab for object that have attached files | R9/R10 lowest-signal feature request | planned |
| 1246 | Search Bar for Settings | R9/R10 lowest-signal feature request | planned |
| 1220 | Ability to lookup similar scenes by PHASH in scene duplicate checker | R9/R10 lowest-signal feature request | planned |
| 1182 | Add "Test" button to "Chrome CDP Path" config option | R9/R10 lowest-signal feature request | planned |
| 1173 | In-app editor for config.yml file | R9/R10 lowest-signal feature request | planned |
| 1171 | Add clear logs button | R9/R10 lowest-signal feature request | planned |
| 1170 | Add organized flag to performers | R9/R10 lowest-signal feature request | planned |
| 1129 | Browse and filter scenes by alphabet letter  | R9/R10 lowest-signal feature request | planned |
| 1127 | Support for sub-galleries | R9/R10 lowest-signal feature request | planned |
| 1125 | Upload image from the UI | R9/R10 lowest-signal feature request | planned |
| 1111 | Checkbox to enable case insensitive RegEx filtering | R9/R10 lowest-signal feature request | planned |
| 861 | Image Filename Parser tool | R9/R10 lowest-signal feature request | planned |
| 837 | Log potential issues with files and show in dedicated information hub | R9/R10 lowest-signal feature request | planned |
| 819 | Configure number of threads for ffmpeg transcodes | R9/R10 lowest-signal feature request | planned |
| 773 | Add tag view mode to display all tags in a single page | R9/R10 lowest-signal feature request | planned |
| 284 | Navigation using next/previous buttons for performers | R9/R10 lowest-signal feature request | planned |
| 7013 | Avoid unnecessary cover image updates when re-scraping scenes | R9/R10 lowest-signal feature request | planned |
| 6987 | Ability to reinstall plugin from installed plugins section | R9/R10 lowest-signal feature request | planned |
| 6982 | Ability to fast forward/rewind at 2x speed using long press | R9/R10 lowest-signal feature request | planned |
| 6970 | Add IS_NULL and NOT_NULL modifiers to duration field for Group filter | R9/R10 lowest-signal feature request | planned |
| 6955 | Settings option to enable automatic scrolling in wall view | R9/R10 lowest-signal feature request | planned |
| 6945 | Add Scenes field to ScrapedGroup schema | R9/R10 lowest-signal feature request | planned |
| 6899 | Add defaults for plugin settings | R9/R10 lowest-signal feature request | planned |
| 6871 | Add paths and behavior flags to findFolders() and findFiles() | R9/R10 lowest-signal feature request | planned |
| 6866 | Dynamically hide non applicable performer fields based on gender | R9/R10 lowest-signal feature request | planned |
| 6861 | Cover Image usability improvements + metadata | R9/R10 lowest-signal feature request | planned |
| 6837 | Ability to skip generation tasks on scenes where it failed | R9/R10 lowest-signal feature request | planned |
| 6816 | Reduce size of the action buttons on performer page | R9/R10 lowest-signal feature request | planned |
| 6811 | Show marker ranges on scene player control bar on mobile screens | R9/R10 lowest-signal feature request | planned |
| 6719 | GraphQL SFW shadowed query fields | R9/R10 lowest-signal feature request | planned |
| 6668 | Sort scenes by file by creation time in filesystem | R9/R10 lowest-signal feature request | planned |
| 6657 | Ability to generate group cover from attached scenes if no image is se | R9/R10 lowest-signal feature request | planned |
| 6645 | Reorganise generated artifacts | R9/R10 lowest-signal feature request | planned |
| 6516 | Add duplicated filter to galleries | R9/R10 lowest-signal feature request | planned |
| 6509 | Highlight AB loop points on the scene scrubber bar | R9/R10 lowest-signal feature request | planned |
| 6490 | Ability to open tag page on click instead of applying object-specific  | R9/R10 lowest-signal feature request | planned |
| 6457 | Update API to scan in file(s), add metadata on scan | R9/R10 lowest-signal feature request. **Scoped 2026-09-30, and clause (a) turned out to be already satisfied** — measured, not assumed. `walkDir` returns early when the root is not a directory and `queueFiles` SymWalks whatever paths it is given, so a single file inside a library is scannable today. **Clause (b) is the real gap**: `ScanMetadataInput` has no id or metadata field, the resolver takes only that input, and `manager.ScanMetadataInput` has only `Paths` + options + `Filter`. **Clause (b) is the real gap**: `ScanMetadataInput` has no id or metadata field, the resolver takes only that input, and `manager.ScanMetadataInput` has only `Paths` + options + `Filter` — that is an API design change, not a bug fix, and it is not started. **Silence was the real defect in clause (a), and it is fixed** (`70df72477`, then the fix commit). `getScanPaths` skipped a path matching no configured library and DISCARDED it, so a request naming one valid and one invalid path returned a job ID, scanned the valid one and reported success with nothing recording that the other was never looked at. **Decision: keep the skip lenient, return the skipped paths.** Erroring would break scripted callers that pass a superset and rely on the lenient reading, and #6457 never asked for a new error; but the skip is now returned from `getScanPaths` and reported by `ScanJob.Execute`, so the log line is no longer the only record. Verified by reverting the fix: the three reporting tests fail and the four positive controls keep passing. | planned |
| 6455 | SQL Query Improvments for Larger DB | R9/R10 lowest-signal feature request | planned |
| 6365 | Replace in-app manual with offline version of StashDocs | R9/R10 lowest-signal feature request | planned |
| 6360 | Ability to merge studios | R9/R10 lowest-signal feature request | planned |
| 6357 | Filter based on related objects | R9/R10 lowest-signal feature request | planned |
| 6335 | Allow tablet view to be full desktop view instead of mobile view | R9/R10 lowest-signal feature request | planned |
| 6121 | Display resolution of scraped images across all scraper modals | R9/R10 lowest-signal feature request | planned |
| 6052 | Ability to run Bulk Auto Tag from Tags page | R9/R10 lowest-signal feature request | planned |
| 6050 | Ability to start marker in AB Loop mode if it has start/stop time | R9/R10 lowest-signal feature request | planned |
| 6013 | sys.stdin.read function response is too long | R9/R10 lowest-signal feature request | planned |
| 5966 | Pattern Match Studio URLs | R9/R10 lowest-signal feature request | planned |
| 5944 | Include settings when executing plugin task | **CLOSED** `6d5a8131c` — see closed-issues.md | closed |
| 5942 | Ability to edit scene marker directly from scene card | R9/R10 lowest-signal feature request | planned |
| 5823 | Show 100% identical duplicates on Scene Duplicate Checker | R9/R10 lowest-signal feature request | planned |
| 5806 | Improve the layout for comparing existing and new values when scraping | R9/R10 lowest-signal feature request | planned |
| 5788 | [meta] Sorting Feature Requests | R9/R10 lowest-signal feature request | planned |
| 5786 | [meta] Duplicate Checker improvements | R9/R10 lowest-signal feature request | planned |
| 5785 | Settings option to display favourited tags first | R9/R10 lowest-signal feature request | planned |
| 5783 | Marker Preview Generation Options | R9/R10 lowest-signal feature request | planned |
| 5748 | Consider career start/end dates when dispalying performer age | R9/R10 lowest-signal feature request | planned |
| 5711 | Image relationships feature to allow linking related images  | R9/R10 lowest-signal feature request | planned |
| 5643 | Ability to report false-positive fingerprint back to stash-box instanc | R9/R10 lowest-signal feature request | planned |
| 5631 | `moveFiles` should optionally clean up empty directories | R9/R10 lowest-signal feature request | planned |
| 5627 | Ability to Merge existing Performer aliases in Performer Scrape Result | R9/R10 lowest-signal feature request | planned |
| 5625 | Centralized Automated Update Checker | R9/R10 lowest-signal feature request | planned |
| 5612 | Create a sharable link to give temporary access to a scene | R9/R10 lowest-signal feature request | planned |
| 5601 | Add Bluesky Icon for performer links | R9/R10 lowest-signal feature request | planned |
| 5596 | Client Side Hamming Distance Settings for Identify Tasks | R9/R10 lowest-signal feature request | planned |
| 5593 | Support multiple aliases for groups | R9/R10 lowest-signal feature request | planned |
| 5541 | Ability to manually trigger Auto Tag on single performer alias | R9/R10 lowest-signal feature request | planned |
| 5500 | Ability to filter scenes by scene_index | R9/R10 lowest-signal feature request | planned |
| 5498 | Add IMPORT option to import tags, galleries, performers, and studios | R9/R10 lowest-signal feature request | planned |
| 5460 | Ability to Pin/Favorite scrapers | R9/R10 lowest-signal feature request | planned |
| 5448 | Live marker preview | R9/R10 lowest-signal feature request | planned |
| 5444 | Add flag to scenes to exclude those performers from "Appear With" tab | R9/R10 lowest-signal feature request | planned |
| 5434 | Add icon/field to performer profile/card to indicate performer activit | R9/R10 lowest-signal feature request | planned |
| 5420 | Expose scene subtitles over DLNA | R9/R10 lowest-signal feature request | planned |
| 5400 | Open performer link directly if it only has single link of that type | R9/R10 lowest-signal feature request | planned |
| 5397 | Middle Align scene_index on scene cards | R9/R10 lowest-signal feature request | planned |
| 5394 | Add "Include sub-group content" toggle on groups page in scenes tab | R9/R10 lowest-signal feature request | planned |
| 5384 | Ability to add images and galleries to groups | R9/R10 lowest-signal feature request | planned |
| 5313 | Support deinterlacing during playback | R9/R10 lowest-signal feature request | planned |
| 5312 | Adding crop & pan for video filters | R9/R10 lowest-signal feature request | planned |
| 5275 | Option to flatten VR scene preview and markers previews | R9/R10 lowest-signal feature request | planned |
| 5273 | Enhancements to the Tags System, File Details and Settings | R9/R10 lowest-signal feature request | planned |
| 5210 | Support non-unique studio names | R9/R10 lowest-signal feature request | planned |
| 5159 | Support multiple filters of the same type | R9/R10 lowest-signal feature request | planned |
| 5144 | Add option to generate/scrub waveforms and display them alongside the  | R9/R10 lowest-signal feature request | planned |
| 5101 | Support for min/max stroke length slider for Handy integration | R9/R10 lowest-signal feature request | planned |
| 5094 | Multiple image support for all objects using corresponding user-define | R9/R10 lowest-signal feature request | planned |
| 5089 | Copy existing markers on newly created scene from split | R9/R10 lowest-signal feature request | planned |
| 5067 | Option to select highest bitrate in scene duplicate checker | R9/R10 lowest-signal feature request | planned |
| 4970 | Add Organized flag to all objects | R9/R10 lowest-signal feature request | planned |
| 4950 | Add Custom Image Numbering and Sorting for Galleries | R9/R10 lowest-signal feature request | planned |
| 4933 | Show related scenes directly on the Appears With page instead of just  | R9/R10 lowest-signal feature request | planned |
| 4917 | Support for file size scene filter | R9/R10 lowest-signal feature request | planned |
| 4916 | Ability to configure displayed tabs in Scene page | R9/R10 lowest-signal feature request | planned |
| 4831 | Breast/Cup Size Sort on Performers Page | R9/R10 lowest-signal feature request | planned |
| 4779 | Add more id and data attributes | R9/R10 lowest-signal feature request | planned |
| 4656 | Support for galleryByQueryFragment for image galleries | R9/R10 lowest-signal feature request | planned |
| 4651 | Global search bar | R9/R10 lowest-signal feature request | planned |
| 4642 | Allow playback of secondary files | R9/R10 lowest-signal feature request | planned |
| 4594 | Ability to define Clips/Sub-Scenes from Scenes | R9/R10 lowest-signal feature request | planned |
| 4556 | Prevent installing multiple themes simultaneously  | R9/R10 lowest-signal feature request | planned |
| 4523 | Add new field to support performer role in relation to the scene | R9/R10 lowest-signal feature request | planned |
| 4513 | Ability to sort and/or filter library list | R9/R10 lowest-signal feature request | planned |
| 4499 | Sort performers by play duration | R9/R10 lowest-signal feature request | planned |
| 4467 | Ability to scrape scene markers | R9/R10 lowest-signal feature request | planned |
| 4465 | Task Queue Improvements | R9/R10 lowest-signal feature request | planned |
| 4457 | Scene Tagger regex specify replacement instead of space in blacklist f | R9/R10 lowest-signal feature request | planned |
| 4433 | Ability to set settings in scraper yaml configuration file | R9/R10 lowest-signal feature request | planned |
| 4409 | Hardlink Duplicates in scene duplicate checker | R9/R10 lowest-signal feature request | planned |
| 4384 | Multiple image support for tags | R9/R10 lowest-signal feature request | planned |
| 4366 | Run backup as a queued task | R9/R10 lowest-signal feature request | planned |
| 4326 | Ability to browse related content during video playback without leavin | R9/R10 lowest-signal feature request | planned |
| 4306 | Allow user to define TagFilterType for various input forms | R9/R10 lowest-signal feature request | planned |
| 4239 | Make "Scrape With" (on scene page) function identically to "Scene Tagg | R9/R10 lowest-signal feature request | planned |
| 4221 | Make internal scene ID a searchable field | R9/R10 lowest-signal feature request | planned |
| 4219 | Add/expose `ID` tags on HTML elements | R9/R10 lowest-signal feature request | planned |
| 4175 | Ability to change default live transcode method | R9/R10 lowest-signal feature request | planned |
| 4174 | Filter to exclude by duration in scene duplicate checker | R9/R10 lowest-signal feature request | planned |
| 4168 | Allow user to define a custom start/end time per scene | R9/R10 lowest-signal feature request | planned |
| 4167 | Support multiple studio codes | R9/R10 lowest-signal feature request | planned |
| 4115 | Support HEIC image format | R9/R10 lowest-signal feature request | planned |
| 4084 | Show visual indication if scene tagger returns multiple results | R9/R10 lowest-signal feature request | planned |
| 4083 | AHash fingerprint support | R9/R10 lowest-signal feature request | planned |
| 4080 | Run tasks from system tray | R9/R10 lowest-signal feature request | planned |
| 4077 | Filter menu: Add rename option; Consolidate icon buttons into vertical | R9/R10 lowest-signal feature request | planned |
| 4070 | Ability to scan image clips from archives | R9/R10 lowest-signal feature request | planned |
| 4067 | Ability to generate scene markers immediately after creation | R9/R10 lowest-signal feature request | planned |
| 4042 | Allow selective tasks input to parse delineated list of directories | R9/R10 lowest-signal feature request | planned |
| 4041 | Checking for StashID in scene duplicate checker | R9/R10 lowest-signal feature request | planned |
| 3994 | Filter for Is Image Clip: True/False | R9/R10 lowest-signal feature request | planned |
| 3967 | Display zoom level percentage inside a lightbox | R9/R10 lowest-signal feature request | planned |
| 3962 | Ability to favorite scenes, images and galleries | R9/R10 lowest-signal feature request | planned |
| 3950 | Hide header and footer inside lightbox by default | R9/R10 lowest-signal feature request | planned |
| 3949 | Add a flag to hide individual images on specific galleries | R9/R10 lowest-signal feature request | planned |
| 3923 | Ability to play and navigate markers from a playlist | R9/R10 lowest-signal feature request | planned |
| 3871 | Ability to add custom country/state codes for Country field | R9/R10 lowest-signal feature request | planned |
| 3861 | Localization setting to display units in either metric, imperial units | R9/R10 lowest-signal feature request | planned |
| 3855 | Change O-Counter value via keybaord shortcut during video playback | R9/R10 lowest-signal feature request | planned |
| 3819 | Make lightbox slideshow options aware of image clips that have duratio | R9/R10 lowest-signal feature request | planned |
| 3773 | Persistent settings/toggles via cookies | R9/R10 lowest-signal feature request | planned |
| 3750 | Ability to set viewport dimensions for CDP scraper | R9/R10 lowest-signal feature request | planned |
| 3749 | .forceGallery metadata & include scenes on scan | R9/R10 lowest-signal feature request | planned |
| 3693 | Smart Tags (Organized Saved Filters) | R9/R10 lowest-signal feature request | planned |
| 3637 | Add Aspect Ratio to scene File Info tab | R9/R10 lowest-signal feature request | planned |
| 3573 | Exclude directories in scene duplicate checker | R9/R10 lowest-signal feature request | planned |
| 3531 | Exclude organized scenes from scene duplicate checker | R9/R10 lowest-signal feature request | planned |
| 3529 | Create new object for characters | R9/R10 lowest-signal feature request | planned |
| 3486 | Option to performer scheduled automatic database backups | R9/R10 lowest-signal feature request | planned |
| 3485 | Perform database integrity check on startup | R9/R10 lowest-signal feature request | planned |
| 3478 | Recent Activity sorts for Performers, Studios, and Tags | R9/R10 lowest-signal feature request | planned |
| 3450 | Ability to use relative dates for date-specific filters | R9/R10 lowest-signal feature request | planned |
| 3431 | Boolean Filter Wrappers | R9/R10 lowest-signal feature request | planned |
| 3336 | metadataScan flag to disable/ignore hooks | R9/R10 lowest-signal feature request | planned |
| 3322 | Increase the checkbox size for selecting multiple items in scene dupli | R9/R10 lowest-signal feature request | planned |
| 3312 | Dynamically load more rows on front page when reaching the end of item | R9/R10 lowest-signal feature request | planned |
| 3266 | Add alias field keyword searching for groups | R9/R10 lowest-signal feature request | planned |
| 3253 | Video.js Tags Quick Add Dialouge | R9/R10 lowest-signal feature request | planned |
| 3237 | Request sub-task within Generate to run Identify on a file | R9/R10 lowest-signal feature request | planned |
| 3221 | Add a button inside toasts to quickly undo accidentally created tags | R9/R10 lowest-signal feature request | planned |
| 3197 | Add ratings to markers | R9/R10 lowest-signal feature request | planned |
| 3189 | Fallback to scene cover in Duplicate Checker tool if no scrubber sprit | R9/R10 lowest-signal feature request | planned |
| 3107 | DLNA legacy folders | R9/R10 lowest-signal feature request | planned |
| 3038 | Optimize/compress images in both generated folder and database | R9/R10 lowest-signal feature request | planned |
| 3001 | Adding `File.Destroy.Post` hook | R9/R10 lowest-signal feature request | planned |
| 2960 | Make "Create galleries from folders containing images" more robust to  | R9/R10 lowest-signal feature request | planned |
| 2956 | Add the ability to sort Images and Galleries by Performer Age | R9/R10 lowest-signal feature request | planned |
| 2871 | Move task queue to a nav bar icon popover | R9/R10 lowest-signal feature request | planned |
| 2770 | Inject metadata into raw streams (for external players) | R9/R10 lowest-signal feature request | planned |
| 2722 | Gallery Filename Parser tool | R9/R10 lowest-signal feature request | planned |
| 2679 | Option to choose Studio during selective scan | R9/R10 lowest-signal feature request | planned |
| 2479 | Add tag to objects based on scraping method | R9/R10 lowest-signal feature request | planned |
| 2463 | Improve readability when logging to shell | R9/R10 lowest-signal feature request | planned |
| 2420 | Collapse button for excluded tag patterns | R9/R10 lowest-signal feature request | planned |
| 2399 | Scene Tagger - Add option to exclude specific metadata fields from sea | R9/R10 lowest-signal feature request | planned |
| 2381 | Add DuplicateChecker context to on delete hook | R9/R10 lowest-signal feature request | planned |
| 2350 | Flag previously deleted files | R9/R10 lowest-signal feature request | planned |
| 2318 | Add ability to ignore more fields when using scene tagger | R9/R10 lowest-signal feature request | planned |
| 2305 | Dedicated Saved Filter list for tagger view | R9/R10 lowest-signal feature request | planned |
| 2248 | Move logging prefix to its own HTML element | R9/R10 lowest-signal feature request | planned |
| 2227 | "Generate thumbnail" should update scene cover image immediately | R9/R10 lowest-signal feature request | planned |
| 2152 | De-duplicating auto-taggable strings | R9/R10 lowest-signal feature request | planned |
| 2142 | Change thumbnail slider to button | R9/R10 lowest-signal feature request | planned |
| 2085 | Link parts of a compilation to their original scenes with markers | R9/R10 lowest-signal feature request | planned |
| 2080 | Backup functionality should not work concurrently to other tasks | R9/R10 lowest-signal feature request | planned |
| 2067 | Scan task button label change | R9/R10 lowest-signal feature request | planned |
| 1896 | Remember sort/filter settings when navigating outside the page | R9/R10 lowest-signal feature request | planned |
| 1828 | Getting Phash with a hook plugin | R9/R10 lowest-signal feature request | planned |
| 1811 | Faster/more intuitive UI/UX for adding to galleries | R9/R10 lowest-signal feature request | planned |
| 1732 | Tag tree view mode for Nested Tags | R9/R10 lowest-signal feature request | planned |
| 1695 | Display plugin errors as toasts | R9/R10 lowest-signal feature request | planned |
| 1652 | Improve/modernize consistency of formatting in scene detail | R9/R10 lowest-signal feature request | planned |
| 1585 | Convert thumbnails to support progressive image loading or support laz | R9/R10 lowest-signal feature request | planned |
| 1459 | Add multiple new movies with url list | R9/R10 lowest-signal feature request | planned |
| 1445 | Resume Interrupted Task Queue | R9/R10 lowest-signal feature request | planned |
| 1334 | Unified Scene and Image View on Performer page | R9/R10 lowest-signal feature request | planned |
| 1290 | Ability to Move Between Media in Collection | R9/R10 lowest-signal feature request | planned |
| 1183 | Add `{studioName}` and `{studioURL}` placeholder fields for sceneByFra | R9/R10 lowest-signal feature request | planned |
| 1165 | Filter the stash-box query based on existing metadata | R9/R10 lowest-signal feature request | planned |

## Not planned — deferred by rule (93)

| # | Title | Why | Verdict |
|---|---|---|---|
| 12 | Account system | R8 no home in this codebase | deferred |
| 233 | Support additional gallery archive formats (7z, gz, tar.gz) | R7 one issue implies a whole subsystem | deferred |
| 428 | Import/Export scene metadata to/from same folder | R2 a client app or extension, not this codeb | deferred |
| 700 | Make in-app help section more noticeable to new users | R8 no home in this codebase | deferred |
| 1006 | PDF Support | R7 one issue implies a whole subsystem | deferred |
| 1028 | Stash built for a particular type of conventual studio-produced porn,  | R6 a product decision, not an issue | deferred |
| 1052 | Keyboard shortcut to create scene marker while watching a video | R1 upstream labels it a plugin idea, not cor | deferred |
| 1058 | Ability to select different audio track during playback | R8 no home in this codebase | deferred |
| 1161 | Option to link scenes to galleries if they are in the same folder | R1 upstream labels it a plugin idea, not cor | deferred |
| 1258 | Support audio files/object type | R3 a new first-class object: schema, GQL, UI | deferred |
| 1259 | Support text files/object type | R3 a new first-class object: schema, GQL, UI | deferred |
| 1385 | Semantic FUSE filesystem to mount organised version of contents of lib | R8 no home in this codebase | deferred |
| 1464 | Feed / Editing View for Collections (Galleries, Images, Scenes, etc) | R1 upstream labels it a plugin idea, not cor | deferred |
| 1580 | DLNA folders Recently added, Recently viewed, Unplayed | R2 a client app or extension, not this codeb | deferred |
| 1659 | Add a Manga/Doujin section | R3 a new first-class object: schema, GQL, UI | deferred |
| 1863 | Child studios should Inherit posters from their parent | R1 upstream labels it a plugin idea, not cor | deferred |
| 1914 | Option to display linked scene title instead of gallery title | R1 upstream labels it a plugin idea, not cor | deferred |
| 1924 | i18n: Streamline Singular/Plural Nouns | R5 translating the UI, not fixing it | deferred |
| 1939 | Ability to export selected scenes to a .m3u playlist file | R1 upstream labels it a plugin idea, not cor | deferred |
| 2032 | Set tag image based on a marker image or scene image | R1 upstream labels it a plugin idea, not cor | deferred |
| 2094 | Automatically delete duplicate scenes based on pre-defined rules | R1 upstream labels it a plugin idea, not cor | deferred |
| 2276 | Handling multi-part scenes, "part X" field and/or UI indicator? | R5 translating the UI, not fixing it | deferred |
| 2296 | Support for scene extras similar Plex's movie extras | R2 a client app or extension, not this codeb | deferred |
| 2337 | Support multiple users with configurable permissions | R8 no home in this codebase | deferred |
| 2397 | Add additional metrics about video quality in Duplicate Checker | R1 upstream labels it a plugin idea, not cor | deferred |
| 2528 | Show badge on Performer card in Scene Details Page if performer has St | R1 upstream labels it a plugin idea, not cor | deferred |
| 2548 | Select .zip files in Selective Scan | R8 no home in this codebase | deferred |
| 2645 | Custom playback speed (or just bring 4x back) | R1 upstream labels it a plugin idea, not cor | deferred |
| 2662 | Create menu item for Interactive Options in settings | R7 one issue implies a whole subsystem | deferred |
| 2719 | Integrated way to import embedded metadata from JPEG | R1 upstream labels it a plugin idea, not cor | deferred |
| 2734 | Achievements | R1 upstream labels it a plugin idea, not cor | deferred |
| 2742 | Studio logo on cards visibility options | R1 upstream labels it a plugin idea, not cor | deferred |
| 2747 | Jellyfin-like external remote player support | R2 a client app or extension, not this codeb | deferred |
| 2762 | Ability to manually pause interactive funscript from the scene player | R7 one issue implies a whole subsystem | deferred |
| 2902 | Add option for images to inherit metadata from the gallery they are in | R1 upstream labels it a plugin idea, not cor | deferred |
| 2942 | Parse scene details field when using auto tag task | R1 upstream labels it a plugin idea, not cor | deferred |
| 3031 | Funscript related ideas/goals | R7 one issue implies a whole subsystem | deferred |
| 3073 | Allow downloading/caching of video(s) for offline use | R2 a client app or extension, not this codeb | deferred |
| 3077 | Advanced SubStation Alpha (ASS) subtitle format support | R8 no home in this codebase | deferred |
| 3135 | Expose Saved Filters over DLNA | R2 a client app or extension, not this codeb | deferred |
| 3219 | Add toggles in settings to hide fields on edit pages | R1 upstream labels it a plugin idea, not cor | deferred |
| 3238 | Allow different time units for minimum play percent option | R8 no home in this codebase | deferred |
| 3250 | Add ratings directly to the scene player | R1 upstream labels it a plugin idea, not cor | deferred |
| 3303 | Remove `^https?:\/\/(www\.)?` and `\/$` from all <a> HTML tags display | R1 upstream labels it a plugin idea, not cor | deferred |
| 3364 | Tagging Flow/Interface to facilitate content tagging | R1 upstream labels it a plugin idea, not cor | deferred |
| 3382 | Expose Named Capture Groups in Scene filename parser | R1 upstream labels it a plugin idea, not cor | deferred |
| 3384 | Ungreedy Repetitions in Scene filename parser | R1 upstream labels it a plugin idea, not cor | deferred |
| 3481 | Import tags from sidecar text files | R1 upstream labels it a plugin idea, not cor | deferred |
| 3505 | Similar Performers Tab | R1 upstream labels it a plugin idea, not cor | deferred |
| 3602 | Add scene field to track uncredited performers | R1 upstream labels it a plugin idea, not cor | deferred |
| 3625 | Support funscripts for Kiiroo interactive toys | R8 no home in this codebase | deferred |
| 3685 | Custom menu items for saved scene/marker filters | R1 upstream labels it a plugin idea, not cor | deferred |
| 3734 | Setting to replace scene cover with sprites used for scene scrubber  | R1 upstream labels it a plugin idea, not cor | deferred |
| 3738 | Update "Updated At" date when funscript is attached to the scene | R7 one issue implies a whole subsystem | deferred |
| 3809 | Distribute as Flatpak on Linux | R8 no home in this codebase | deferred |
| 3824 | Display most common scene tags per performer | R1 upstream labels it a plugin idea, not cor | deferred |
| 3866 | User configurable keyboard shortcuts for VideoJS | R1 upstream labels it a plugin idea, not cor | deferred |
| 3875 | Extract and display embedded subtitles | R8 no home in this codebase | deferred |
| 4002 | Associate audio files used by e-stim toys to matching scenes | R3 a new first-class object: schema, GQL, UI | deferred |
| 4019 | Sync or Export/Import across devices | R4 object sync: an architecture, not a fix | deferred |
| 4586 | Save default caption settings / Language rulesets | R7 one issue implies a whole subsystem | deferred |
| 4589 | Custom label for captions/subtitles | R7 one issue implies a whole subsystem | deferred |
| 4640 | Add Manga (2-image) layout option to Image lightbox | R3 a new first-class object: schema, GQL, UI | deferred |
| 4739 | Inherit tag images from tagged images | R1 upstream labels it a plugin idea, not cor | deferred |
| 4771 | Ability to set subtitle offset | R7 one issue implies a whole subsystem | deferred |
| 4816 | Add play count to scene cards | R2 a client app or extension, not this codeb | deferred |
| 4985 | Ability to search/query the text of caption files | R7 one issue implies a whole subsystem | deferred |
| 4995 | Ability to Upload Videos/Images from the GraphQL API | R2 a client app or extension, not this codeb | deferred |
| 5399 | Ability to add icons/favicons for performer links | R1 upstream labels it a plugin idea, not cor | deferred |
| 5450 | Watchlist queue | R2 a client app or extension, not this codeb | deferred |
| 5468 | Option to link galleries to scenes if they are in the same folder | R1 upstream labels it a plugin idea, not cor | deferred |
| 5514 | Multi-language entries | R5 translating the UI, not fixing it | deferred |
| 5532 | Main page horizontal customization | R1 upstream labels it a plugin idea, not cor | deferred |
| 5534 | Offload Thumbnail Generation to External Computer | R7 one issue implies a whole subsystem | deferred |
| 5646 | Add new /temp/ Application Path instead of using /generated/temp/ for  | R8 no home in this codebase | deferred |
| 5650 | Support for funscript tokens (aka DRM) | R8 no home in this codebase | deferred |
| 5795 | Add URLs to scenes "Details" tab | R1 upstream labels it a plugin idea, not cor | deferred |
| 6008 | Support for scanning Encryped Archives | R8 no home in this codebase | deferred |
| 6193 | Ability to Skip Preview Generation based on set duration threshold | R8 no home in this codebase | deferred |
| 6218 | Ability to re-order performers in scene cards | R1 upstream labels it a plugin idea, not cor | deferred |
| 6254 | Inherit tag images from tagged scenes | R1 upstream labels it a plugin idea, not cor | deferred |
| 6311 | Multi axis Funscript and Serial Connections (OSR2+ & SR6) | R5 translating the UI, not fixing it | deferred |
| 6339 | Filter for multi-axis interactive scenes | R5 translating the UI, not fixing it | deferred |
| 6459 | Expand scene captions filter to support all languages | R7 one issue implies a whole subsystem | deferred |
| 6564 | Stash Object Sync Project | R4 object sync: an architecture, not a fix | deferred |
| 6579 | Support funscripts for AutoBlow interactive toys | R7 one issue implies a whole subsystem | deferred |
| 6672 | X-Ray Style Performer Overlay in Fullscreen Image & Video Viewer | R1 upstream labels it a plugin idea, not cor | deferred |
| 6744 | Option to check specific folder for subtitles | R7 one issue implies a whole subsystem | deferred |
| 6860 | Allow setting default tab on studio details page on a per-studio basis | R1 upstream labels it a plugin idea, not cor | deferred |
| 6883 | Log X-Real-IP from reverse proxy | R8 no home in this codebase | deferred |
| 7139 | Lack of right-click paste option in multi-edit boxes like Tags, Perfor | R5 translating the UI, not fixing it | deferred |
| 7250 | Use AI technology to tag videos, marking different sexual positions an | R1 upstream labels it a plugin idea, not cor | deferred |
| 7253 | Add Kiswahili (Swahili, sw-KE) language translation | R5 translating the UI, not fixing it | deferred |

## Not planned — cut by signal (132)

No maintainer label, no discussion, recent. **Any of these can be moved back
with one word** — the roster records them so the cut is reversible rather
than silent.

| # | Title | Why | Verdict |
|---|---|---|---|
| 7262 | Sorting by file size should be clearer | R10 | not-planned |
| 7175 | Ability to attach performer to a group | R10 | not-planned |
| 7134 | Watch Later | R10 | not-planned |
| 7132 | Performer evolution | R10 | not-planned |
| 7071 | Add VR specific fields to `VideoFile` | **CLOSED** upstream by PR #7048 (open) — see closed-issues.md (was mis-ticketed *not-planned*) | closed |
| 7058 | Do not pre-select scene match when multiple scenes are found in scene  | R10 | not-planned |
| 7020 | Image view counter | R10 | not-planned |
| 7258 | Improve scraper discoverability | R10 | not-planned |
| 7230 | Display ZIP compression method in gallery file info, and optionally al | R10 | not-planned |
| 7228 | Image scrapers should be able to return a `Galleries` field | R10 | not-planned |
| 7200 | Warn about overlapping URL patterns in scrapers | R10 | not-planned |
| 7197 | Add plugin media-src CSP support | **CLOSED** `b14aef421` — see closed-issues.md (was mis-ticketed *not-planned*) | closed |
| 7194 | Show free/available disk space on the Statistics page | R10 | not-planned |
| 7192 | Option to disable automatic file hash merging for Images/Galleries and | R10 | not-planned |
| 7165 | Plugin settings should be able to add connect-src CSP sources | **CLOSED** `c71899e7f` via PR #7166 — see closed-issues.md (was mis-ticketed *not-planned*) | closed |
| 7160 | [UI/UX] Organized icon state is hard to distinguish on scene detail pa | R10 | closed |
| 7157 | Option to play next scene after deleting | R10 | not-planned |
| 7118 | Duration filtering for scene identification | R10 | not-planned |
| 7086 | Add `{inputName}` placeholder for searchByName scrapers | R10 | not-planned |
| 7068 | Secondary sort issue when sorting by descending date | R10 | not-planned |
| 7052 | Link O history to multiple scenes (O Assists) | R10 | not-planned |
| 7007 | Official Docker image with hardware acceleration support | R10 | not-planned |
| 6979 | Ability to add tags to exclusion list from tags page | R10 | not-planned |
| 6956 | Improve "Clear Date Data" popups | R10 | not-planned |
| 6953 | Improve dense scene marker readability | R10 | not-planned |
| 6944 | Add/Remove/Reorder scenes from Group page | R10 | not-planned |
| 6925 | Performer Age vs. Birthday Sort | R10 | not-planned |
| 6875 | Enter troubleshooting mode via ENV/ argument | R10 | not-planned |
| 6874 | safe catch/ bypass of PluginApi.patch | R10 | not-planned |
| 6873 | Add Image(s) to scrapeSingleTag | R10 | not-planned |
| 6864 | Move disableAnimation lightbox option from GraphQL to UI config | R10 | not-planned |
| 6823 | Standardize Buttons on Performers Cards | R10 | not-planned |
| 6793 | Add similar markers filters that exist on scenes | R10 | not-planned |
| 6781 | [epic] src/ui dependency update spree | R10 | not-planned |
| 6745 | Ability to link scraped studio to existing studio from scrape dialog w | R10 | not-planned |
| 6677 | Log date to history tab when tag was applied to an object | R10 | not-planned |
| 6576 | Ability to filter images based on gallery rating | R10 | not-planned |
| 6544 | "Group By" View mode with collapsible sections | R10 | not-planned |
| 6539 | Scraper postprocess option to decode HTML entities to Unicode | R10 | not-planned |
| 6460 | Add configuration option on scene tagger to set studio code | R10 | not-planned |
| 6446 | Make thumbnail placeholders more informative by mentioning the task re | R10 | not-planned |
| 6430 | Option to merge metadata and delete files in a single task in scene Du | R10 | not-planned |
| 6429 | Improve visual clarity on merge direction in scene duplicate checker | R10 | not-planned |
| 6422 | Option to apply scene filters only to primary file | R10 | not-planned |
| 6394 | Allow scrapers to return custom fields | R10 | not-planned |
| 6383 | Make the selectable item area bigger in scene duplciate checker | R10 | not-planned |
| 6382 | Add option to select every file by path in scene Duplicate Checker | R10 | not-planned |
| 6283 | Ability to arbitrarily group/organize scrapers | R10 | not-planned |
| 6274 | [meta] "short form" video content | R10 | not-planned |
| 6231 | Add keyboard shortcut to trigger "Merge..." action from scenes page | R10 | not-planned |
| 6183 | Redirect old scene page to merged scene page after merge | R10 | not-planned |
| 6112 | Support for sub-scraper post process option for scrapeJson scrapers | R10 | not-planned |
| 6088 | [meta] O-Counter feature requests | R10 | not-planned |
| 6078 | Add studio code field for Groups | R10 | not-planned |
| 6076 | Add option to sort sub-groups by number of the sub-group item | R10 | not-planned |
| 6062 | Permit GraphQL API to update video captions | R10 | not-planned |
| 6057 | Add O-Counter to markers | R10 | not-planned |
| 6045 | Ability to set external image as gallery cover | R10 | not-planned |
| 5998 | Ability to configure specific custom fields to be hidden from object p | R10 | not-planned |
| 5992 | Add "Add Child Tag" and "Add Parent Tag" Buttons in Single Tag View | R10 | not-planned |
| 5970 | GQL sorting by custom field value | R10 | not-planned |
| 5939 | Ability to run scene filename parser on selected scenes | R10 | not-planned |
| 5893 | Filter by File Modification Time for Scenes and Images | R10 | not-planned |
| 5873 | Filter Markers tab by "Is missing thumbnail/preview" | R10 | not-planned |
| 5869 | Add gallery support to identify task | R10 | not-planned |
| 5819 | Set scene preview from file or URL | R10 | not-planned |
| 5792 | "Play Next" in marker view | R10 | not-planned |
| 5772 | Make director/photographer field a list | R10 | not-planned |
| 5750 | Ability to view markers based on secondary tags | R10 | not-planned |
| 5736 | Support Handy Firmware 4 | R10 | not-planned |
| 5667 | Add "Ignore Auto Tag" box to scraped performer create dialog | R10 | not-planned |
| 5626 | Main Nav Bar Notification System | R10 | not-planned |
| 5616 | Implement ratings keyboard shortcut for images in lightbox mode | R10 | not-planned |
| 5595 | Ability to edit scene number from scene tagger on scenes sub-page | R10 | not-planned |
| 5587 | Setting to sort Performer Credits in TabPanel & CardPopover | R10 | not-planned |
| 5584 | Video / "Image Clip" controls for lightbox | R10 | not-planned |
| 5536 | Migrate Scene Previews To Its Own Directory  | R10 | not-planned |
| 5518 | Tagger view for groups with batch tasks | R10 | not-planned |
| 5517 | Inhibit system sleep while tasks are running | R10 | not-planned |
| 5419 | Ability to set marker offset  | R10 | not-planned |
| 5412 | Scene Duplicate Checker report | R10 | not-planned |
| 5390 | Regenerate scene markers and scrubber sprites after merging | R10 | not-planned |
| 5364 | Show O-Count on gallery cards (using sum of image O-Counts) | R10 | not-planned |
| 5352 | Option to show full size images in gallery preview scrubber | R10 | not-planned |
| 5193 | Make O-Counter on Performer Card clickable | R10 | not-planned |
| 5185 | Treat videos in folders that have `.forcegallery` as image clips | R10 | not-planned |
| 5118 | Plugin Services available in Stash->Services tab | R10 | not-planned |
| 5107 | Ability to limit Auto Tag scraper in scene tagger to only apply to spe | R10 | not-planned |
| 4998 | Expand hook context to determine source | R10 | not-planned |
| 4919 | Create/link ScrapedStudio Parent Studio on Scrape | R10 | not-planned |
| 4860 | Add collapsible elements to identify modal | R10 | not-planned |
| 4853 | Ability to increase O-Count on all images inside a gallery with a sing | R10 | not-planned |
| 4834 | Editable saved filter names | R10 | not-planned |
| 4827 | Add Filter by Tag in scene duplicate checker tool | R10 | not-planned |
| 4823 | Add full list of editable fields in the edit modal for images | R10 | not-planned |
| 4756 | Ability to merge all the scenes with the same Stash ID in Scene Duplic | R10 | not-planned |
| 4746 | Mark Studio As Network | R10 | not-planned |
| 4698 | Track and display statistics about scene duplicate checker actions | R10 | not-planned |
| 4680 | O-Counter history for Images and Performers | R10 | not-planned |
| 4673 | Add data-values attributes to div.scene-specs-overlay span elements; a | R10 | not-planned |
| 4647 | Option to delete linked objects | R10 | not-planned |
| 4505 | Improved Tag Selection inside dropdowns | R10 | not-planned |
| 4458 | Scene Tagger blacklist regex enhancements | R10 | not-planned |
| 4418 | Plugin page disclaimer | R10 | not-planned |
| 4383 | Ability to filter Groups by Alias | R10 | not-planned |
| 4353 | Auto-save metadata fields that already include a confirmation | R10 | not-planned |
| 4351 | Add transformational filters to images | R10 | not-planned |
| 4332 | Make "exclusions" regex field with more user friendly UI | R10 | not-planned |
| 4318 | Scene gallery view | R10 | not-planned |
| 4231 | Set performer image from existing images | R10 | not-planned |
| 4207 | Ability to move queued tasks up/down to change priority | R10 | not-planned |
| 4160 | Pass more metadata for sceneByFragment scrapers | R10 | not-planned |
| 4155 | DLNA lists for extended rating system | R10 | not-planned |
| 3961 | Ability to configure default tab for each object page | R10 | not-planned |
| 3958 | Add gallery tagger view mode | R10 | not-planned |
| 3957 | Ability to merge details on individual fields in Scene Scrape Results  | R10 | not-planned |
| 3921 | Gallery update when galleries change from ZIP based to folder based | R10 | not-planned |
| 3917 | Include short "Scenes" (pre-clipped files) in the Marker browser by ta | R10 | not-planned |
| 3869 | Use videojs-vr player by default in VR tagged scenes & default videojs | R10 | not-planned |
| 3837 | Expand `VR tag` settings option to support multiple tags | R10 | not-planned |
| 3825 | Scene aliases for Performers "Jane Doe as Jane" | R10 | not-planned |
| 3769 | Auto Tag might be sped up by keeping tags in RAM | R10 | not-planned |
| 3711 | Replace Tags field dropdown with Checkbox Tree | R10 | not-planned |
| 3700 | 'Add to Gallery' Option for Selected Images | R10 | not-planned |
| 3694 | Allow creating `New` objects from subpages | R10 | not-planned |
| 3655 | Option to display tag image next to the tag name in card popovers | R10 | not-planned |
| 3651 | Configurable browser path and CLI arguments in Desktop Integration | R10 | not-planned |
| 3468 | Return more search results and restructure results presentation in per | R10 | not-planned |
| 3412 | Streamline the scene merging process in scene duplicate checker | R10 | not-planned |
| 3371 | Rename 'Merge' to 'Merge Metadata' in scene duplicate checker tool | R10 | not-planned |
| 3366 | Add metadata in multiple languages | R10 | not-planned |
| 3361 | Add POST support to scraper queries | R10 | not-planned |