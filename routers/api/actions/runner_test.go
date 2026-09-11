// Copyright 2026 Hanzo AI, Inc. All rights reserved.
// Copyright 2026 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package actions

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	actions_model "github.com/hanzoai/git/models/actions"
	runner_module "github.com/hanzoai/forge/modules/actions/runner"
	"github.com/hanzoai/git/modules/web"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/zap-proto/fiber/v3/middleware/adaptor"
	"github.com/zap-proto/zip"
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

// Every value on this protocol must be able to cross the ZAP plane, and the
// derivation says so before anything is served. A map is refused outright; a
// time.Time is worse, because it is accepted, carries nothing and arrives as the
// zero time with no error anywhere. This test is what stops either coming back.
func TestEveryProtocolTypeCrossesThePlane(t *testing.T) {
	types := []any{
		runner_module.Credential{},
		runner_module.Pair{},
		runner_module.Identity{},
		runner_module.RegisterIn{}, runner_module.RegisterOut{},
		runner_module.DeclareIn{}, runner_module.DeclareOut{},
		runner_module.TaskIn{}, runner_module.TaskOut{},
		runner_module.Task{}, runner_module.Need{}, runner_module.Context{},
		runner_module.StateIn{}, runner_module.StateOut{},
		runner_module.State{}, runner_module.Step{},
		runner_module.LogIn{}, runner_module.LogOut{}, runner_module.Line{},
	}
	for _, v := range types {
		rt := reflect.TypeOf(v)
		t.Run(rt.Name(), func(t *testing.T) {
			require.NoError(t, crosses(rt))
		})
	}
}

// crosses reports whether every value reachable from t survives the plane: the
// layout must derive at all, and no struct on it may derive an EMPTY layout. The
// second half is the one that catches a time.Time — every field of it is
// unexported, so it is accepted, occupies a slot, carries nothing and arrives
// zeroed with the reply still a 200.
func crosses(t reflect.Type) error {
	for t.Kind() == reflect.Pointer || t.Kind() == reflect.Slice {
		if t.Kind() == reflect.Slice && t.Elem().Kind() == reflect.Uint8 {
			return nil // bytes, and they carry themselves
		}
		t = t.Elem()
	}
	if t.Kind() != reflect.Struct {
		return nil
	}
	shape, err := zip.LayoutOf(t)
	if err != nil {
		return err
	}
	if len(shape.Slots) == 0 {
		return fmt.Errorf("%s has no slot: it would cross carrying nothing", t)
	}
	for i := range t.NumField() {
		f := t.Field(i)
		if !f.IsExported() {
			continue
		}
		if err := crosses(f.Type); err != nil {
			return fmt.Errorf("%s.%s: %w", t, f.Name, err)
		}
	}
	return nil
}

// A time.Time is the value the rule above exists for: it derives a layout, and
// that layout is empty.
func TestATimeValueWouldCarryNothing(t *testing.T) {
	require.ErrorContains(t, crosses(reflect.TypeOf(struct{ When time.Time }{})),
		"has no slot")
}

// The five operations are addressed by name under the mount, on both of the
// faces zip derives from one declaration: the REST route a runner posts to, and
// the call plane a ZAP caller reaches by operation name. Register is the
// operation this can prove without a database — it refuses an input missing a
// name or a token before it reads anything.
func TestRunnerOpsAddressOperationsByName(t *testing.T) {
	serve := adaptor.FiberApp(RunnerOps().Fiber())

	post := func(path, body string) *httptest.ResponseRecorder {
		t.Helper()
		resp := httptest.NewRecorder()
		serve(resp, httptest.NewRequest(http.MethodPost, path, strings.NewReader(body)))
		return resp
	}

	// The mount does not strip, so an operation is reached at its whole path.
	at := func(name string) string { return RunnerRouteBase + "/" + name }

	t.Run("an input the operation refuses comes back as a refusal", func(t *testing.T) {
		resp := post(at("register"), `{}`)
		assert.Equal(t, http.StatusBadRequest, resp.Code)

		var problem map[string]any
		require.NoError(t, json.Unmarshal(resp.Body.Bytes(), &problem))
		assert.EqualValues(t, http.StatusBadRequest, problem["status"])
		assert.Equal(t, "missing runner token or name", problem["detail"])
	})

	t.Run("a body that is not the operation's input is a refusal too", func(t *testing.T) {
		assert.Equal(t, http.StatusBadRequest, post(at("register"), `not json`).Code)
	})

	t.Run("the other four refuse a caller with no credential", func(t *testing.T) {
		for _, name := range []string{"declare", "task", "state", "log"} {
			resp := post(at(name), `{}`)
			assert.Equal(t, http.StatusUnauthorized, resp.Code, "%s must be routed and refused", name)
			assert.Contains(t, resp.Body.String(), `"code":"unregistered"`)
		}
	})

	t.Run("a credential in the body is not a credential", func(t *testing.T) {
		// Over HTTP there is exactly one way to present a credential and the body
		// is not it, which is what json:"-" on Credential buys.
		resp := post(at("task"), `{"uuid":"u","token":"t","UUID":"u","Token":"t"}`)
		assert.Equal(t, http.StatusUnauthorized, resp.Code)
	})

	t.Run("the call plane addresses the same operations by name", func(t *testing.T) {
		for _, name := range []string{"register", "declare", "task", "state", "log"} {
			op := zip.ID(http.MethodPost, at(name))
			assert.Equal(t, "post_runner_"+name, op)
			// A ZAP frame is expected here, so JSON is refused — which is the proof
			// the plane is mounted and speaking ZAP rather than answering 404.
			resp := post(zip.CallPath+op, `{}`)
			assert.NotEqual(t, http.StatusNotFound, resp.Code, "%s must be reachable on the call plane", op)
		}
	})

	t.Run("nothing else is addressable", func(t *testing.T) {
		assert.Equal(t, http.StatusNotFound, post(at("runner.v1.RunnerService/Register"), `{}`).Code)
		assert.Equal(t, http.StatusNotFound, post(at("fetchtask"), `{}`).Code)
		assert.Equal(t, http.StatusNotFound, post(at("ping"), `{}`).Code)
		assert.Equal(t, http.StatusNotFound, post(zip.CallPath+"nope", `{}`).Code)
	})
}

// The mount is load-bearing and easy to get wrong: chi's Mount strips the prefix
// it matched, and an app whose operations declare absolute paths then answers
// nothing. Post reaches chi's Method, which leaves URL.Path alone. This is that
// difference, measured on the forge's own router rather than assumed.
func TestTheForgeRouterDeliversTheWholePath(t *testing.T) {
	serve := adaptor.FiberApp(RunnerOps().Fiber())
	r := web.NewRouter()
	r.Post(RunnerRouteBase+"/*", serve)
	r.Post(zip.CallPath+"*", serve)

	post := func(path string) *httptest.ResponseRecorder {
		resp := httptest.NewRecorder()
		r.ServeHTTP(resp, httptest.NewRequest(http.MethodPost, path, strings.NewReader(`{}`)))
		return resp
	}

	// Reached, and refused on its own terms — which only happens if the operation
	// matched, and it only matches on the whole path.
	assert.Equal(t, http.StatusBadRequest, post(RunnerRouteBase+"/register").Code)
	assert.Equal(t, http.StatusUnauthorized, post(RunnerRouteBase+"/task").Code)
	assert.NotEqual(t, http.StatusNotFound, post(zip.CallPath+"post_runner_task").Code)
}

// The credential is declared, not read off to the side: an embedded Credential
// puts x-runner-uuid and x-runner-token in the contract of the four operations
// that need one, and leaves register — the operation a runner reaches before it
// has a credential at all — carrying none.
func TestTheCredentialIsDeclaredOnTheFourThatNeedIt(t *testing.T) {
	paths, _ := RunnerOps().OpenAPISpec()["paths"].(map[string]map[string]any)
	require.NotEmpty(t, paths)

	headers := func(name string) []string {
		path := paths[RunnerRouteBase+"/"+name]
		require.NotNilf(t, path, "%s is not in the document", name)
		op, _ := path["post"].(map[string]any)
		require.NotNil(t, op)
		params, _ := op["parameters"].([]any)
		var got []string
		for _, p := range params {
			if p, _ := p.(map[string]any); p["in"] == "header" {
				got = append(got, p["name"].(string))
			}
		}
		return got
	}

	for _, name := range []string{"declare", "task", "state", "log"} {
		assert.ElementsMatch(t, []string{"x-runner-uuid", "x-runner-token"}, headers(name), "%s", name)
	}
	assert.Empty(t, headers("register"), "register answers a runner that has no credential yet")
}
