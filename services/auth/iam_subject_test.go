// Copyright 2026 Hanzo AI, Inc. All rights reserved.
// SPDX-License-Identifier: MIT

package auth

import (
	"testing"

	auth_model "github.com/hanzoai/git/models/auth"
	"github.com/hanzoai/git/models/db"
	"github.com/hanzoai/git/models/unittest"
	user_model "github.com/hanzoai/git/models/user"
	"github.com/hanzoai/git/modules/setting"
	"github.com/hanzoai/git/modules/test"

	"github.com/stretchr/testify/assert"
)

// A provisioned account (login_source/login_name = IAM subject) resolves; a
// seat binding, an unknown subject and an ambiguous one do not.
func TestSubjectUserBinding(t *testing.T) {
	assert.NoError(t, unittest.PrepareTestDatabase())
	ctx := t.Context()
	defer test.MockVariableValue(&setting.IAM.LoginSource, "7")()

	bind := func(id int64, src int64, name string) {
		_, err := db.GetEngine(ctx).ID(id).Cols("login_source", "login_name", "login_type").
			Update(&user_model.User{LoginSource: src, LoginName: name, LoginType: auth_model.OAuth2})
		assert.NoError(t, err)
	}
	bind(2, 7, "sub-alice")
	bind(4, 7, "seat:sub-bob")
	bind(5, 7, "sub-dup")
	bind(8, 7, "sub-dup")
	bind(9, 3, "sub-other-source")

	u, err := subjectUser(ctx, 0, "sub-alice")
	assert.NoError(t, err)
	if assert.NotNil(t, u) {
		assert.EqualValues(t, 2, u.ID)
	}
	for _, sub := range []string{"sub-bob", "sub-nobody", "sub-dup", "sub-other-source"} {
		u, err := subjectUser(ctx, 0, sub)
		assert.NoError(t, err)
		assert.Nil(t, u, sub)
	}

	// Without a numeric LOGIN_SOURCE any OAuth2-bound account matches.
	setting.IAM.LoginSource = "hanzo"
	u, err = subjectUser(ctx, 0, "sub-other-source")
	assert.NoError(t, err)
	if assert.NotNil(t, u) {
		assert.EqualValues(t, 9, u.ID)
	}
}
