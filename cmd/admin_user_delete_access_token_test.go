// Copyright 2026 Hanzo AI, Inc. All rights reserved.
// SPDX-License-Identifier: MIT

package cmd

import (
	"strconv"
	"testing"

	auth_model "github.com/hanzoai/git/models/auth"
	"github.com/hanzoai/git/models/db"
	"github.com/hanzoai/git/models/unittest"
	user_model "github.com/hanzoai/git/models/user"

	"github.com/stretchr/testify/require"
)

func tokensOf(t *testing.T, username string) []*auth_model.AccessToken {
	t.Helper()
	u := unittest.AssertExistsAndLoadBean(t, &user_model.User{LowerName: username})
	tokens, err := db.Find[auth_model.AccessToken](t.Context(), auth_model.ListAccessTokensOptions{UserID: u.ID})
	require.NoError(t, err)
	return tokens
}

func TestAdminUserDeleteAccessToken(t *testing.T) {
	ctx := t.Context()
	defer func() {
		require.NoError(t, db.TruncateBeans(t.Context(), &user_model.User{}))
		require.NoError(t, db.TruncateBeans(t.Context(), &user_model.EmailAddress{}))
		require.NoError(t, db.TruncateBeans(t.Context(), &auth_model.AccessToken{}))
	}()

	setup := func(t *testing.T, username, tokenName string) {
		t.Helper()
		require.NoError(t, microcmdUserCreate().Run(t.Context(),
			[]string{"create", "--username", username, "--email", username + "@gitea.local", "--random-password"}))
		require.NoError(t, newUserGenerateAccessTokenCommand().Run(t.Context(),
			[]string{"generate-access-token", "--username", username, "--token-name", tokenName, "--scopes", "read:user"}))
	}

	t.Run("list runs and the token is visible to the model", func(t *testing.T) {
		setup(t, "tokenuser", "kept")
		require.NoError(t, newUserListAccessTokensCommand().Run(ctx, []string{"list-access-tokens", "--username", "tokenuser"}))
		require.Len(t, tokensOf(t, "tokenuser"), 1)
	})

	t.Run("delete by name", func(t *testing.T) {
		require.NoError(t, newUserDeleteAccessTokenCommand().Run(ctx,
			[]string{"delete-access-token", "--username", "tokenuser", "--token-name", "kept"}))
		require.Empty(t, tokensOf(t, "tokenuser"))
	})

	t.Run("delete by id", func(t *testing.T) {
		require.NoError(t, newUserGenerateAccessTokenCommand().Run(ctx,
			[]string{"generate-access-token", "--username", "tokenuser", "--token-name", "byid", "--scopes", "read:user"}))
		tokens := tokensOf(t, "tokenuser")
		require.Len(t, tokens, 1)
		require.NoError(t, newUserDeleteAccessTokenCommand().Run(ctx,
			[]string{"delete-access-token", "--username", "tokenuser", "--id", strconv.FormatInt(tokens[0].ID, 10)}))
		require.Empty(t, tokensOf(t, "tokenuser"))
	})

	// The reason this command exists instead of a hand-written DELETE: one
	// table holds every user's tokens, so the id must be scoped by owner.
	t.Run("an id belonging to another user is not deleted", func(t *testing.T) {
		setup(t, "victim", "victimtoken")
		victim := tokensOf(t, "victim")
		require.Len(t, victim, 1)

		err := newUserDeleteAccessTokenCommand().Run(ctx,
			[]string{"delete-access-token", "--username", "tokenuser", "--id", strconv.FormatInt(victim[0].ID, 10)})
		require.Error(t, err)
		require.Contains(t, err.Error(), "access token not found")
		require.Len(t, tokensOf(t, "victim"), 1)
	})
}

func TestAdminUserDeleteAccessTokenFailure(t *testing.T) {
	defer func() {
		require.NoError(t, db.TruncateBeans(t.Context(), &user_model.User{}))
		require.NoError(t, db.TruncateBeans(t.Context(), &user_model.EmailAddress{}))
		require.NoError(t, db.TruncateBeans(t.Context(), &auth_model.AccessToken{}))
	}()
	require.NoError(t, microcmdUserCreate().Run(t.Context(),
		[]string{"create", "--username", "tokenuser", "--email", "tokenuser@gitea.local", "--random-password"}))

	for _, tc := range []struct {
		name, expectedErr string
		args              []string
	}{
		{
			name:        "no username",
			args:        []string{"delete-access-token", "--token-name", "x"},
			expectedErr: "you must provide a username",
		},
		{
			name:        "neither name nor id",
			args:        []string{"delete-access-token", "--username", "tokenuser"},
			expectedErr: "exactly one of --token-name or --id",
		},
		{
			name:        "both name and id",
			args:        []string{"delete-access-token", "--username", "tokenuser", "--token-name", "x", "--id", "1"},
			expectedErr: "exactly one of --token-name or --id",
		},
		{
			name:        "unknown token name",
			args:        []string{"delete-access-token", "--username", "tokenuser", "--token-name", "nosuchtoken"},
			expectedErr: `has no access token named "nosuchtoken"`,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := newUserDeleteAccessTokenCommand().Run(t.Context(), tc.args)
			require.Error(t, err)
			require.Contains(t, err.Error(), tc.expectedErr)
		})
	}
}
