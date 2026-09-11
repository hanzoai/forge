// Copyright 2026 Hanzo AI, Inc. All rights reserved.
// SPDX-License-Identifier: MIT

package actions

import (
	"testing"

	runner_module "github.com/hanzoai/forge/modules/actions/runner"

	"github.com/stretchr/testify/assert"
)

func TestStatusAsResult(t *testing.T) {
	cases := []struct {
		status Status
		want   runner_module.Result
	}{
		{StatusUnknown, runner_module.Pending},
		{StatusWaiting, runner_module.Pending},
		{StatusRunning, runner_module.Pending},
		{StatusBlocked, runner_module.Pending},
		{StatusSuccess, runner_module.Success},
		{StatusFailure, runner_module.Failure},
		{StatusCancelled, runner_module.Cancelled},
		{StatusCancelling, runner_module.Cancelled},
		{StatusSkipped, runner_module.Skipped},
	}

	for _, tt := range cases {
		assert.Equal(t, tt.want, tt.status.AsResult(), "status=%s", tt.status)
	}
}

func TestStatusFromResult(t *testing.T) {
	cases := []struct {
		result runner_module.Result
		want   Status
	}{
		{runner_module.Pending, StatusUnknown},
		{runner_module.Success, StatusSuccess},
		{runner_module.Failure, StatusFailure},
		{runner_module.Cancelled, StatusCancelled},
		{runner_module.Skipped, StatusSkipped},
	}

	for _, tt := range cases {
		assert.Equal(t, tt.want, StatusFromResult(tt.result), "result=%s", tt.result)
	}
}

func newJob(status Status, continueOnError bool) *ActionRunJob {
	return &ActionRunJob{Status: status, ContinueOnError: continueOnError}
}

func TestAggregateJobStatusContinueOnError(t *testing.T) {
	cases := []struct {
		name string
		jobs []*ActionRunJob
		want Status
	}{
		{
			name: "all success",
			jobs: []*ActionRunJob{newJob(StatusSuccess, false), newJob(StatusSuccess, false)},
			want: StatusSuccess,
		},
		{
			name: "one failure without continue-on-error",
			jobs: []*ActionRunJob{newJob(StatusSuccess, false), newJob(StatusFailure, false)},
			want: StatusFailure,
		},
		{
			name: "one failure with continue-on-error",
			jobs: []*ActionRunJob{newJob(StatusSuccess, false), newJob(StatusFailure, true)},
			want: StatusSuccess,
		},
		{
			name: "only continued-failure",
			jobs: []*ActionRunJob{newJob(StatusFailure, true)},
			want: StatusSuccess,
		},
		{
			name: "continued-failure plus real failure",
			jobs: []*ActionRunJob{newJob(StatusFailure, true), newJob(StatusFailure, false)},
			want: StatusFailure,
		},
		{
			name: "all skipped",
			jobs: []*ActionRunJob{newJob(StatusSkipped, false), newJob(StatusSkipped, false)},
			want: StatusSkipped,
		},
		{
			name: "continued-failure plus skipped counts as success",
			jobs: []*ActionRunJob{newJob(StatusFailure, true), newJob(StatusSkipped, false)},
			want: StatusSuccess,
		},
	}

	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, AggregateJobStatus(tt.jobs))
		})
	}
}
