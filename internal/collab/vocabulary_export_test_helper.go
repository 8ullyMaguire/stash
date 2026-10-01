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

// LinksForTest returns the whole link namespace, so a test OUTSIDE this package can
// check the propose side against a write side it can only see through a real schema.
//
// It exists for the same reason VocabularyForTest does, and the parallel is exact: the
// seam-closure test needs to ask "is every proposable link writable" and "is every
// writable field proposable", and it cannot answer either question from one side.
// A helper that returned only ProposableLinks(target) would force the test to know the
// target list in advance, which is the list the test is supposed to be checking.
func LinksForTest() map[string][]LinkKind {
	out := make(map[string][]LinkKind, len(links))
	for targetType := range links {
		out[targetType] = ProposableLinks(targetType)
	}
	return out
}
