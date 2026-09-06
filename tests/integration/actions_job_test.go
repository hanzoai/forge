// Copyright 2026 Hanzo AI, Inc. All rights reserved.
// Copyright 2024 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package integration

import (
	"encoding/base64"
	"fmt"
	"net/http"
	"net/url"
	"reflect"
	"strconv"
	"testing"
	"time"

	actions_model "github.com/hanzoai/git/models/actions"
	auth_model "github.com/hanzoai/git/models/auth"
	"github.com/hanzoai/git/models/db"
	repo_model "github.com/hanzoai/git/models/repo"
	"github.com/hanzoai/git/models/unittest"
	user_model "github.com/hanzoai/git/models/user"
	runner_module "github.com/hanzoai/git/modules/actions/runner"
	"github.com/hanzoai/git/modules/git"
	"github.com/hanzoai/git/modules/json"
	"github.com/hanzoai/git/modules/setting"
	api "github.com/hanzoai/git/modules/structs"
	"github.com/hanzoai/git/modules/timeutil"
	actions_service "github.com/hanzoai/git/services/actions"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestJobWithNeeds(t *testing.T) {
	testCases := []struct {
		treePath         string
		fileContent      string
		outcomes         map[string]*mockTaskOutcome
		expectedStatuses map[string]string
	}{
		{
			treePath: ".hanzo/workflows/job-with-needs.yml",
			fileContent: `name: job-with-needs
on:
  push:
    paths:
      - '.hanzo/workflows/job-with-needs.yml'
jobs:
  job1:
    runs-on: ubuntu-latest
    steps:
      - run: echo job1
  job2:
    runs-on: ubuntu-latest
    needs: [job1]
    steps:
      - run: echo job2
`,
			outcomes: map[string]*mockTaskOutcome{
				"job1": {
					result: runner_module.Success,
				},
				"job2": {
					result: runner_module.Success,
				},
			},
			expectedStatuses: map[string]string{
				"job1": actions_model.StatusSuccess.String(),
				"job2": actions_model.StatusSuccess.String(),
			},
		},
		{
			treePath: ".hanzo/workflows/job-with-needs-fail.yml",
			fileContent: `name: job-with-needs-fail
on:
  push:
    paths:
      - '.hanzo/workflows/job-with-needs-fail.yml'
jobs:
  job1:
    runs-on: ubuntu-latest
    steps:
      - run: echo job1
  job2:
    runs-on: ubuntu-latest
    needs: [job1]
    steps:
      - run: echo job2
`,
			outcomes: map[string]*mockTaskOutcome{
				"job1": {
					result: runner_module.Failure,
				},
			},
			expectedStatuses: map[string]string{
				"job1": actions_model.StatusFailure.String(),
				"job2": actions_model.StatusSkipped.String(),
			},
		},
		{
			treePath: ".hanzo/workflows/job-with-needs-fail-if.yml",
			fileContent: `name: job-with-needs-fail-if
on:
  push:
    paths:
      - '.hanzo/workflows/job-with-needs-fail-if.yml'
jobs:
  job1:
    runs-on: ubuntu-latest
    steps:
      - run: echo job1
  job2:
    runs-on: ubuntu-latest
    if: ${{ always() }}
    needs: [job1]
    steps:
      - run: echo job2
`,
			outcomes: map[string]*mockTaskOutcome{
				"job1": {
					result: runner_module.Failure,
				},
				"job2": {
					result: runner_module.Success,
				},
			},
			expectedStatuses: map[string]string{
				"job1": actions_model.StatusFailure.String(),
				"job2": actions_model.StatusSuccess.String(),
			},
		},
	}
	onGitRun(t, func(t *testing.T, u *url.URL) {
		user2 := unittest.AssertExistsAndLoadBean(t, &user_model.User{ID: 2})
		session := loginUser(t, user2.Name)
		token := getTokenForLoggedInUser(t, session, auth_model.AccessTokenScopeWriteRepository, auth_model.AccessTokenScopeWriteUser)

		apiRepo := createActionsTestRepo(t, token, "actions-jobs-with-needs", false)
		runner := newMockRunner()
		runner.registerAsRepoRunner(t, user2.Name, apiRepo.Name, "mock-runner", []string{"ubuntu-latest"}, false)

		for _, tc := range testCases {
			t.Run("test "+tc.treePath, func(t *testing.T) {
				// create the workflow file
				opts := getWorkflowCreateFileOptions(user2, apiRepo.DefaultBranch, "create "+tc.treePath, tc.fileContent)
				fileResp := createWorkflowFile(t, token, user2.Name, apiRepo.Name, tc.treePath, opts)

				// fetch and execute task
				for i := 0; i < len(tc.outcomes); i++ {
					task := runner.fetchTask(t)
					jobName := getTaskJobNameByTaskID(t, token, user2.Name, apiRepo.Name, task.ID)
					outcome := tc.outcomes[jobName]
					assert.NotNil(t, outcome)
					runner.execTask(t, task, outcome)
				}

				// check result
				req := NewRequest(t, "GET", fmt.Sprintf("/v1/repos/%s/%s/actions/tasks", user2.Name, apiRepo.Name)).
					AddTokenAuth(token)
				resp := MakeRequest(t, req, http.StatusOK)
				actionTaskRespAfter := DecodeJSON(t, resp, &api.ActionTaskResponse{})
				for _, apiTask := range actionTaskRespAfter.Entries {
					if apiTask.HeadSHA != fileResp.Commit.SHA {
						continue
					}
					status := apiTask.Status
					assert.Equal(t, status, tc.expectedStatuses[apiTask.Name])
				}
			})
		}
	})
}

// wantNeed is what one entry of a task's needs should say. It keeps outputs as a
// map because this is test data a human reads, while the wire carries an ordered
// list.
type wantNeed struct {
	Result  runner_module.Result
	Outputs map[string]string
}

func TestJobNeedsMatrix(t *testing.T) {
	testCases := []struct {
		treePath          string
		fileContent       string
		outcomes          map[string]*mockTaskOutcome
		expectedTaskNeeds map[string]wantNeed // jobID => what that need should say
	}{
		{
			treePath: ".hanzo/workflows/jobs-outputs-with-matrix.yml",
			fileContent: `name: jobs-outputs-with-matrix
on:
  push:
    paths:
      - '.hanzo/workflows/jobs-outputs-with-matrix.yml'
jobs:
  job1:
    runs-on: ubuntu-latest
    outputs:
      output_1: ${{ steps.gen_output.outputs.output_1 }}
      output_2: ${{ steps.gen_output.outputs.output_2 }}
      output_3: ${{ steps.gen_output.outputs.output_3 }}
    strategy:
      matrix:
        version: [1, 2, 3]
    steps:
      - name: Generate output
        id: gen_output
        run: |
          version="${{ matrix.version }}"
          echo "output_${version}=${version}" >> "$GITHUB_OUTPUT"
  job2:
    runs-on: ubuntu-latest
    needs: [job1]
    steps:
      - run: echo '${{ toJSON(needs.job1.outputs) }}'
`,
			outcomes: map[string]*mockTaskOutcome{
				"job1 (1)": {
					result: runner_module.Success,
					outputs: map[string]string{
						"output_1": "1",
						"output_2": "",
						"output_3": "",
					},
				},
				"job1 (2)": {
					result: runner_module.Success,
					outputs: map[string]string{
						"output_1": "",
						"output_2": "2",
						"output_3": "",
					},
				},
				"job1 (3)": {
					result: runner_module.Success,
					outputs: map[string]string{
						"output_1": "",
						"output_2": "",
						"output_3": "3",
					},
				},
			},
			expectedTaskNeeds: map[string]wantNeed{
				"job1": {
					Result: runner_module.Success,
					Outputs: map[string]string{
						"output_1": "1",
						"output_2": "2",
						"output_3": "3",
					},
				},
			},
		},
		{
			treePath: ".hanzo/workflows/jobs-outputs-with-matrix-failure.yml",
			fileContent: `name: jobs-outputs-with-matrix-failure
on:
  push:
    paths:
      - '.hanzo/workflows/jobs-outputs-with-matrix-failure.yml'
jobs:
  job1:
    runs-on: ubuntu-latest
    outputs:
      output_1: ${{ steps.gen_output.outputs.output_1 }}
      output_2: ${{ steps.gen_output.outputs.output_2 }}
      output_3: ${{ steps.gen_output.outputs.output_3 }}
    strategy:
      matrix:
        version: [1, 2, 3]
    steps:
      - name: Generate output
        id: gen_output
        run: |
          version="${{ matrix.version }}"
          echo "output_${version}=${version}" >> "$GITHUB_OUTPUT"
  job2:
    runs-on: ubuntu-latest
    if: ${{ always() }}
    needs: [job1]
    steps:
      - run: echo '${{ toJSON(needs.job1.outputs) }}'
`,
			outcomes: map[string]*mockTaskOutcome{
				"job1 (1)": {
					result: runner_module.Success,
					outputs: map[string]string{
						"output_1": "1",
						"output_2": "",
						"output_3": "",
					},
				},
				"job1 (2)": {
					result: runner_module.Failure,
					outputs: map[string]string{
						"output_1": "",
						"output_2": "",
						"output_3": "",
					},
				},
				"job1 (3)": {
					result: runner_module.Success,
					outputs: map[string]string{
						"output_1": "",
						"output_2": "",
						"output_3": "3",
					},
				},
			},
			expectedTaskNeeds: map[string]wantNeed{
				"job1": {
					Result: runner_module.Failure,
					Outputs: map[string]string{
						"output_1": "1",
						"output_2": "",
						"output_3": "3",
					},
				},
			},
		},
	}
	onGitRun(t, func(t *testing.T, u *url.URL) {
		user2 := unittest.AssertExistsAndLoadBean(t, &user_model.User{ID: 2})
		session := loginUser(t, user2.Name)
		token := getTokenForLoggedInUser(t, session, auth_model.AccessTokenScopeWriteRepository, auth_model.AccessTokenScopeWriteUser)

		apiRepo := createActionsTestRepo(t, token, "actions-jobs-outputs-with-matrix", false)
		runner := newMockRunner()
		runner.registerAsRepoRunner(t, user2.Name, apiRepo.Name, "mock-runner", []string{"ubuntu-latest"}, false)

		for _, tc := range testCases {
			t.Run("test "+tc.treePath, func(t *testing.T) {
				opts := getWorkflowCreateFileOptions(user2, apiRepo.DefaultBranch, "create "+tc.treePath, tc.fileContent)
				createWorkflowFile(t, token, user2.Name, apiRepo.Name, tc.treePath, opts)

				for i := 0; i < len(tc.outcomes); i++ {
					task := runner.fetchTask(t)
					jobName := getTaskJobNameByTaskID(t, token, user2.Name, apiRepo.Name, task.ID)
					outcome := tc.outcomes[jobName]
					assert.NotNil(t, outcome)
					runner.execTask(t, task, outcome)
				}

				task := runner.fetchTask(t)
				assert.Len(t, task.Needs, len(tc.expectedTaskNeeds))
				byJob := make(map[string]runner_module.Need, len(task.Needs))
				for _, need := range task.Needs {
					byJob[need.Job] = need
				}
				for jobID, want := range tc.expectedTaskNeeds {
					got := byJob[jobID]
					assert.Equal(t, want.Result, got.Result)
					assert.Len(t, got.Outputs, len(want.Outputs))
					for _, out := range got.Outputs {
						assert.Equal(t, want.Outputs[out.Name], out.Value)
					}
				}
			})
		}
	})
}

func TestRunnerDisableEnable(t *testing.T) {
	onGitRun(t, func(t *testing.T, _ *url.URL) {
		user2 := unittest.AssertExistsAndLoadBean(t, &user_model.User{ID: 2})
		session := loginUser(t, user2.Name)
		token := getTokenForLoggedInUser(t, session, auth_model.AccessTokenScopeWriteRepository, auth_model.AccessTokenScopeWriteUser)

		t.Run("BasicDisableEnable", func(t *testing.T) {
			testData := prepareRunnerDisableEnableTest(t, user2, token, "actions-runner-disable-enable", "mock-runner", "runner-disable-enable")

			task1 := testData.runner.fetchTask(t)
			require.NotNil(t, task1)

			triggerRunnerDisableEnableRun(t, user2, token, testData.repo, "second-push.txt")

			req := newRunnerUpdateRequest(t, fmt.Sprintf("/v1/repos/%s/%s/actions/runners/%d", user2.Name, testData.repo.Name, testData.runnerID), true).AddTokenAuth(token)
			MakeRequest(t, req, http.StatusOK)

			testData.runner.execTask(t, task1, &mockTaskOutcome{result: runner_module.Success})
			testData.runner.fetchNoTask(t, 2*time.Second)

			req = newRunnerUpdateRequest(t, fmt.Sprintf("/v1/repos/%s/%s/actions/runners/%d", user2.Name, testData.repo.Name, testData.runnerID), false).AddTokenAuth(token)
			MakeRequest(t, req, http.StatusOK)

			task2 := testData.runner.fetchTask(t, 5*time.Second)
			require.NotNil(t, task2)
			testData.runner.execTask(t, task2, &mockTaskOutcome{result: runner_module.Success})
		})

		t.Run("TasksVersionPath", func(t *testing.T) {
			testData := prepareRunnerDisableEnableTest(t, user2, token, "actions-runner-version-path", "mock-runner-version-path", "runner-version-path")

			var firstVersion int64
			var task1 *runner_module.Task
			ddl := time.Now().Add(5 * time.Second)
			for time.Now().Before(ddl) {
				task1, firstVersion = testData.runner.fetchTaskOnce(t, 0)
				if task1 != nil {
					break
				}
				time.Sleep(200 * time.Millisecond)
			}
			require.NotNil(t, task1, "expected to receive first task")
			require.NotZero(t, firstVersion, "response TasksVersion should be set")

			// Trigger a second run so there is a pending job after we re-enable the runner
			triggerRunnerDisableEnableRun(t, user2, token, testData.repo, "second-push.txt")
			time.Sleep(500 * time.Millisecond) // allow workflow run to be created

			req := newRunnerUpdateRequest(t, fmt.Sprintf("/v1/repos/%s/%s/actions/runners/%d", user2.Name, testData.repo.Name, testData.runnerID), true).AddTokenAuth(token)
			MakeRequest(t, req, http.StatusOK)

			testData.runner.execTask(t, task1, &mockTaskOutcome{result: runner_module.Success})

			// Fetch with the version we had before disable. Server has bumped version on disable,
			// so we enter PickTask with a re-loaded runner (disabled) and get no task.
			taskAfterDisable, _ := testData.runner.fetchTaskOnce(t, firstVersion)
			assert.Nil(t, taskAfterDisable, "disabled runner must not receive a task when sending previous TasksVersion")

			req = newRunnerUpdateRequest(t, fmt.Sprintf("/v1/repos/%s/%s/actions/runners/%d", user2.Name, testData.repo.Name, testData.runnerID), false).AddTokenAuth(token)
			MakeRequest(t, req, http.StatusOK)

			task2 := testData.runner.fetchTask(t, 5*time.Second)
			require.NotNil(t, task2, "after re-enable runner should receive tasks again")
			testData.runner.execTask(t, task2, &mockTaskOutcome{result: runner_module.Success})
		})
	})
}

type runnerDisableEnableTestData struct {
	repo     *api.Repository
	runner   *mockRunner
	runnerID int64
}

func prepareRunnerDisableEnableTest(t *testing.T, user *user_model.User, authToken, repoName, runnerName, workflowName string) *runnerDisableEnableTestData {
	t.Helper()

	apiRepo := createActionsTestRepo(t, authToken, repoName, false)
	runner := newMockRunner()
	runner.registerAsRepoRunner(t, user.Name, apiRepo.Name, runnerName, []string{"ubuntu-latest"}, false)

	wfTreePath := fmt.Sprintf(".hanzo/workflows/%s.yml", workflowName)
	wfContent := fmt.Sprintf(`name: %s
on: [push]
jobs:
  build:
    runs-on: ubuntu-latest
    steps:
      - run: echo %s
`, workflowName, workflowName)
	opts := getWorkflowCreateFileOptions(user, apiRepo.DefaultBranch, "create workflow", wfContent)
	createWorkflowFile(t, authToken, user.Name, apiRepo.Name, wfTreePath, opts)

	return &runnerDisableEnableTestData{
		repo:     apiRepo,
		runner:   runner,
		runnerID: getRepoRunnerID(t, authToken, user.Name, apiRepo.Name),
	}
}

func triggerRunnerDisableEnableRun(t *testing.T, user *user_model.User, authToken string, repo *api.Repository, treePath string) {
	t.Helper()
	opts := getWorkflowCreateFileOptions(user, repo.DefaultBranch, "second push", "second run")
	createWorkflowFile(t, authToken, user.Name, repo.Name, treePath, opts)
}

func getRepoRunnerID(t *testing.T, authToken, ownerName, repoName string) int64 {
	t.Helper()
	req := NewRequest(t, "GET", fmt.Sprintf("/v1/repos/%s/%s/actions/runners", ownerName, repoName)).AddTokenAuth(authToken)
	resp := MakeRequest(t, req, http.StatusOK)
	runnerList := DecodeJSON(t, resp, &api.ActionRunnersResponse{})
	require.Len(t, runnerList.Entries, 1)
	return runnerList.Entries[0].ID
}

func TestActionsGitContext(t *testing.T) {
	onGitRun(t, func(t *testing.T, u *url.URL) {
		user2 := unittest.AssertExistsAndLoadBean(t, &user_model.User{ID: 2})
		user2Session := loginUser(t, user2.Name)
		user2Token := getTokenForLoggedInUser(t, user2Session, auth_model.AccessTokenScopeWriteRepository, auth_model.AccessTokenScopeWriteUser)

		apiBaseRepo := createActionsTestRepo(t, user2Token, "actions-gitea-context", false)
		baseRepo := unittest.AssertExistsAndLoadBean(t, &repo_model.Repository{ID: apiBaseRepo.ID})
		user2APICtx := NewAPITestContext(t, baseRepo.OwnerName, baseRepo.Name, auth_model.AccessTokenScopeWriteRepository)

		runner := newMockRunner()
		runner.registerAsRepoRunner(t, baseRepo.OwnerName, baseRepo.Name, "mock-runner", []string{"ubuntu-latest"}, false)

		// init the workflow
		wfTreePath := ".hanzo/workflows/pull.yml"
		wfFileContent := `name: Pull Request
on: pull_request
jobs:
  wf1-job:
    runs-on: ubuntu-latest
    steps:
      - run: echo 'test the pull'
`
		opts := getWorkflowCreateFileOptions(user2, baseRepo.DefaultBranch, "create "+wfTreePath, wfFileContent)
		createWorkflowFile(t, user2Token, baseRepo.OwnerName, baseRepo.Name, wfTreePath, opts)
		// user2 creates a pull request
		doAPICreateFile(user2APICtx, "user2-patch.txt", &api.CreateFileOptions{
			FileOptions: api.FileOptions{
				NewBranchName: "user2/patch-1",
				Message:       "create user2-patch.txt",
				Author: api.Identity{
					Name:  user2.Name,
					Email: user2.Email,
				},
				Committer: api.Identity{
					Name:  user2.Name,
					Email: user2.Email,
				},
				Dates: api.CommitDateOptions{
					Author:    time.Now(),
					Committer: time.Now(),
				},
			},
			ContentBase64: base64.StdEncoding.EncodeToString([]byte("user2-fix")),
		})(t)
		apiPull, err := doAPICreatePullRequest(user2APICtx, baseRepo.OwnerName, baseRepo.Name, baseRepo.DefaultBranch, "user2/patch-1")(t)
		assert.NoError(t, err)
		task := runner.fetchTask(t)
		gtCtx := task.Context
		actionTask := unittest.AssertExistsAndLoadBean(t, &actions_model.ActionTask{ID: task.ID})
		actionRunJob := unittest.AssertExistsAndLoadBean(t, &actions_model.ActionRunJob{ID: actionTask.JobID})
		actionRun := unittest.AssertExistsAndLoadBean(t, &actions_model.ActionRun{ID: actionRunJob.RunID})
		assert.NoError(t, actionRun.LoadAttributes(t.Context()))

		assertTaskContext(t, gtCtx, actionTask, actionRunJob, actionRun, apiPull)
	})
}

// Ephemeral
func TestActionsGitContextEphemeral(t *testing.T) {
	onGitRun(t, func(t *testing.T, u *url.URL) {
		user2 := unittest.AssertExistsAndLoadBean(t, &user_model.User{ID: 2})
		user2Session := loginUser(t, user2.Name)
		user2Token := getTokenForLoggedInUser(t, user2Session, auth_model.AccessTokenScopeWriteRepository, auth_model.AccessTokenScopeWriteUser)

		apiBaseRepo := createActionsTestRepo(t, user2Token, "actions-gitea-context", false)
		baseRepo := unittest.AssertExistsAndLoadBean(t, &repo_model.Repository{ID: apiBaseRepo.ID})
		user2APICtx := NewAPITestContext(t, baseRepo.OwnerName, baseRepo.Name, auth_model.AccessTokenScopeWriteRepository)

		runner := newMockRunner()
		runner.registerAsRepoRunner(t, baseRepo.OwnerName, baseRepo.Name, "mock-runner", []string{"ubuntu-latest"}, true)

		// verify CleanupEphemeralRunners does not remove this runner
		err := actions_service.CleanupEphemeralRunners(t.Context())
		assert.NoError(t, err)

		// init the workflow
		wfTreePath := ".hanzo/workflows/pull.yml"
		wfFileContent := `name: Pull Request
on: pull_request
jobs:
  wf1-job:
    runs-on: ubuntu-latest
    steps:
      - run: echo 'test the pull'
  wf2-job:
    runs-on: ubuntu-latest
    steps:
      - run: echo 'test the pull'
`
		opts := getWorkflowCreateFileOptions(user2, baseRepo.DefaultBranch, "create "+wfTreePath, wfFileContent)
		createWorkflowFile(t, user2Token, baseRepo.OwnerName, baseRepo.Name, wfTreePath, opts)
		// user2 creates a pull request
		doAPICreateFile(user2APICtx, "user2-patch.txt", &api.CreateFileOptions{
			FileOptions: api.FileOptions{
				NewBranchName: "user2/patch-1",
				Message:       "create user2-patch.txt",
				Author: api.Identity{
					Name:  user2.Name,
					Email: user2.Email,
				},
				Committer: api.Identity{
					Name:  user2.Name,
					Email: user2.Email,
				},
				Dates: api.CommitDateOptions{
					Author:    time.Now(),
					Committer: time.Now(),
				},
			},
			ContentBase64: base64.StdEncoding.EncodeToString([]byte("user2-fix")),
		})(t)
		apiPull, err := doAPICreatePullRequest(user2APICtx, baseRepo.OwnerName, baseRepo.Name, baseRepo.DefaultBranch, "user2/patch-1")(t)
		assert.NoError(t, err)
		task := runner.fetchTask(t)
		gtCtx := task.Context
		actionTask := unittest.AssertExistsAndLoadBean(t, &actions_model.ActionTask{ID: task.ID})
		actionRunJob := unittest.AssertExistsAndLoadBean(t, &actions_model.ActionRunJob{ID: actionTask.JobID})
		actionRun := unittest.AssertExistsAndLoadBean(t, &actions_model.ActionRun{ID: actionRunJob.RunID})
		assert.NoError(t, actionRun.LoadAttributes(t.Context()))

		assertTaskContext(t, gtCtx, actionTask, actionRunJob, actionRun, apiPull)

		// verify CleanupEphemeralRunners does not remove this runner
		err = actions_service.CleanupEphemeralRunners(t.Context())
		assert.NoError(t, err)

		out, err := runner.task(t, &runner_module.TaskIn{Queue: 0})
		assert.NoError(t, err)
		assert.Nil(t, out.Task)

		// verify CleanupEphemeralRunners does not remove this runner
		err = actions_service.CleanupEphemeralRunners(t.Context())
		assert.NoError(t, err)

		_, err = runner.state(t, &runner_module.StateIn{
			State: runner_module.State{ID: actionTask.ID, Result: runner_module.Success},
		})
		assert.NoError(t, err)

		// The ephemeral runner removed itself when its task finished, so it can no
		// longer be placed and every later call is refused.
		out, err = runner.task(t, &runner_module.TaskIn{Queue: 0})
		assert.Error(t, err)
		assert.Nil(t, out)

		out, err = runner.task(t, &runner_module.TaskIn{Queue: 0})
		assert.Error(t, err)
		assert.Nil(t, out)

		// create a runner that picks a job and get force cancelled
		runnerToBeRemoved := newMockRunner()
		runnerToBeRemoved.registerAsRepoRunner(t, baseRepo.OwnerName, baseRepo.Name, "mock-runner-to-be-removed", []string{"ubuntu-latest"}, true)

		taskToStopAPIObj := runnerToBeRemoved.fetchTask(t)

		taskToStop := unittest.AssertExistsAndLoadBean(t, &actions_model.ActionTask{ID: taskToStopAPIObj.ID})

		// verify CleanupEphemeralRunners does not remove the custom crafted runner
		err = actions_service.CleanupEphemeralRunners(t.Context())
		assert.NoError(t, err)

		runnerToRemove := unittest.AssertExistsAndLoadBean(t, &actions_model.ActionRunner{ID: taskToStop.RunnerID})

		err = actions_model.StopTask(t.Context(), taskToStop.ID, actions_model.StatusFailure)
		assert.NoError(t, err)

		// verify CleanupEphemeralRunners does remove the custom crafted runner
		err = actions_service.CleanupEphemeralRunners(t.Context())
		assert.NoError(t, err)

		unittest.AssertNotExistsBean(t, &actions_model.ActionRunner{ID: runnerToRemove.ID})
	})
}

func createActionsTestRepo(t *testing.T, authToken, repoName string, isPrivate bool) *api.Repository {
	req := NewRequestWithJSON(t, "POST", "/v1/user/repos", &api.CreateRepoOption{
		Name:          repoName,
		Private:       isPrivate,
		Readme:        "Default",
		AutoInit:      true,
		DefaultBranch: "main",
	}).AddTokenAuth(authToken)
	resp := MakeRequest(t, req, http.StatusCreated)
	apiRepo := DecodeJSON(t, resp, &api.Repository{})
	return apiRepo
}

func getWorkflowCreateFileOptions(u *user_model.User, branch, msg, content string) *api.CreateFileOptions {
	return &api.CreateFileOptions{
		FileOptions: api.FileOptions{
			BranchName: branch,
			Message:    msg,
			Author: api.Identity{
				Name:  u.Name,
				Email: u.Email,
			},
			Committer: api.Identity{
				Name:  u.Name,
				Email: u.Email,
			},
			Dates: api.CommitDateOptions{
				Author:    time.Now(),
				Committer: time.Now(),
			},
		},
		ContentBase64: base64.StdEncoding.EncodeToString([]byte(content)),
	}
}

func createWorkflowFile(t *testing.T, authToken, ownerName, repoName, treePath string, opts *api.CreateFileOptions) *api.FileResponse {
	req := NewRequestWithJSON(t, "POST", fmt.Sprintf("/v1/repos/%s/%s/contents/%s", ownerName, repoName, treePath), opts).
		AddTokenAuth(authToken)
	resp := MakeRequest(t, req, http.StatusCreated)
	fileResponse := DecodeJSON(t, resp, &api.FileResponse{})
	return fileResponse
}

// getTaskJobNameByTaskID get the job name of the task by task ID
// there is currently not an API for querying a task by ID so we have to list all the tasks
func getTaskJobNameByTaskID(t *testing.T, authToken, ownerName, repoName string, taskID int64) string {
	// FIXME: we may need to query several pages
	req := NewRequest(t, "GET", fmt.Sprintf("/v1/repos/%s/%s/actions/tasks", ownerName, repoName)).
		AddTokenAuth(authToken)
	resp := MakeRequest(t, req, http.StatusOK)
	taskRespBefore := DecodeJSON(t, resp, &api.ActionTaskResponse{})
	for _, apiTask := range taskRespBefore.Entries {
		if apiTask.ID == taskID {
			return apiTask.Name
		}
	}
	return ""
}

// TestLegacyRunsInCronTasks verifies that the background cron tasks correctly handle runs/jobs
// created before migration v331 (legacy data with LatestAttemptID=0 and jobs with RunAttemptID=0).
func TestLegacyRunsInCronTasks(t *testing.T) {
	onGitRun(t, func(t *testing.T, _ *url.URL) {
		user2 := unittest.AssertExistsAndLoadBean(t, &user_model.User{ID: 2})
		session := loginUser(t, user2.Name)
		token := getTokenForLoggedInUser(t, session, auth_model.AccessTokenScopeWriteRepository, auth_model.AccessTokenScopeWriteUser)

		apiRepo := createActionsTestRepo(t, token, "actions-legacy-cron", false)
		repo := unittest.AssertExistsAndLoadBean(t, &repo_model.Repository{ID: apiRepo.ID})
		httpContext := NewAPITestContext(t, user2.Name, repo.Name, auth_model.AccessTokenScopeWriteRepository)
		defer doAPIDeleteRepository(httpContext)(t)

		// Far-past timestamp so the queries match regardless of the configured timeouts.
		oldTS := timeutil.TimeStamp(time.Now().Add(-30 * 24 * time.Hour).Unix())

		// insertLegacyRunJob inserts a run + job without an ActionRunAttempt record, simulating data created before migration v331 (LatestAttemptID=0, job.RunAttemptID=0, job.AttemptJobID=0).
		insertLegacyRunJob := func(t *testing.T, index int64, runStatus, jobStatus actions_model.Status) (*actions_model.ActionRun, *actions_model.ActionRunJob) {
			t.Helper()

			run := &actions_model.ActionRun{
				Title:         fmt.Sprintf("legacy run %d", index),
				RepoID:        repo.ID,
				OwnerID:       repo.OwnerID,
				WorkflowID:    fmt.Sprintf("legacy-%d.yml", index),
				Index:         index,
				TriggerUserID: user2.ID,
				Ref:           "refs/heads/" + repo.DefaultBranch,
				CommitSHA:     "0000000000000000000000000000000000000000",
				Event:         "workflow_dispatch",
				TriggerEvent:  "workflow_dispatch",
				EventPayload:  "{}",
				Status:        runStatus,
			}
			require.NoError(t, db.Insert(t.Context(), run))

			job := &actions_model.ActionRunJob{
				RunID:        run.ID,
				RepoID:       repo.ID,
				OwnerID:      repo.OwnerID,
				CommitSHA:    run.CommitSHA,
				Name:         "legacy-job",
				Attempt:      1,
				JobID:        "legacy-job",
				RunsOn:       []string{"ubuntu-latest"},
				Status:       jobStatus,
				RunAttemptID: 0,
				AttemptJobID: 0,
			}
			require.NoError(t, db.Insert(t.Context(), job))

			// backfill timestamps so the cron task queries can match them.
			_, err := db.GetEngine(t.Context()).Exec("UPDATE action_run SET created=?, updated=? WHERE id=?", int64(oldTS), int64(oldTS), run.ID)
			require.NoError(t, err)
			_, err = db.GetEngine(t.Context()).Exec("UPDATE action_run_job SET created=?, updated=? WHERE id=?", int64(oldTS), int64(oldTS), job.ID)
			require.NoError(t, err)
			run.Created, run.Updated = oldTS, oldTS
			job.Created, job.Updated = oldTS, oldTS
			return run, job
		}

		t.Run("StopZombieTasks", func(t *testing.T) {
			run, job := insertLegacyRunJob(t, 10, actions_model.StatusRunning, actions_model.StatusRunning)

			task := &actions_model.ActionTask{
				JobID:     job.ID,
				Attempt:   1,
				Status:    actions_model.StatusRunning,
				Started:   oldTS,
				RepoID:    repo.ID,
				OwnerID:   repo.OwnerID,
				CommitSHA: run.CommitSHA,
			}
			task.GenerateAndFillToken()
			require.NoError(t, db.Insert(t.Context(), task))
			_, err := db.GetEngine(t.Context()).Exec("UPDATE action_task SET updated=? WHERE id=?", int64(oldTS), task.ID)
			require.NoError(t, err)
			job.TaskID = task.ID
			_, err = db.GetEngine(t.Context()).ID(job.ID).Cols("task_id").Update(job)
			require.NoError(t, err)

			require.NoError(t, actions_service.StopZombieTasks(t.Context()))

			gotTask := unittest.AssertExistsAndLoadBean(t, &actions_model.ActionTask{ID: task.ID})
			assert.Equal(t, actions_model.StatusFailure, gotTask.Status)
			gotJob := unittest.AssertExistsAndLoadBean(t, &actions_model.ActionRunJob{ID: job.ID})
			assert.Equal(t, actions_model.StatusFailure, gotJob.Status)
			gotRun := unittest.AssertExistsAndLoadBean(t, &actions_model.ActionRun{ID: run.ID})
			assert.Equal(t, actions_model.StatusFailure, gotRun.Status)
		})

		t.Run("StopEndlessTasks", func(t *testing.T) {
			run, job := insertLegacyRunJob(t, 20, actions_model.StatusRunning, actions_model.StatusRunning)

			task := &actions_model.ActionTask{
				JobID:     job.ID,
				Attempt:   1,
				Status:    actions_model.StatusRunning,
				Started:   oldTS,
				RepoID:    repo.ID,
				OwnerID:   repo.OwnerID,
				CommitSHA: run.CommitSHA,
			}
			task.GenerateAndFillToken()
			require.NoError(t, db.Insert(t.Context(), task))
			job.TaskID = task.ID
			_, err := db.GetEngine(t.Context()).ID(job.ID).Cols("task_id").Update(job)
			require.NoError(t, err)

			require.NoError(t, actions_service.StopEndlessTasks(t.Context()))

			gotTask := unittest.AssertExistsAndLoadBean(t, &actions_model.ActionTask{ID: task.ID})
			assert.Equal(t, actions_model.StatusFailure, gotTask.Status)
			gotJob := unittest.AssertExistsAndLoadBean(t, &actions_model.ActionRunJob{ID: job.ID})
			assert.Equal(t, actions_model.StatusFailure, gotJob.Status)
			gotRun := unittest.AssertExistsAndLoadBean(t, &actions_model.ActionRun{ID: run.ID})
			assert.Equal(t, actions_model.StatusFailure, gotRun.Status)
		})

		t.Run("CancelAbandonedJobs", func(t *testing.T) {
			run, job := insertLegacyRunJob(t, 30, actions_model.StatusWaiting, actions_model.StatusWaiting)

			require.NoError(t, actions_service.CancelAbandonedJobs(t.Context()))

			gotJob := unittest.AssertExistsAndLoadBean(t, &actions_model.ActionRunJob{ID: job.ID})
			assert.Equal(t, actions_model.StatusCancelled, gotJob.Status)
			gotRun := unittest.AssertExistsAndLoadBean(t, &actions_model.ActionRun{ID: run.ID})
			assert.Equal(t, actions_model.StatusCancelled, gotRun.Status)
		})

		t.Run("Cleanup", func(t *testing.T) {
			run, _ := insertLegacyRunJob(t, 40, actions_model.StatusSuccess, actions_model.StatusSuccess)

			expiredArtifact := &actions_model.ActionArtifact{
				RunID:                 run.ID,
				RunAttemptID:          0, // legacy artifact
				RepoID:                repo.ID,
				OwnerID:               repo.OwnerID,
				CommitSHA:             run.CommitSHA,
				StoragePath:           fmt.Sprintf("artifacts/legacy-expired-%d.zip", run.ID),
				FileSize:              1,
				FileCompressedSize:    1,
				ContentEncodingOrType: actions_model.ContentTypeZip,
				ArtifactPath:          "legacy-expired.zip",
				ArtifactName:          "legacy-expired",
				Status:                actions_model.ArtifactStatusUploadConfirmed,
				ExpiredUnix:           oldTS,
			}
			require.NoError(t, db.Insert(t.Context(), expiredArtifact))

			require.NoError(t, actions_service.Cleanup(t.Context()))

			gotArtifact := unittest.AssertExistsAndLoadBean(t, &actions_model.ActionArtifact{ID: expiredArtifact.ID})
			assert.Equal(t, actions_model.ArtifactStatusExpired, gotArtifact.Status)
		})
	})
}

// assertTaskContext checks every value a runner is given about its job. The
// context is a struct on the wire, so a field that stops being sent is a compile
// error here rather than a nil a runner reads as an empty string.
func assertTaskContext(t *testing.T, c runner_module.Context, task *actions_model.ActionTask, job *actions_model.ActionRunJob, run *actions_model.ActionRun, pull api.PullRequest) {
	t.Helper()

	runEvent, event := map[string]any{}, map[string]any{}
	require.NoError(t, json.Unmarshal([]byte(run.EventPayload), &runEvent))
	require.NoError(t, json.Unmarshal(c.Event, &event))
	assert.True(t, reflect.DeepEqual(event, runEvent))

	assert.Equal(t, run.TriggerUser.Name, c.Actor)
	assert.Equal(t, setting.AppURL+"v1", c.APIURL)
	assert.Equal(t, pull.Base.Ref, c.BaseRef)
	assert.Equal(t, pull.Head.Ref, c.HeadRef)
	assert.Equal(t, run.TriggerEvent, c.EventName)
	assert.Equal(t, job.JobID, c.Job)
	assert.Equal(t, run.Ref, c.Ref)
	assert.Equal(t, (git.RefName(run.Ref)).ShortName(), c.RefName)
	assert.Equal(t, string((git.RefName(run.Ref)).RefType()), c.RefType)
	assert.Equal(t, run.Repo.OwnerName+"/"+run.Repo.Name, c.Repository)
	assert.Equal(t, run.Repo.OwnerName, c.RepositoryOwner)
	assert.Equal(t, strconv.FormatInt(job.RunID, 10), c.RunID)
	assert.Equal(t, strconv.FormatInt(run.Index, 10), c.RunNumber)
	assert.Equal(t, strconv.FormatInt(job.Attempt, 10), c.RunAttempt)
	assert.Equal(t, setting.AppURL, c.ServerURL)
	assert.Equal(t, run.CommitSHA, c.Sha)
	assert.Equal(t, task.TokenLastEight, c.Token[len(c.Token)-8:])
	assert.NotEmpty(t, c.RuntimeToken)

	// A runner composes an action's clone URL as <ActionsURL>/<owner>/<repo>.
	// Receive nothing here and it composes "https://" + "" + "/actions/checkout",
	// which fails as `http: no Host in request URL` — a job dead before its first
	// step, reading as a broken runner rather than an absent field.
	assert.Equal(t, setting.Actions.DefaultActionsURL.URL(), c.ActionsURL)
}
