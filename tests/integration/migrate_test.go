// Copyright 2026 Hanzo AI, Inc. All rights reserved.
// SPDX-License-Identifier: MIT

package integration

import (
	"os"
	"path/filepath"
	"testing"

	issues_model "github.com/hanzoai/git/models/issues"
	"github.com/hanzoai/git/models/unittest"
	user_model "github.com/hanzoai/git/models/user"
	"github.com/hanzoai/git/modules/setting"
	"github.com/hanzoai/git/modules/structs"
	"github.com/hanzoai/git/modules/test"
	"github.com/hanzoai/git/services/migrations"

	"github.com/stretchr/testify/assert"
)

func TestMigrateLocalPath(t *testing.T) {
	assert.NoError(t, unittest.PrepareTestDatabase())
	defer test.MockVariableValue(&setting.ImportLocalPaths, true)()

	adminUser := unittest.AssertExistsAndLoadBean(t, &user_model.User{Name: "user1"})

	basePath := t.TempDir()

	lowercasePath := filepath.Join(basePath, "lowercase")
	err := os.Mkdir(lowercasePath, 0o700)
	assert.NoError(t, err)

	err = migrations.IsMigrateURLAllowed(lowercasePath, adminUser)
	assert.NoError(t, err, "case lowercase path")

	mixedcasePath := filepath.Join(basePath, "mIxeDCaSe")
	err = os.Mkdir(mixedcasePath, 0o700)
	assert.NoError(t, err)

	err = migrations.IsMigrateURLAllowed(mixedcasePath, adminUser)
	assert.NoError(t, err, "case mixedcase path")
}

func Test_UpdateCommentsMigrationsByType(t *testing.T) {
	assert.NoError(t, unittest.PrepareTestDatabase())

	err := issues_model.UpdateCommentsMigrationsByType(t.Context(), structs.GithubService, "1", 1)
	assert.NoError(t, err)
}
