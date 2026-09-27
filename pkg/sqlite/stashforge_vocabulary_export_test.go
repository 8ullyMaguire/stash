//go:build integration
// +build integration

package sqlite

// Exported for the integration test in the sqlite_test package, which cannot
// see unexported identifiers. A one-line shim rather than renaming the function:
// the caller of the real thing is internal, and widening its visibility for a
// test would make it part of the package's API.
func GetRandomSortForTest(tableName, direction string, seed uint64) string {
	return getRandomSort(tableName, direction, seed)
}
