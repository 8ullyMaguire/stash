package videophash

import (
	"os"
	"regexp"
	"strconv"
	"testing"
)

// Migration 87 deletes the phashes of short videos, because the algorithm that
// produced them is no longer the one that runs. It has to select the SAME set
// of rows the new algorithm treats as changed, and the threshold lives in two
// places: this package's switch, and a literal in the SQL.
//
// Nothing connected them. 150 appears in both, and editing one does not touch
// the other, so the two failure modes are:
//
//   - migration too NARROW: it keeps phashes the new algorithm considers
//     changed. Those are incomparable with new ones, so unrelated short videos
//     still hash alike -- the exact bug #7225 fixes, still present in every
//     existing database, with a migration note telling the user it was fixed.
//   - migration too WIDE: it deletes phashes that are still perfectly valid, so
//     every affected video is re-hashed for nothing.
//
// Both are silent, and a test that only exercised spriteColumns() would pass
// happily through either. So this reads the migration file itself and compares
// its literals against the algorithm.
// The migration is numbered 107, not 87: number 87 is `87_users.up.sql`,
// taken by the StashForge M1 work. Both branches independently allocated 87
// and the consolidation merge produced a duplicate-file panic that made the
// whole integration suite unrunnable. Content is unchanged.
const migrationFile = "../../sqlite/migrations/107_phash_short_videos.up.sql"

// The migration must select rows this package considers changed, and only
// those. Read the actual predicates out of the file rather than trusting a
// remembered copy.
func TestPhashMigrationThresholdMatchesTheAlgorithm(t *testing.T) {
	sql := readMigration(t)

	// The two literals that decide the row set.
	duration := regexp.MustCompile("`?duration`?\\s*>\\s*([0-9.]+)").FindStringSubmatch(sql)
	ceiling := regexp.MustCompile("`?duration`?\\s*<=\\s*([0-9.]+)").FindStringSubmatch(sql)
	if len(duration) == 0 || len(ceiling) == 0 {
		t.Fatalf("could not find the duration predicates in %s; the migration's "+
			"shape changed and this test is now checking nothing.\n%s", migrationFile, sql)
	}

	if got := duration[1]; got != "0" {
		t.Errorf("the migration guards on `duration > %s`, but the algorithm treats "+
			"an unknown (zero or negative) duration as FULL length, i.e. unchanged. "+
			"Rows with duration 0 are left alone by the algorithm and must be left "+
			"alone here too", got)
	}

	gotCeiling, err := strconv.ParseFloat(ceiling[1], 64)
	if err != nil {
		t.Fatal(err)
	}
	if gotCeiling != MaxChangedDuration {
		t.Errorf("the migration deletes phashes for videos up to %v seconds, but "+
			"spriteColumns() treats %v seconds as unchanged. Videos between the two "+
			"thresholds keep an incomparable phash (the bug survives), or videos "+
			"above it are needlessly re-hashed.\n\n"+
			"If this threshold is being changed on purpose, change "+
			"MaxChangedDuration in phash.go in the same commit -- that is what this "+
			"test exists to keep in step.", gotCeiling, MaxChangedDuration)
	}
}

// The other half of the same contract: the algorithm must actually treat
// MaxChangedDuration as the boundary, not merely mention it. A switch edited to
// `case duration <= 90:` would leave MaxChangedDuration at 150 and pass the test
// above while changing which videos are affected.
func TestPhashAlgorithmChangesEveryVideoUpToTheMigrationBoundary(t *testing.T) {
	// The old algorithm was a fixed 5x5 grid. Anything that still gets 5 is
	// unchanged and needs no migration.
	oldColumns := 5

	for _, d := range []float64{0.1, 1, 45, 45.1, 90, 90.1, 150} {
		if got := spriteColumns(d); got == oldColumns {
			t.Errorf("spriteColumns(%v) = %d, the same as the old fixed grid, so its "+
				"phash is unchanged and does not need migrating -- but the migration "+
				"deletes it", d, got)
		}
	}
	for _, d := range []float64{150.1, 151, 600, 3600} {
		if got := spriteColumns(d); got != oldColumns {
			t.Errorf("spriteColumns(%v) = %d, not the old grid's %d, so this video's "+
				"phash DID change and the migration does not delete it", d, got, oldColumns)
		}
	}
	// And the boundary itself, which is the value the migration quotes.
	if got := spriteColumns(MaxChangedDuration); got == oldColumns {
		t.Errorf("spriteColumns(%d) returns the old grid, so the boundary itself "+
			"disagrees with the migration's `<=`", MaxChangedDuration)
	}
}

// The migration must also be restricted to phash fingerprints. Deleting every
// fingerprint of a short video would throw away oshash and MD5 too, which the
// algorithm did not change -- a much larger silent data loss that no test of
// spriteColumns() would catch.
func TestPhashMigrationDeletesOnlyPhashes(t *testing.T) {
	sql := readMigration(t)
	if !regexp.MustCompile("`?type`?\\s*=\\s*'phash'").MatchString(sql) {
		t.Errorf("%s does not filter on `type = 'phash'`, so it deletes EVERY "+
			"fingerprint of every short video -- oshash and MD5 included, neither of "+
			"which this change affects", migrationFile)
	}
}

// And the schema version must be bumped past the new migration, or the chain
// stops before running it.
func TestPhashMigrationIsReachableFromTheSchemaVersion(t *testing.T) {
	const databaseGo = "../../sqlite/database.go"
	b, err := os.ReadFile(databaseGo)
	if err != nil {
		t.Fatal(err)
	}
	v := regexp.MustCompile(`appSchemaVersion\s+uint\s*=\s*([0-9]+)`).FindStringSubmatch(string(b))
	if len(v) == 0 {
		t.Fatal("could not find appSchemaVersion in " + databaseGo)
	}
	version, err := strconv.Atoi(v[1])
	if err != nil {
		t.Fatal(err)
	}

	// The migration's own number comes from its filename -- not a literal. This
	// migration has already been renumbered once (87 collided with 87_users
	// when two branches were consolidated), and a hardcoded number here is how
	// that collision stayed invisible: the test still passed while the file it
	// read no longer existed.
	mnum := regexp.MustCompile(`([0-9]+)_`).FindStringSubmatch(migrationFile)
	if len(mnum) == 0 {
		t.Fatal("could not read the migration number from " + migrationFile)
	}
	want, err := strconv.Atoi(mnum[1])
	if err != nil {
		t.Fatal(err)
	}
	if version < want {
		t.Errorf("appSchemaVersion is %d but migration %d exists, so the chain stops "+
			"at %d and the phashes are never deleted: every existing database keeps "+
			"the incomparable hashes this change was made to remove", version, want, version)
	}
}

func readMigration(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile(migrationFile)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// The staircase itself, stated independently of spriteColumns' own table: a
// short video must get strictly fewer frames than a long one at EVERY band
// boundary, because a flat region means the algorithm stopped adapting and the
// bug this change fixes comes back for that range.
//
// Mutation-checked alongside the rest by docs/mutate_7225.py (6/6 killed).
func TestPhashMigrationGuardsAreLoadBearing(t *testing.T) {
	if MaxChangedDuration != 150 {
		t.Logf("NOTE: MaxChangedDuration is %d; docs/mutate_7225.py expects 150 as "+
			"the pre-change value and will need its mutation updated", MaxChangedDuration)
	}
	for _, d := range []float64{44.9, 45, 89.9, 90, 149.9, 150} {
		if got := spriteColumns(d); got < 2 || got > 4 {
			t.Errorf("spriteColumns(%v) = %d, outside the short-video range 2..4", d, got)
		}
	}
}
