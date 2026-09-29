# Capability taxonomy — the complete vocabulary

Choose EXACTLY ONE code per issue. Use the `intent` line, not the name, to decide.

C01 | Video container support | §5.2 | Accept mp4/mkv/webm/avi/mov/wmv and transcode-free preview.
C02 | Multi-scene single file | §5.2 | One file holding N scenes; split, or a parent clip with segments.
C03 | Image & gallery sets | §5.3 | Images, zips, folder galleries as first-class items.
C04 | Audio & music | §5.4 | Audio objects, waveform, music metadata, audio scrapers.
C05 | Comics / manga / doujin | §5.5 | CBZ/CBR/PDF page-based items, right-to-left reading, page ordering.
C06 | Funscript & interactive | §5.6 | Funscript play-along, sidecar discovery, in-browser sync.
C07 | Text, story posts, links | §5.7 | Text objects and external link items with metadata.
C08 | Performer interviews | §5.8 | Interview/talk format: ASR transcript, chapters, quotes, Q&A indexing.
C09 | Scene extras & related clips | §5.9 | Extras, BTS, trailers, behind-the-scenes as linked children.
C10 | Subtitles & captions | §5.10 | Sidecar/embedded subtitle support and playback integration.
C11 | Markers & chapters | §5.11 | Timeline markers persisting across transcodes and players.
C12 | Smart collections | §5.12 | Saved, dynamic, auto-updating queries as a first-class object.
C13 | Favourites / organized flag | §5.13 | Mark items organized/favourite; bulk-operate large sets.
C14 | Playlists & queues | §5.14 | Ordered lists of any item type, drag-reorder, shuffle.
C15 | Scanner throughput | §6.1 | Multi-GB and 100k+ file libraries, parallel scan, progress + cancel.
C16 | Incremental scan correctness | §6.2 | Move/rename detection, offline-drive resilience, no re-hash storms.
C17 | Background job engine | §6.3 | Durable queue: identify, generate, match, cluster, transcode.
C18 | Hardware decode for generation | §6.4 | VA-API/NVENC/QSV/VideoToolbox in generation and transcode tasks.
C19 | Transcode & proxy pipeline | §6.5 | On-demand proxy for exotic codecs; original always preserved.
C20 | Storage accounting | §6.6 | Per-item size, dedupe by hash, disk-space reporting.
C21 | Unsupervised identity clustering | §7.1 | Cluster faces across the library with no upstream source.
C22 | Performer identity merge/split | §7.2 | Manual merge, split, alias, disambiguation UI.
C23 | Performer fingerprints | §7.3 | Face/print fingerprints for cross-index identity matching.
C24 | Body/appearance similarity | §7.4 | Distinguish same person from lookalike/sibling via body embedding.
C25 | Self-service performer claim | §7.5 | Performer privately verifies a cluster, gains a dashboard.
C26 | Performer field model | §7.6 | Gender, ethnicity, nationality, measurements, career span, status.
C27 | Multi-valued attributes | §7.7 | Multi-select for breast type, ethnicity, nationality, genitalia.
C28 | Performer image sets & categories | §7.8 | Many images per performer, categorized, thumbnailed.
C29 | Performer death/status markers | §7.9 | Deceased, retired, inactive badges and filtering.
C30 | Stage/alias naming with studio | §7.10 | Aliases scoped to studio or era, validation against name.
C31 | Automatic career span | §7.11 | Derive first/last appearance from item dates, editable.
C32 | Group appearance credit | §7.12 | Mark cameos / non-sexual presence, per-item appearance type.
C33 | Typed field voting | §8.1 | Per-field proposals with weights, history, and merge.
C34 | Candidate generation | §8.2 | Auto-propose titles, descriptions, tags, studios from signals.
C35 | Reputation & trust | §8.3 | Weighted votes, decay, sybil damping, newcomer ramp.
C36 | Leaderboards & badges | §8.4 | Contribution ranking, badges, bounties, quests.
C37 | Field locking & moderation | §8.5 | Lock contested fields, steward review, dispute queue.
C38 | Edit history & rollbacks | §8.6 | Per-field history, revert, blame.
C39 | Alternate titles | §8.7 | Many titles per item with language and source.
C40 | Ratings & recommendations | §8.8 | User ratings, taste profiles, recommendations.
C41 | Studio model & extras | §8.9 | Studio aliases, URLs, ownership history, codes, auto-tags.
C42 | Release groups / scene groups | §8.10 | Group related items into releases, sets, franchises.
C43 | User-created lists | §8.11 | Shared and private lists, templates.
C44 | Content filtering & consent tiers | §8.12 | Category filters, consent-gated visibility, hide-lists.
C45 | Funder / subscription linking | §8.13 | Link and display funder/subscription profiles.
C46 | Filter UI redesign | §9.1 | One coherent filter model: facets, saved filters, URL state.
C47 | Full-text search breadth | §9.2 | Search across title, body, tags, performers, studio, transcript.
C48 | Smart search & typo tolerance | §9.3 | Fuzzy, phonetic, synonym and alias-aware matching.
C49 | Tag groups & attributes | §9.4 | Tag namespaces, tag groups, per-tag attributes and colors.
C50 | Folder-like organization | §9.5 | Virtual folders over query results, breadcrumbs.
C51 | Image/gallery organization | §9.6 | Per-image tagging, ratings, ordering, similarity search.
C52 | Similar-item detection | §9.7 | Near-duplicate items, re-encodes, same scene different file.
C53 | Recommendations engine | §9.8 | Content-based and collaborative suggestions.
C54 | Preview & sprite pipeline | §10.1 | Scrubber sprites, posters, responsive thumbnails, cache.
C55 | Lightbox & compare | §10.2 | Fast full-screen gallery with keyboard, compare, zoom.
C56 | Identify-task image support | §10.3 | Use frames/images in the identify workflow.
C57 | Grid/list view modes | §10.4 | Multiple view modes, per-user density, virtualized rows.
C58 | Play from grid | §10.5 | Inline playback in the grid, hover-scrub, no page hop.
C59 | TikTok-style vertical view | §10.6 | Vertical feed of clips for browsing.
C60 | Keyboard shortcuts & power use | §10.7 | Full shortcut map, command palette, undo.
C61 | Theming & accessibility | §10.8 | Dark/light, contrast, focus, screen-reader labels.
C62 | Card & profile detail | §10.9 | Performer/studio cards, direct profile links, sorting.
C63 | Confirmation on cancel | §10.10 | Guard destructive and long-running actions.
C64 | Player & subtitle UX | §11.1 | Codec fallback, subtitle toggle, frame-accurate seek.
C65 | Chromecast / DLNA / AirPlay | §11.2 | Cast to local network displays and receivers.
C66 | External player handoff | §11.3 | Send to mpv/VLC/Jellyfin/Plex with resume position.
C67 | Plugin & scraper SDK | §11.4 | Stable extension API for scrapers, plugins, hooks.
C68 | Jellyfin-style API compatibility | §11.5 | External library clients can read the local server.
C69 | Multi-user & permissions | §12.1 | Accounts, roles, per-library permission, audit log.
C70 | Auth hardening | §12.2 | Passkeys/2FA, password rules, session management, recovery.
C71 | Federation protocol | §13.1 | Signed claim exchange between independent index servers.
C72 | Consensus & conflict merge | §13.2 | Per-field merge with tombstones and provenance.
C73 | Open API & SDK | §13.3 | Documented public API, client libraries, import/export.
C74 | Import / export / backup | §13.4 | Full-fidelity export, scheduled backup, restore drill.
C75 | Docker & compose | §12.3 | Reproducible containers, non-root, healthchecks, volumes.
C76 | Native install path | §12.4 | Single binary + Tauri app, no container required.
C77 | Reverse proxy & TLS | §12.5 | nginx/Caddy recipes, static assets, websocket tuning.
C78 | Observability & logging | §12.6 | Structured logs, job traces, health dashboard, metrics.
C79 | Consent & takedown pipeline | §14.1 | Attestation, quarantine, hash-deny, cross-peer takedown.
C80 | Migration & upgrade safety | §14.2 | Forward-only migrations, backup gate, rollback plan.
C81 | Tag/field naming consistency | §15.1 | One vocabulary for tagging concepts across the UI.
C82 | Country & locale reference data | §15.2 | Maintained locale list, correct names, i18n coverage.
C83 | Duplicate-entity hygiene | §15.3 | Detect and merge duplicate studios, tags, performers.
C84 | Notifications & activity feed | §15.4 | Watched entities, edit notifications, digest mail.
C85 | Documentation & onboarding | §15.5 | Docs site, first-run tour, sample library.
C86 | Accessibility of metadata entry | §15.6 | Bulk editor, CSV import, paste-parse, undo.
C87 | Fingerprint privacy | §15.7 | Privacy-preserving fingerprint publication, opt-out.
C88 | Dataset export for research | §15.8 | De-identified dumps for ML and research use.
C89 | Scheduled maintenance | §15.9 | Clustering refresh, orphan cleanup, re-verify jobs.
C90 | Deep-linkable URLs | §15.10 | Every view state is a shareable, resolvable URL.
C91 | P2P locators & external client hand-off | §5.18 | Store/exchange magnet, ed2k, infohash locators as metadata; hand off to an external download client; never fetch.

# Documented non-goals — only when the issue genuinely matches the reason

X01 | Windows-only requirement | The platform targets Linux desktop plus a hosted server; Windows/macOS 
X02 | Embedded browser runtime | Explicitly out of scope: no Electron/Chromium bundling, ever. Desktop uses 
X03 | Cloud-only requirement | Out of scope: features that only work when a proprietary cloud service is 
X04 | Deliberate content policy expansion | Out of scope: features whose only purpose is to broaden the index to material 
X05 | Upstream tracker | Forwarded upstream: belongs to ffmpeg, a browser, or an OS, not to this project.
X06 | Duplicate of a merged issue | Folded into the canonical capability; the tracker thread is the discussion, 