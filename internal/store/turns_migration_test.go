package store

import (
	"path/filepath"
	"testing"
)

func TestTurnsMigrationInvalidatesCleanFingerprint(t *testing.T) {
	path := filepath.Join(t.TempDir(), "index.sqlite")
	db, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(`ALTER TABLE sessions DROP COLUMN turns`); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(`INSERT INTO meta(key,value)VALUES('clean_fingerprint','old-policy')`); err != nil {
		t.Fatal(err)
	}
	db.Close()
	db, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	fp, err := Meta(db, "clean_fingerprint")
	if err != nil || fp != "" {
		t.Fatalf("fingerprint %q err=%v", fp, err)
	}
	cols, err := sessionColumns(db)
	if err != nil || !cols["turns"] {
		t.Fatalf("columns %v err=%v", cols, err)
	}
	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	if err = SetMeta(tx, "clean_fingerprint", "new-policy"); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	db.Close()
	db, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	fp, err = Meta(db, "clean_fingerprint")
	if err != nil || fp != "new-policy" {
		t.Fatalf("second open fingerprint %q err=%v", fp, err)
	}
}
