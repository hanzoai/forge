// Copyright 2024 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package setting

import (
	"net/http"

	"github.com/hanzoai/git/modules/setting"
	"github.com/hanzoai/git/modules/templates"
	shared_user "github.com/hanzoai/git/routers/web/shared/user"
	"github.com/hanzoai/git/services/context"
)

const (
	tplSettingsBlockedUsers templates.TplName = "user/settings/blocked_users"
)

func BlockedUsers(ctx *context.Context) {
	ctx.Data["Title"] = ctx.Tr("user.block.list")
	ctx.Data["PageIsSettingsBlockedUsers"] = true

	shared_user.BlockedUsers(ctx, ctx.Doer)
	if ctx.Written() {
		return
	}

	ctx.HTML(http.StatusOK, tplSettingsBlockedUsers)
}

func BlockedUsersPost(ctx *context.Context) {
	shared_user.BlockedUsersPost(ctx, ctx.Doer, setting.AppSubURL+"/user/settings/blocked_users")
}
