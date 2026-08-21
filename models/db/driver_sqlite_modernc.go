// Copyright 2026 Hanzo AI, Inc. All rights reserved.
// Copyright 2026 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

//go:build !sqlite_mattn

// hanzoai/sqlite is the default driver. It registers the "sqlite" database/sql
// name on import and answers on either build — its own cgo backend when cgo is
// on, a pure-Go engine when it is not. The optional sqlite_mattn build path
// remains for CGO builds that need sqlite_unlock_notify.

package db

import (
	"strconv"

	// registers the "sqlite" database/sql driver, and builds its DSN.
	"github.com/hanzoai/sqlite"
)

func init() {
	// this driver contains huge amount of Golang code, so it is much slower when "-race" check is enabled.
	registerSQLiteConnStrMaker(makeSQLiteConnStrModerncCCGO)
}

func makeSQLiteConnStrModerncCCGO(opts SQLiteConnStrOptions) (string, string, error) {
	// TODO: there is a changed behavior from mattn driver:
	// * mattn driver can wait for pretty long time for concurrent accesses (not limited by the busy timeout)
	// * but other drivers will report something like "database is locked (5) (SQLITE_BUSY)" if the timeout is reached
	// Maybe we need to relax the busy timeout to a reasonable long time in the future

	// The driver builds the pragma half. Its two backends spell a pragma
	// differently in a DSN and each ignores the other's spelling silently, and
	// which one is linked turns on cgo — not on the sqlite_mattn tag that selects
	// this file — so either spelling written here is right on one build and
	// evaporates on the other. _txlock is a driver parameter, not a pragma, and
	// both backends read it.
	pragmas := []sqlite.Pragma{{Name: "busy_timeout", Value: strconv.Itoa(opts.BusyTimeout)}}
	if opts.JournalMode != "" {
		pragmas = append(pragmas, sqlite.Pragma{Name: "journal_mode", Value: opts.JournalMode})
	}
	connStr := sqlite.PragmaDSN(opts.FilePath, pragmas) + "&_txlock=immediate"
	return sqlDriverSQLite3, connStr, nil
}
