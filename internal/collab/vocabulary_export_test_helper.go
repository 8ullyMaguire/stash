package collab

// VocabularyForTest exposes the vocabulary as target -> set of field names.
//
// It exists for exactly one test: the one in pkg/sqlite that checks every field
// is a real column in the migrated database. That check cannot live here,
// because reading the schema needs a database, and a database cannot be opened
// from a package that deliberately has none.
//
// Named with the ForTest suffix rather than presented as API, so nobody
// discovers it and starts building on it.
func VocabularyForTest() map[string]map[string]bool {
	out := make(map[string]map[string]bool, len(vocabulary))
	for target, fields := range vocabulary {
		set := make(map[string]bool, len(fields))
		for name := range fields {
			set[name] = true
		}
		out[target] = set
	}
	return out
}
