// Copyright 2014 The Gogs Authors. All rights reserved.
// Copyright 2020 The Gitea Authors.
// SPDX-License-Identifier: MIT

package admin

import (
	"github.com/hanzoai/git/models/db"
	user_model "github.com/hanzoai/git/models/user"
	"github.com/hanzoai/git/modules/setting"
	"github.com/hanzoai/git/modules/structs"
	"github.com/hanzoai/git/modules/templates"
	"github.com/hanzoai/git/routers/web/explore"
	"github.com/hanzoai/git/services/context"
)

const (
	tplOrgs templates.TplName = "admin/org/list"
)

// Organizations show all the organizations
func Organizations(ctx *context.Context) {
	ctx.Data["Title"] = ctx.Tr("admin.organizations")
	ctx.Data["PageIsAdminOrganizations"] = true

	sortOrder := ctx.FormString("sort", UserSearchDefaultAdminSort)
	explore.RenderUserSearch(ctx, user_model.SearchUserOptions{
		Actor:           ctx.Doer,
		Types:           []user_model.UserType{user_model.UserTypeOrganization},
		IncludeReserved: true, // administrator needs to list all accounts include reserved
		ListOptions: db.ListOptions{
			PageSize: setting.UI.Admin.OrgPagingNum,
		},
		Visible: []structs.VisibleType{structs.VisibleTypePublic, structs.VisibleTypeLimited, structs.VisibleTypePrivate},
		OrderBy: db.SearchOrderBy(sortOrder),
	}, tplOrgs)
}
