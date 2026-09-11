// Copyright 2026 Hanzo AI, Inc. All rights reserved.
// Copyright 2024 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package integration

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"testing"
	"time"

	auth_model "github.com/hanzoai/git/models/auth"
	runner_module "github.com/hanzoai/forge/modules/actions/runner"
	"github.com/hanzoai/git/modules/setting"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// mockRunner speaks the runner protocol the way a real runner does: one POST per
// operation under /v1/runner, carrying JSON in and JSON out, with its uuid and
// token in headers once it has registered.
type mockRunner struct {
	uuid  string
	token string
}

func newMockRunner() *mockRunner {
	return &mockRunner{}
}

// runnerCall invokes one operation and decodes the reply. A non-2xx answer comes
// back as an error carrying the fault body, which is the whole error contract.
func runnerCall[In, Out any](t *testing.T, r *mockRunner, opName string, in *In) (*Out, error) {
	t.Helper()
	body, err := json.Marshal(in)
	require.NoError(t, err)

	req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, setting.AppURL+"v1/runner/"+opName, bytes.NewReader(body))
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/json")
	if r.uuid != "" {
		req.Header.Set("x-runner-uuid", r.uuid)
		req.Header.Set("x-runner-token", r.token)
	}

	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	payload, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%s: %s: %s", opName, resp.Status, bytes.TrimSpace(payload))
	}

	out := new(Out)
	require.NoError(t, json.Unmarshal(payload, out))
	return out, nil
}

func (r *mockRunner) doRegister(t *testing.T, name, token string, labels []string, ephemeral bool) {
	out, err := runnerCall[runner_module.RegisterIn, runner_module.RegisterOut](t, r, "register", &runner_module.RegisterIn{
		Name:      name,
		Token:     token,
		Version:   "mock-runner-version",
		Labels:    labels,
		Ephemeral: ephemeral,
	})
	assert.NoError(t, err)
	if out != nil {
		r.uuid, r.token = out.Runner.UUID, out.Runner.Token
	}
}

func (r *mockRunner) registerAsRepoRunner(t *testing.T, ownerName, repoName, runnerName string, labels []string, ephemeral bool) {
	session := loginUser(t, ownerName)
	token := getTokenForLoggedInUser(t, session, auth_model.AccessTokenScopeWriteRepository)
	req := NewRequest(t, http.MethodPost, fmt.Sprintf("/v1/repos/%s/%s/actions/runners/registration-token", ownerName, repoName)).AddTokenAuth(token)
	resp := MakeRequest(t, req, http.StatusOK)
	registrationToken := DecodeJSON(t, resp, &struct {
		Token string `json:"token"`
	}{})
	r.doRegister(t, runnerName, registrationToken.Token, labels, ephemeral)
}

// task asks for work.
func (r *mockRunner) task(t *testing.T, in *runner_module.TaskIn) (*runner_module.TaskOut, error) {
	return runnerCall[runner_module.TaskIn, runner_module.TaskOut](t, r, "task", in)
}

// state reports task progress.
func (r *mockRunner) state(t *testing.T, in *runner_module.StateIn) (*runner_module.StateOut, error) {
	return runnerCall[runner_module.StateIn, runner_module.StateOut](t, r, "state", in)
}

// appendLog adds console output.
func (r *mockRunner) appendLog(t *testing.T, in *runner_module.LogIn) (*runner_module.LogOut, error) {
	return runnerCall[runner_module.LogIn, runner_module.LogOut](t, r, "log", in)
}

func (r *mockRunner) fetchTask(t *testing.T, timeout ...time.Duration) *runner_module.Task {
	task := r.tryFetchTask(t, timeout...)
	require.NotNil(t, task, "failed to fetch a task")
	return task
}

func (r *mockRunner) fetchNoTask(t *testing.T, timeout ...time.Duration) {
	task := r.tryFetchTask(t, timeout...)
	require.Nil(t, task, "a task is fetched")
}

const defaultFetchTaskTimeout = 1 * time.Second

func (r *mockRunner) tryFetchTask(t *testing.T, timeout ...time.Duration) *runner_module.Task {
	fetchTimeout := defaultFetchTaskTimeout
	if len(timeout) > 0 {
		fetchTimeout = timeout[0]
	}
	ddl := time.Now().Add(fetchTimeout)
	var task *runner_module.Task
	for time.Now().Before(ddl) {
		task, _ = r.fetchTaskOnce(t, 0)
		if task != nil {
			break
		}
		time.Sleep(200 * time.Millisecond)
	}

	return task
}

// fetchTaskOnce asks for work once with the given queue version and returns the
// task, if any, along with the version the forge answered with. This is the
// production path: a runner always sends the version it last saw.
func (r *mockRunner) fetchTaskOnce(t *testing.T, queue int64) (*runner_module.Task, int64) {
	out, err := r.task(t, &runner_module.TaskIn{Queue: queue})
	require.NoError(t, err)
	return out.Task, out.Queue
}

type mockTaskOutcome struct {
	result   runner_module.Result
	outputs  map[string]string
	logLines []runner_module.Line
}

func (r *mockRunner) execTask(t *testing.T, task *runner_module.Task, outcome *mockTaskOutcome) {
	for idx, line := range outcome.logLines {
		out, err := r.appendLog(t, &runner_module.LogIn{
			Task:  task.ID,
			Index: int64(idx),
			Lines: []runner_module.Line{line},
			Last:  idx == len(outcome.logLines)-1,
		})
		assert.NoError(t, err)
		assert.EqualValues(t, idx+1, out.Ack)
	}
	stored := make([]string, 0, len(outcome.outputs))
	for outputKey, outputValue := range outcome.outputs {
		out, err := r.state(t, &runner_module.StateIn{
			State:   runner_module.State{ID: task.ID, Result: runner_module.Pending},
			Outputs: []runner_module.Pair{{Name: outputKey, Value: outputValue}},
		})
		assert.NoError(t, err)
		stored = append(stored, outputKey)
		assert.ElementsMatch(t, stored, out.Stored)
	}
	out, err := r.state(t, &runner_module.StateIn{
		State: runner_module.State{ID: task.ID, Result: outcome.result, Stopped: time.Now().UnixNano()},
	})
	assert.NoError(t, err)
	assert.Equal(t, outcome.result, out.State.Result)
}

// valueOf reads one named value out of the ordered list the wire carries.
func valueOf(pairs []runner_module.Pair, name string) string {
	for _, p := range pairs {
		if p.Name == name {
			return p.Value
		}
	}
	return ""
}

// needOf reads what one job this task depends on produced.
func needOf(needs []runner_module.Need, job string) runner_module.Need {
	for _, need := range needs {
		if need.Job == job {
			return need
		}
	}
	return runner_module.Need{}
}

// needOutput reads one output of one job this task depends on.
func needOutput(needs []runner_module.Need, job, name string) string {
	return valueOf(needOf(needs, job).Outputs, name)
}
