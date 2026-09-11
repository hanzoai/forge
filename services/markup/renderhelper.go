// Copyright 2026 Hanzo AI, Inc. All rights reserved.
// SPDX-License-Identifier: MIT

package markup

import (
	"context"

	"github.com/hanzoai/git/models/user"
	"github.com/hanzoai/git/modules/markup"
	git_context "github.com/hanzoai/git/services/context"
)

func FormalRenderHelperFuncs() *markup.RenderHelperFuncs {
	return &markup.RenderHelperFuncs{
		RenderRepoFileCodePreview: renderRepoFileCodePreview,
		RenderRepoIssueIconTitle:  renderRepoIssueIconTitle,
		IsUsernameMentionable: func(ctx context.Context, username string) bool {
			mentionedUser, err := user.GetUserByName(ctx, username)
			if err != nil {
				return false
			}

			webCtx := git_context.GetWebContext(ctx)
			if webCtx == nil {
				// when using general context, use user's visibility to check
				return mentionedUser.Visibility.IsPublic()
			}

			// when using the web context, use user's visibility and user's permission to check
			return user.IsUserVisibleToViewer(webCtx, mentionedUser, webCtx.Doer)
		},
	}
}
