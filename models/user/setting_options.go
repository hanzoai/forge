// Copyright 2026 Hanzo AI, Inc. All rights reserved.
// SPDX-License-Identifier: MIT

package user

const (
	// SettingsKeyHiddenCommentTypes is the setting key for hidden comment types
	SettingsKeyHiddenCommentTypes = "issue.hidden_comment_types"
	// SettingsKeyDiffWhitespaceBehavior is the setting key for whitespace behavior of diff
	SettingsKeyDiffWhitespaceBehavior = "diff.whitespace_behaviour"
	// SettingsKeyShowOutdatedComments is the setting key whether or not to show outdated comments in PRs
	SettingsKeyShowOutdatedComments = "comment_code.show_outdated"

	// SignupIP is the IP address that the user signed up with
	SignupIP = "signup.ip"
	// SignupUserAgent is the user agent that the user signed up with
	SignupUserAgent = "signup.user_agent"

	SettingsKeyCodeViewShowFileTree = "code_view.show_file_tree"

	SettingsKeyEmailNotificationActions        = "email_notification.gitea_actions" //nolint:gosec // the value is a stored user_setting key; renaming it needs a migration
	SettingEmailNotificationActionsAll         = "all"
	SettingEmailNotificationActionsFailureOnly = "failure-only" // Default for actions email preference
	SettingEmailNotificationActionsDisabled    = "disabled"

	SettingsKeyActionsConfig = "actions.config"
)
