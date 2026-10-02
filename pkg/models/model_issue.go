package models

import "time"

// stash#837 — "Log potential issues with files, show in a dedicated UI."
//
// THE MODEL IS DELIBERATELY THIN. An issue is a fact about the library that a
// human may want to look at: a duplicate, a zero-byte file, a scan that found
// nothing. It is not a rule, not a severity, and not an event.
//
// The two shape decisions, and why:
//
//   - `Domain` and `Kind` are the machine-readable contract; `Details` is prose
//     that NOTHING parses. A caller that needs to filter has a column to filter
//     on. Anything else is a string match waiting to rot, and the panel's
//     filters are the reason this table exists at all.
//   - There is no `Severity`. Every row this feature records is the same kind of
//     thing, and a severity axis needs a second axis of judgement to be worth
//     storing. Adding one later is a migration; storing a value nobody sets is a
//     column that lies.
const (
	// Domains. See docs/ISSUE-837-spec.md §4.
	IssueDomainFile     = "file"
	IssueDomainScan     = "scan"
	IssueDomainMetadata = "metadata"
)

// Kinds within IssueDomainFile. The CHECK in migration 120 constrains `domain`
// to these three values; `kind` is constrained only to be non-blank, because a
// new detector must be able to add a kind without a migration and the cost of
// forgetting one is a row the panel cannot group.
const (
	IssueKindDuplicate    = "duplicate"
	IssueKindZeroDuration = "zero_duration"
	IssueKindZeroSize     = "zero_size"
	IssueKindNoFiles      = "no_files"
)

type Issue struct {
	ID         int        `json:"id"`
	FileID     *FileID    `json:"file_id"`
	Domain     string     `json:"domain"`
	Kind       string     `json:"kind"`
	Details    string     `json:"details"`
	DetectedAt time.Time  `json:"detected_at"`
	Resolved   bool       `json:"resolved"`
	ResolvedAt *time.Time `json:"resolved_at"`
}

// IssueFilterType.
//
// `Resolved` is a POINTER on purpose. `filter.Resolved != nil && *filter.Resolved`
// distinguishes "the caller asked for resolved issues" from "the caller said
// nothing", and the second case must NOT default to true at this layer: a caller
// that forgets would be handed every dismissal this library has ever recorded.
//
// The default lives in the STORE (see sqlite/issue.go), where it can be a
// decision with a test attached rather than a nil check every caller has to
// remember. A pointer here is what lets the store express "unresolved unless you
// say otherwise" without this type pretending to know the answer.
type IssueFilterType struct {
	Domain   *string `json:"domain"`
	Kind     *string `json:"kind"`
	Resolved *bool   `json:"resolved"`
	FileID   *FileID `json:"file_id"`
}
