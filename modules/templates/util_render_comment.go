// Copyright 2026 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package templates

import (
	"html/template"
	"strings"

	issues_model "github.com/hanzoai/git/models/issues"
	"github.com/hanzoai/git/modules/htmlutil"
	"github.com/hanzoai/git/modules/setting"
	"github.com/hanzoai/git/modules/svg"
	"github.com/hanzoai/git/modules/translation"
	"github.com/hanzoai/git/modules/util"
)

func commentTimelineEventIsWipToggle(c *issues_model.Comment) (isToggle, isWip bool) {
	title1, ok1 := issues_model.CutWorkInProgressPrefix(c.OldTitle)
	title2, ok2 := issues_model.CutWorkInProgressPrefix(c.NewTitle)
	return ok1 != ok2 && strings.TrimSpace(title1) == strings.TrimSpace(title2), ok2
}

func (ut *RenderUtils) RenderTimelineEventBadge(c *issues_model.Comment) template.HTML {
	if c.Type == issues_model.CommentTypeChangeTitle {
		isToggle, isWip := commentTimelineEventIsWipToggle(c)
		if !isToggle {
			return svg.RenderHTML("octicon-pencil")
		}
		return util.Iif(isWip, svg.RenderHTML("octicon-git-pull-request-draft"), svg.RenderHTML("octicon-eye"))
	}
	setting.PanicInDevOrTesting("unimplemented comment type %v: %v", c.Type, c)
	return htmlutil.HTMLFormat("(CommentType:%v)", c.Type)
}

func (ut *RenderUtils) RenderTimelineEventComment(c *issues_model.Comment, createdStr template.HTML) template.HTML {
	if c.Type == issues_model.CommentTypeChangeTitle {
		locale := ut.ctx.Value(translation.ContextKey).(translation.Locale)
		isToggle, isWip := commentTimelineEventIsWipToggle(c)
		if !isToggle {
			return locale.Tr("repo.issues.change_title_at", ut.RenderEmoji(c.OldTitle), ut.RenderEmoji(c.NewTitle), createdStr)
		}
		trKey := util.Iif(isWip, "repo.pulls.marked_as_work_in_progress_at", "repo.pulls.marked_as_ready_for_review_at")
		return locale.Tr(trKey, createdStr)
	}
	setting.PanicInDevOrTesting("unimplemented comment type %v: %v", c.Type, c)
	return htmlutil.HTMLFormat("(Comment:%v,%v)", c.Type, c.Content)
}
