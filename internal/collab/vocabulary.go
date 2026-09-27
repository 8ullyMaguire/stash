// Package collab's second file: the proposal write path.
//
// The governance rules in governance.go decide WHETHER a change is accepted.
// This file decides WHETHER it may be proposed at all, and is the only place
// that answer is computed.
package collab

import (
	"fmt"
	"strconv"
	"strings"
)

// Vocabulary is the closed set of (target, field) pairs that may be proposed.
//
// Closed is the operative word. An open field is a stored-XSS and a
// deserialisation surface: a proposal carrying a value the apply path does not
// know how to write is a payload waiting for a caller that does. Every other
// table in the system can be extended later; this one is a security boundary,
// and the way it is enforced is a map lookup rather than anything cleverer.
//
// The values are the field types, not validators. Type is what makes the map
// checkable: a field's type cannot change without changing this file, so
// ValidateValue below can switch over it and be exhaustive.
type FieldType int

const (
	// TypeString is free text. The only genuinely dangerous type, and the reason
	// the vocabulary is closed rather than derived from the database schema.
	TypeString FieldType = iota

	// TypeInt is a foreign key or a count.
	TypeInt

	// TypeDate is a calendar date, no time component.
	TypeDate

	// TypeRating is a bounded number. The bounds are part of the type because an
	// unbounded rating is the thing that needs bounding.
	TypeRating
)

// proposableField binds a field name to its type.
type proposableField struct {
	Type FieldType
	// Min and Max apply to TypeRating only. Zero values mean "unbounded", which
	// is why TypeRating is distinct rather than just TypeInt with a comment.
	Min, Max int
}

// vocabulary is the whole allowed surface, exactly as spec §4.1 lists it.
//
// Kept as a literal map rather than derived from the schema because a derived
// list would grow every time someone adds a column to scenes, and this map is
// the thing standing between that and a stored-XSS vector. Someone adding a
// field has to come here and think about it, which is the point.
var vocabulary = map[string]map[string]proposableField{
	"scene": {
		"title":     {Type: TypeString},
		"details":   {Type: TypeString},
		"director":  {Type: TypeString},
		"studio_id": {Type: TypeInt},
		"date":      {Type: TypeDate},
		// NO `url`. A scene's URLs live in the `scene_urls` JOIN table
		// (scene.go:34), which is multi-valued and ordered. Proposing a single
		// `url` would mean either inventing a column that does not exist or
		// silently dropping every URL but one -- and "edit the url" is not an
		// edit the single-field proposal model can express honestly. Out of
		// scope until the proposal model carries list semantics.
	},
	"performer": {
		"name":           {Type: TypeString},
		"disambiguation": {Type: TypeString},
		"details":        {Type: TypeString},
		"gender":         {Type: TypeString},
		"birthdate":      {Type: TypeDate},
		"country":        {Type: TypeString},
	},
	"studio": {
		// NO `url`. The spec §4.1 list includes one; studios have no such column
		// (see studio.go: ID, Name, ParentID, Rating, Details, Favorite,
		// IgnoreAutoTag, Organized, ImageBlob). A vocabulary entry for a column
		// that does not exist is a runtime SQL error waiting for the first
		// proposal against a studio, so the map follows the schema and the spec
		// is corrected in its own file.
		"name":      {Type: TypeString},
		"details":   {Type: TypeString},
		"parent_id": {Type: TypeInt},
	},
	"tag": {
		"name":        {Type: TypeString},
		"description": {Type: TypeString},
	},
	"gallery": {
		"title":   {Type: TypeString},
		"details": {Type: TypeString},
	},
	"image": {
		"title":  {Type: TypeString},
		"rating": {Type: TypeRating, Min: 1, Max: 5},
	},
	"group": {
		// NO `title` or `details`: a group has `name` and `description` (see
		// group.go: ID, Name, Aliases, Duration, Date, Rating, StudioID,
		// Director, Description). Spec §4.1 lists title/details, which are the
		// GALLERY's fields -- most likely a copy-paste when the list was written.
		"name":        {Type: TypeString},
		"description": {Type: TypeString},
		"date":        {Type: TypeDate},
		"studio_id":   {Type: TypeInt},
		"rating":      {Type: TypeRating, Min: 1, Max: 5},
	},
}

// FieldInfo describes a proposable field, for callers that need to render a
// form or report what is allowed.
type FieldInfo struct {
	TargetType string
	Field      string
	Type       FieldType
	Min, Max   int
}

// ProposableFields returns the fields allowed for a target type, sorted by the
// caller's iteration over a returned slice rather than a map — the UI needs a
// stable order and Go map iteration is deliberately randomised.
func ProposableFields(targetType string) []FieldInfo {
	fields, ok := vocabulary[targetType]
	if !ok {
		return nil
	}
	names := make([]string, 0, len(fields))
	for name := range fields {
		names = append(names, name)
	}
	sortStrings(names)

	out := make([]FieldInfo, 0, len(names))
	for _, name := range names {
		f := fields[name]
		out = append(out, FieldInfo{
			TargetType: targetType, Field: name, Type: f.Type, Min: f.Min, Max: f.Max,
		})
	}
	return out
}

// sortStrings is a tiny insertion sort rather than a sort.Slice call, so this
// file has no import beyond fmt/strconv/strings. Not worth a dependency debate:
// the input is a vocabulary of at most six strings.
func sortStrings(s []string) {
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j] < s[j-1]; j-- {
			s[j], s[j-1] = s[j-1], s[j]
		}
	}
}

// ErrFieldNotProposable is returned for any (target, field) outside the
// vocabulary, and for a target type that does not exist at all.
//
// The two are deliberately NOT distinguished. A caller that could tell "no such
// target type" from "no such field on this target" could enumerate the schema
// through error messages, which is the same mistake as the invite key returning
// distinct errors for unknown vs exhausted.
var ErrFieldNotProposable = fmt.Errorf("field is not proposable")

// ErrValueInvalid is returned when the value does not parse as the field's
// type. The caller turns this into a 422 at PROPOSAL time, not a 500 at apply
// time: a value that will fail later must not be allowed to reach a vote.
var ErrValueInvalid = fmt.Errorf("value is not valid for this field")

// LookupField reports whether (targetType, field) is proposable, and its type.
func LookupField(targetType, field string) (FieldInfo, bool) {
	fields, ok := vocabulary[targetType]
	if !ok {
		return FieldInfo{}, false
	}
	f, ok := fields[field]
	if !ok {
		return FieldInfo{}, false
	}
	return FieldInfo{
		TargetType: targetType, Field: field, Type: f.Type, Min: f.Min, Max: f.Max,
	}, true
}

// ValidateValue checks a proposed value against its field's type.
//
// This is the 422-at-proposal-time rule from spec §4.1: a proposal cannot carry
// a value that would fail at apply time. The alternative is a queue full of
// proposals that quorum accepted and the apply path then had to reject, which
// wastes every voter's time and, worse, trains people to ignore rejections.
//
// A NIL value is valid for every type. It means "clear this field", which is a
// real edit people ask for and is exactly why the schema keeps NULL and "" apart.
func ValidateValue(targetType, field string, value *string) error {
	info, ok := LookupField(targetType, field)
	if !ok {
		return ErrFieldNotProposable
	}
	if value == nil {
		return nil
	}

	switch info.Type {
	case TypeString:
		// No length check here. The apply path owns string limits because the
		// limit is the COLUMN's, and duplicating it here would be a second place
		// to forget. What this does reject is control characters, which are the
		// one string problem that is ours rather than the schema's: they are
		// never legitimate in a title and they are how a log line gets forged.
		if err := checkNoControlChars(*value); err != nil {
			return err
		}
		return nil

	case TypeInt:
		if *value == "" {
			return ErrValueInvalid
		}
		n, err := strconv.Atoi(strings.TrimSpace(*value))
		if err != nil {
			return ErrValueInvalid
		}
		// Negative ids are meaningless and a -1 that reached the apply path
		// would either no-op or, worse, be written as a real row.
		if n <= 0 {
			return ErrValueInvalid
		}
		return nil

	case TypeDate:
		return validateDate(*value)

	case TypeRating:
		return validateRating(*value, info)
	}
	return ErrValueInvalid
}

// checkNoControlChars rejects C0 controls and DEL, allowing \t \n \r because
// a details field legitimately contains a line break.
//
// The reason is stated in the test that exercises it: these are the characters
// that let one line of user-supplied text forge another when it reaches a log,
// a terminal, or a CSV export.
func checkNoControlChars(s string) error {
	for _, r := range s {
		if r == '\t' || r == '\n' || r == '\r' {
			continue
		}
		if r < 0x20 || r == 0x7f {
			return ErrValueInvalid
		}
	}
	return nil
}

// validateDate requires exactly YYYY-MM-DD.
//
// Strictly. A lenient parser here would accept "2024-13-45" and let it through
// to apply time, where it either fails on a different code path or is silently
// stored — and the whole reason this function exists is that no value may fail
// later.
func validateDate(s string) error {
	t := strings.TrimSpace(s)
	if len(t) != 10 || t[4] != '-' || t[7] != '-' {
		return ErrValueInvalid
	}
	if _, err := strconv.Atoi(t[0:4]); err != nil {
		return ErrValueInvalid
	}
	if _, err := strconv.Atoi(t[5:7]); err != nil {
		return ErrValueInvalid
	}
	if _, err := strconv.Atoi(t[8:10]); err != nil {
		return ErrValueInvalid
	}

	month, _ := strconv.Atoi(t[5:7])
	day, _ := strconv.Atoi(t[8:10])
	if month < 1 || month > 12 || day < 1 || day > 31 {
		return ErrValueInvalid
	}
	// Days-in-month, leap year included. Without the leap rule, 2023-02-29
	// would pass proposal time and fail at apply time, which is precisely the
	// split this function is here to prevent.
	days := [...]int{31, 28, 31, 30, 31, 30, 31, 31, 30, 31, 30, 31}
	max := days[month-1]
	year, _ := strconv.Atoi(t[0:4])
	if month == 2 && (year%4 == 0 && (year%100 != 0 || year%400 == 0)) {
		max = 29
	}
	if day > max {
		return ErrValueInvalid
	}
	return nil
}

func validateRating(s string, info FieldInfo) error {
	// Accepts "4" and "4.0" but not "4.5": a five-point scale with halves in it
	// is a different scale, and admitting it here would mean a rating that
	// proposes fine and applies as a different number than it displayed.
	t := strings.TrimSpace(s)
	if t == "" {
		return ErrValueInvalid
	}
	if dot := strings.IndexByte(t, '.'); dot >= 0 {
		if frac := t[dot+1:]; frac != "0" {
			return ErrValueInvalid
		}
		t = t[:dot]
	}
	n, err := strconv.Atoi(t)
	if err != nil {
		return ErrValueInvalid
	}
	if n < info.Min || n > info.Max {
		return ErrValueInvalid
	}
	return nil
}
