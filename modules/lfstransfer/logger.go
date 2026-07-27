// Copyright 2026 Hanzo AI, Inc. All rights reserved.
// Copyright 2024 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package lfstransfer

import (
	"github.com/charmbracelet/git-lfs-transfer/transfer"
)

var _ transfer.Logger = (*GitLogger)(nil)

// noop logger for passing into transfer
type GitLogger struct{}

func newLogger() transfer.Logger {
	return &GitLogger{}
}

// Log implements transfer.Logger
func (g *GitLogger) Log(msg string, items ...any) {
}
