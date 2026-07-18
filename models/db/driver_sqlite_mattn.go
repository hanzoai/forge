// Copyright 2026 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

//go:build sqlite_mattn && sqlite_unlock_notify

package db

import (
	"fmt"
	"strconv"
	"strings"

	// CGO build path: hanzoai/sqlite's cgo backend (csqlite) registers the
	// "sqlite" driver and parses mattn-style DSN params, so the connstr maker
	// below keeps _busy_timeout=/_journal_mode= form (NOT the pure-Go _pragma()).
	_ "github.com/hanzoai/sqlite"
)

func init() {
	registerSQLiteConnStrMaker(makeSQLiteConnStrMattnCGO)
}

func makeSQLiteConnStrMattnCGO(opts SQLiteConnStrOptions) (string, string, error) {
	var params []string
	params = append(params, "cache=shared")
	params = append(params, "mode=rwc")
	params = append(params, "_busy_timeout="+strconv.Itoa(opts.BusyTimeout))
	params = append(params, "_txlock=immediate")
	if opts.JournalMode != "" {
		params = append(params, "_journal_mode="+opts.JournalMode)
	}
	connStr := fmt.Sprintf("file:%s?%s", opts.FilePath, strings.Join(params, "&"))
	return sqlDriverSQLite3, connStr, nil
}
