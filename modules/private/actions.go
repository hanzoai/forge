// Copyright 2026 Hanzo AI, Inc. All rights reserved.
// Copyright 2023 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package private

import (
	"context"
)

type GenerateTokenRequest struct {
	Scope string
}

// GenerateActionsRunnerToken calls the internal GenerateActionsRunnerToken function
func GenerateActionsRunnerToken(ctx context.Context, scope string) (*ResponseText, ResponseExtra) {
	reqURL := internalURL("actions/generate_actions_runner_token")

	req := newInternalRequestAPI(ctx, reqURL, "POST", GenerateTokenRequest{
		Scope: scope,
	})

	return requestJSONResp(req, &ResponseText{})
}
