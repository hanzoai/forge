// Copyright 2026 Hanzo AI, Inc. All rights reserved.
// Copyright 2026 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package integration

import (
	"fmt"
	"net/url"
	"os"
	"testing"

	actions_model "github.com/hanzoai/git/models/actions"
	auth_model "github.com/hanzoai/git/models/auth"
	"github.com/hanzoai/git/models/dbfs"
	repo_model "github.com/hanzoai/git/models/repo"
	"github.com/hanzoai/git/models/unittest"
	user_model "github.com/hanzoai/git/models/user"
	actions_module "github.com/hanzoai/git/modules/actions"
	runner_module "github.com/hanzoai/forge/modules/actions/runner"
	"github.com/hanzoai/git/modules/storage"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A runner that finalizes a task having printed nothing seals its log with no
// lines at all. A short-circuit on len(lines)==0 skipped TransferLogs and left
// an orphan dbfs_data row behind; this holds the row archived and removed.
func TestActionsLogFinalizeWithoutLines(t *testing.T) {
	onGitRun(t, func(t *testing.T, _ *url.URL) {
		user2 := unittest.AssertExistsAndLoadBean(t, &user_model.User{ID: 2})
		session := loginUser(t, user2.Name)
		token := getTokenForLoggedInUser(t, session, auth_model.AccessTokenScopeWriteRepository, auth_model.AccessTokenScopeWriteUser)

		apiRepo := createActionsTestRepo(t, token, "actions-finalize-no-rows", false)
		repo := unittest.AssertExistsAndLoadBean(t, &repo_model.Repository{ID: apiRepo.ID})

		runner := newMockRunner()
		runner.registerAsRepoRunner(t, user2.Name, repo.Name, "mock-runner", []string{"ubuntu-latest"}, false)

		const wfTreePath = ".hanzo/workflows/finalize-no-rows.yml"
		wfFileContent := fmt.Sprintf(`name: finalize-no-rows
on:
  push:
    paths:
      - '%s'
jobs:
  job1:
    runs-on: ubuntu-latest
    steps:
      - run: noop
`, wfTreePath)
		createWorkflowFile(t, token, user2.Name, repo.Name, wfTreePath, getWorkflowCreateFileOptions(user2, repo.DefaultBranch, "trigger", wfFileContent))

		task := runner.fetchTask(t)

		out, err := runner.appendLog(t, &runner_module.LogIn{
			Task:  task.ID,
			Index: 0,
			Lines: nil,
			Last:  true,
		})
		require.NoError(t, err)
		assert.EqualValues(t, 0, out.Ack)

		freshTask := unittest.AssertExistsAndLoadBean(t, &actions_model.ActionTask{ID: task.ID})
		require.True(t, freshTask.LogInStorage, "log_in_storage must flip when an empty log is sealed")

		_, err = storage.Actions.Stat(freshTask.LogFilename)
		assert.NoError(t, err, "archived log must exist in storage")

		_, err = dbfs.Open(t.Context(), actions_module.DBFSPrefix+freshTask.LogFilename)
		assert.ErrorIs(t, err, os.ErrNotExist, "DBFS row must be cleaned up after TransferLogs")

		// The runner re-sends its final append when the reply was lost. A sealed
		// log must ack the re-send and still refuse new lines.
		t.Run("re-sent finalize is idempotent", func(t *testing.T) {
			out, err := runner.appendLog(t, &runner_module.LogIn{Task: task.ID, Index: 0, Lines: nil, Last: true})
			require.NoError(t, err)
			assert.EqualValues(t, 0, out.Ack)

			_, err = runner.appendLog(t, &runner_module.LogIn{
				Task: task.ID, Index: 0, Lines: []runner_module.Line{{Content: "late"}}, Last: true,
			})
			require.Error(t, err, "appending past the seal must be refused")
		})
	})
}
