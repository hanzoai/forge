// Copyright 2026 Hanzo AI, Inc. All rights reserved.
// SPDX-License-Identifier: MIT

package db

import (
	"database/sql"
	"path/filepath"
	"testing"
)

// TestConnStrAppliesItsPragmas opens a real file with the connection string the
// registered maker produces and asks the database what it got.
//
// Asserting on the string would prove nothing: the spelling is exactly what
// differs between the driver's two backends, and each ignores the other's
// silently, so a string assertion passes on the build that ignores it.
func TestConnStrAppliesItsPragmas(t *testing.T) {
	driver, connStr, err := makeSQLiteConnStr(SQLiteConnStrOptions{
		FilePath: filepath.Join(t.TempDir(), "t.db"), BusyTimeout: 7000, JournalMode: "WAL",
	})
	if err != nil {
		t.Fatalf("connstr: %v", err)
	}
	db, err := sql.Open(driver, connStr)
	if err != nil {
		t.Fatalf("open %s %s: %v", driver, connStr, err)
	}
	defer func() { _ = db.Close() }()

	for _, c := range []struct{ pragma, want string }{
		{"journal_mode", "wal"},
		{"busy_timeout", "7000"},
	} {
		var got string
		if err := db.QueryRow("PRAGMA " + c.pragma).Scan(&got); err != nil {
			t.Fatalf("PRAGMA %s: %v", c.pragma, err)
		}
		if got != c.want {
			t.Errorf("%s = %q, want %q — the connection string asked for it and the backend did not take it", c.pragma, got, c.want)
		}
	}
}
