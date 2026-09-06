// Copyright 2026 Hanzo AI, Inc. All rights reserved.
// Copyright 2026 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package actions

import (
	"testing"

	actions_model "github.com/hanzoai/git/models/actions"
	runner_module "github.com/hanzoai/git/modules/actions/runner"

	"github.com/stretchr/testify/assert"
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
