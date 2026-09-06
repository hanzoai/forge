// Copyright 2026 Hanzo AI, Inc. All rights reserved.
// Copyright 2026 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package actions

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	actions_model "github.com/hanzoai/git/models/actions"
	runner_module "github.com/hanzoai/git/modules/actions/runner"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDeclaredAdvertisedCapabilityEnablesCancelling(t *testing.T) {
	r := &actions_model.ActionRunner{}
	cols := declared(r, &runner_module.DeclareIn{
		Version:      "1.2.3",
		Labels:       []string{"linux"},
		Capabilities: []string{cancelling, "other"},
	})

	assert.Equal(t, []string{"agent_labels", "version", "has_cancelling_support"}, cols)
	assert.True(t, r.HasCancellingSupport)
	assert.Equal(t, "1.2.3", r.Version)
	assert.Equal(t, []string{"linux"}, r.AgentLabels)
}

func TestDeclaredMissingCapabilityDisablesCancelling(t *testing.T) {
	r := &actions_model.ActionRunner{HasCancellingSupport: true}
	cols := declared(r, &runner_module.DeclareIn{
		Version: "1.2.3",
		Labels:  []string{"linux"},
	})

	assert.Equal(t, []string{"agent_labels", "version", "has_cancelling_support"}, cols)
	assert.False(t, r.HasCancellingSupport)
}

func TestDeclaredUnchangedCapabilityOmitsColumn(t *testing.T) {
	r := &actions_model.ActionRunner{HasCancellingSupport: true}
	cols := declared(r, &runner_module.DeclareIn{
		Version:      "1.2.3",
		Labels:       []string{"linux"},
		Capabilities: []string{cancelling},
	})

	assert.Equal(t, []string{"agent_labels", "version"}, cols)
	assert.True(t, r.HasCancellingSupport)
}

// The five operations are addressed by name under the mount, and a failure
// arrives as the one fault shape. Register is the operation this can prove
// without a database: it refuses an input missing a name or a token before it
// reads anything.
func TestRunnerRoutesAddressOperationsByName(t *testing.T) {
	routes := RunnerRoutes()

	post := func(path, body string) *httptest.ResponseRecorder {
		t.Helper()
		resp := httptest.NewRecorder()
		routes.ServeHTTP(resp, httptest.NewRequest(http.MethodPost, path, strings.NewReader(body)))
		return resp
	}

	t.Run("an input the operation refuses comes back as a fault", func(t *testing.T) {
		resp := post("/register", `{}`)
		assert.Equal(t, http.StatusBadRequest, resp.Code)
		assert.Equal(t, "application/json", resp.Header().Get("Content-Type"))

		var f fault
		require.NoError(t, json.Unmarshal(resp.Body.Bytes(), &f))
		assert.Equal(t, http.StatusBadRequest, f.Status)
		assert.Equal(t, "missing runner token or name", f.Msg)
	})

	t.Run("a body that is not the operation's input is a fault too", func(t *testing.T) {
		resp := post("/register", `not json`)
		assert.Equal(t, http.StatusBadRequest, resp.Code)
		assert.Contains(t, resp.Body.String(), "decode request")
	})

	t.Run("the other four sit behind the credential check", func(t *testing.T) {
		for _, name := range []string{"declare", "task", "state", "logs"} {
			resp := post("/"+name, `{}`)
			assert.Equal(t, http.StatusUnauthorized, resp.Code, "%s must be routed and refused", name)
			assert.Contains(t, resp.Body.String(), `"code":"unregistered"`)
		}
	})

	t.Run("nothing else is addressable", func(t *testing.T) {
		assert.Equal(t, http.StatusNotFound, post("/runner.v1.RunnerService/Register", `{}`).Code)
		assert.Equal(t, http.StatusNotFound, post("/fetchtask", `{}`).Code)
		assert.Equal(t, http.StatusNotFound, post("/ping", `{}`).Code)
	})
}
