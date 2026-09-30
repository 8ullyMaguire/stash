package videophash

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	_ "github.com/mattn/go-sqlite3"
)

// Does migration 87 actually DELETE the right rows?
//
// Reading the SQL proves the text; running it against a real SQLite proves the
// ROW SET, which is the thing the migration exists to get right. Every other
// test in this file checks the migration against the algorithm; this one checks
// the algorithm's assumption about the schema -- and that assumption is wrong in
// the way a fixture written from memory gets wrong.
//
// The first version of this probe declared `video_files(id INTEGER PRIMARY KEY,
// duration REAL)` and a `files_fingerprints` with no `fingerprint` column. The
// migration's subquery is `SELECT file_id FROM video_files`, so against that
// fixture it failed to resolve -- and because a failed subquery inside an IN()
// does not abort the DELETE, every phash row went, including the 200-second
// ones. The report was "the migration deletes far too much", which is exactly
// what a destructive migration looks like, and the migration was correct.
//
// So: the shapes come from 32_files.up.sql verbatim, and the lesson is recorded
// because it is general -- a test fixture that does not match the real schema
// does not fail loudly, it fails as a FALSE ALARM about the code under test.
func TestMigration87ActuallyDeletes(t *testing.T) {
	dsn := "file:" + filepath.Join(t.TempDir(), "t.db") + "?_foreign_keys=on"
	db, err := sql.Open("sqlite3", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	// The REAL shapes, from 32_files.up.sql. The first version of this probe
	// invented `id INTEGER PRIMARY KEY` and no `fingerprint` column, so the
	// migration's own subquery -- `SELECT file_id FROM video_files` -- failed to
	// resolve, and the migration appeared to delete EVERY phash including the
	// 200-second ones. The migration was correct; the fixture was fiction. Read
	// the schema you are testing against rather than approximating it.
	stmts := []string{
		`CREATE TABLE video_files (
			file_id integer NOT NULL primary key,
			duration float NOT NULL
		)`,
		`CREATE TABLE files_fingerprints (
			file_id integer NOT NULL,
			type varchar(255) NOT NULL,
			fingerprint blob NOT NULL,
			PRIMARY KEY (file_id, type, fingerprint)
		)`,
	}
	for _, s := range stmts {
		if _, err := db.Exec(s); err != nil {
			t.Fatal(err)
		}
	}

	// id, duration, type
	rows := [][]any{
		{1, 30.0, "phash"},  // short, phash   -> DELETE
		{2, 30.0, "oshash"}, // short, oshash  -> KEEP
		{3, 200.0, "phash"}, // long, phash   -> KEEP
		{4, 0.0, "phash"},   // unknown        -> KEEP
		{5, 150.0, "phash"}, // at the boundary-> DELETE
		{6, 150.1, "phash"}, // just over      -> KEEP
	}
	for _, r := range rows {
		if _, err := db.Exec(
			`INSERT INTO video_files (file_id, duration) VALUES (?, ?)`,
			r[0], r[1]); err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(
			`INSERT INTO files_fingerprints (file_id, type, fingerprint) VALUES (?, ?, ?)`,
			// The fingerprint must be DISTINCT per row:
			// files_fingerprints is keyed (file_id, type, fingerprint), so two
			// rows sharing one fingerprint string collide and the second INSERT
			// is silently dropped by the primary key. The first version used a
			// constant "fp" and the fixture wrote 4 rows instead of 6 -- caught
			// only because the count is asserted, which is exactly why it is.
			r[0], r[2], fmt.Sprintf("fp-%v", r[0])); err != nil {
			t.Fatal(err)
		}
	}

	// The fixture must have landed BEFORE the migration runs, or every
	// assertion below passes vacuously against a table the migration never saw.
	//
	// This count belongs here, not after the migration: its first version sat
	// after the DELETE and therefore reported SURVIVORS (4) against an expected
	// 6, which reads as "the fixture lost rows" and is really "the fixture was
	// checked at the wrong moment". Assert the precondition where the
	// precondition holds.
	var seeded int
	if err := db.QueryRow(
		`SELECT count(*) FROM files_fingerprints`).Scan(&seeded); err != nil {
		t.Fatal(err)
	}
	if seeded != len(rows) {
		t.Fatalf("fixture wrote %d fingerprints before the migration, want %d -- "+
			"a test that asserts against a table the migration never saw proves "+
			"nothing", seeded, len(rows))
	}

	body, err := os.ReadFile(migrationFile)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(string(body)); err != nil {
		t.Fatalf("the migration does not run: %v", err)
	}

	want := map[int]bool{1: false, 2: true, 3: true, 4: true, 5: false, 6: true}
	kept, err := db.Query(`SELECT file_id, type FROM files_fingerprints ORDER BY file_id`)
	if err != nil {
		t.Fatal(err)
	}
	defer kept.Close()
	seen := map[int]bool{}
	for kept.Next() {
		var id int
		var typ string
		if err := kept.Scan(&id, &typ); err != nil {
			t.Fatal(err)
		}
		seen[id] = true
	}
	for id, shouldSurvive := range want {
		if seen[id] != shouldSurvive {
			t.Errorf("file %d: survived=%v, want survived=%v", id, seen[id], shouldSurvive)
		}
	}
}
