// Copyright 2026 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

//go:build !sqlite_mattn

// hanzoai/sqlite is the default driver: a pure-Go (CGO-free) SQLite engine that
// self-registers the "sqlite" database/sql driver name on import. It is the
// default because it needs no CGO; the optional mattn build path (sqlite_mattn
// tag) remains for CGO builds that need sqlite_unlock_notify.

package db

import (
	"fmt"
	"strings"

	// blank import registers the "sqlite" database/sql driver (pure-Go backend).
	_ "github.com/hanzoai/sqlite"
)

func init() {
	// this driver contains huge amount of Golang code, so it is much slower when "-race" check is enabled.
	registerSQLiteConnStrMaker(makeSQLiteConnStrModerncCCGO)
}

func makeSQLiteConnStrModerncCCGO(opts SQLiteConnStrOptions) (string, string, error) {
	var params []string
	// TODO: there is a changed behavior from mattn driver:
	// * mattn driver can wait for pretty long time for concurrent accesses (not limited by the busy timeout)
	// * but other drivers will report something like "database is locked (5) (SQLITE_BUSY)" if the timeout is reached
	// Maybe we need to relax the busy timeout to a reasonable long time in the future
	params = append(params, fmt.Sprintf("_pragma=busy_timeout(%d)", opts.BusyTimeout))
	params = append(params, "_txlock=immediate")
	if opts.JournalMode != "" {
		params = append(params, fmt.Sprintf("_pragma=journal_mode(%s)", opts.JournalMode))
	}
	connStr := fmt.Sprintf("file:%s?%s", opts.FilePath, strings.Join(params, "&"))
	return sqlDriverSQLite3, connStr, nil
}
