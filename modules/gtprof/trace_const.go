// Copyright 2026 Hanzo AI, Inc. All rights reserved.
// SPDX-License-Identifier: MIT

package gtprof

// Some interesting names could be found in https://github.com/open-telemetry/opentelemetry-go/tree/main/semconv

const (
	TraceSpanHTTP     = "http"
	TraceSpanGitRun   = "git-run"
	TraceSpanDatabase = "database"
)

const (
	TraceAttrFuncCaller = "func.caller"
	TraceAttrDbSQL      = "db.sql"
	TraceAttrGitCommand = "git.command"
	TraceAttrHTTPRoute  = "http.route"
)
