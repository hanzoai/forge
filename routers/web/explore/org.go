// Copyright 2021 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package explore

import (
	"github.com/hanzoai/git/models/db"
	user_model "github.com/hanzoai/git/models/user"
	"github.com/hanzoai/git/modules/container"
	"github.com/hanzoai/git/modules/setting"
	"github.com/hanzoai/git/modules/structs"
	"github.com/hanzoai/git/modules/util"
	"github.com/hanzoai/git/services/context"
)

// Organizations render explore organizations page
func Organizations(ctx *context.Context) {
	if setting.Service.Explore.DisableOrganizationsPage {
		ctx.Redirect(setting.AppSubURL + "/explore")
		return
	}

	ctx.Data["UsersPageIsDisabled"] = setting.Service.Explore.DisableUsersPage
	ctx.Data["CodePageIsDisabled"] = setting.Service.Explore.DisableCodePage
	ctx.Data["Title"] = ctx.Tr("explore_title")
	ctx.Data["PageIsExplore"] = true
	ctx.Data["PageIsExploreOrganizations"] = true
	ctx.Data["IsRepoIndexerEnabled"] = setting.Indexer.RepoIndexerEnabled

	visibleTypes := []structs.VisibleType{structs.VisibleTypePublic}
	if ctx.Doer != nil {
		visibleTypes = append(visibleTypes, structs.VisibleTypeLimited, structs.VisibleTypePrivate)
	}

	supportedSortOrders := container.SetOf(
		"newest",
		"oldest",
		"alphabetically",
		"reversealphabetically",
	)
	sortOrderDefault := util.Iif(supportedSortOrders.Contains(setting.UI.ExploreDefaultSort), setting.UI.ExploreDefaultSort, "newest")
	sortOrder := ctx.FormString("sort", sortOrderDefault)
	RenderUserSearch(ctx, user_model.SearchUserOptions{
		Actor:       ctx.Doer,
		Types:       []user_model.UserType{user_model.UserTypeOrganization},
		ListOptions: db.ListOptions{PageSize: setting.UI.ExplorePagingNum},
		Visible:     visibleTypes,
		OrderBy:     db.SearchOrderBy(sortOrder),

		SupportedSortOrders: supportedSortOrders,
	}, tplExploreUsers)
}
