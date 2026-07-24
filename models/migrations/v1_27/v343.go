// Copyright 2026 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package v1_27

import (
	"github.com/hanzoai/git/models/db"
)

// RenameGiteaThemesToHanzo maps stored user theme selections onto the renamed
// built-in themes (theme-gitea-* → theme-hanzo-*). Without this, every user
// who explicitly picked a theme would silently fall back to the default when
// the old theme name stops resolving.
func RenameGiteaThemesToHanzo(x db.EngineMigration) error {
	for _, m := range []struct{ old, new string }{
		{"gitea-auto", "hanzo-auto"},
		{"gitea-dark", "hanzo-dark"},
		{"gitea-light", "hanzo-light"},
		{"gitea-auto-protanopia-deuteranopia", "hanzo-auto-protanopia-deuteranopia"},
		{"gitea-auto-tritanopia", "hanzo-auto-tritanopia"},
		{"gitea-dark-protanopia-deuteranopia", "hanzo-dark-protanopia-deuteranopia"},
		{"gitea-dark-tritanopia", "hanzo-dark-tritanopia"},
		{"gitea-light-protanopia-deuteranopia", "hanzo-light-protanopia-deuteranopia"},
		{"gitea-light-tritanopia", "hanzo-light-tritanopia"},
	} {
		if _, err := x.Exec("UPDATE `user` SET `theme` = ? WHERE `theme` = ?", m.new, m.old); err != nil {
			return err
		}
	}
	return nil
}
