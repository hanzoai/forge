// Copyright 2026 Hanzo AI, Inc. All rights reserved.
// Copyright 2026 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package jobparser

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.yaml.in/yaml/v4"
)

func TestParse(t *testing.T) {
	tests := []struct {
		name    string
		options []ParseOption
		wantErr bool
	}{
		{
			name:    "multiple_jobs",
			options: nil,
			wantErr: false,
		},
		{
			name:    "multiple_matrix",
			options: nil,
			wantErr: false,
		},
		{
			name:    "has_needs",
			options: nil,
			wantErr: false,
		},
		{
			name:    "has_with",
			options: nil,
			wantErr: false,
		},
		{
			name:    "has_secrets",
			options: nil,
			wantErr: false,
		},
		{
			name:    "empty_step",
			options: nil,
			wantErr: false,
		},
		{
			name:    "job_name_with_matrix",
			options: nil,
			wantErr: false,
		},
		{
			name:    "prefixed_newline",
			options: nil,
			wantErr: false,
		},
		{
			name:    "continue_on_error_expr",
			options: nil,
			wantErr: false,
		},
	}
	invalidFileTests := []struct {
		name string
	}{
		{name: "null_job_implicit"},
		{name: "null_job_explicit"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			content := ReadTestdata(t, tt.name+".in.yaml")
			want := ReadTestdata(t, tt.name+".out.yaml")
			got, err := Parse(content, tt.options...)
			if tt.wantErr {
				require.Error(t, err)
			}
			require.NoError(t, err)

			builder := &strings.Builder{}
			for _, v := range got {
				if builder.Len() > 0 {
					builder.WriteString("---\n")
				}
				encoder := yaml.NewEncoder(builder)
				encoder.SetIndent(2)
				require.NoError(t, encoder.Encode(v))
				id, job := v.Job()
				assert.NotEmpty(t, id)
				assert.NotNil(t, job)
			}
			assert.Equal(t, string(want), builder.String())
		})
	}

	for _, tt := range invalidFileTests {
		t.Run(tt.name, func(t *testing.T) {
			content := ReadTestdata(t, tt.name+".in.yaml")
			require.NotPanics(t, func() {
				_, err := Parse(content)
				require.Error(t, err)
			})
		})
	}
}

// TestParseRunsOnExpression pins that `runs-on` is evaluated STRUCTURALLY.
//
// A reusable workflow takes its pool from an input as
// `runs-on: ${{ fromJson(inputs.runner) }}`. That used to be flattened to
// []string and pushed through Interpolate, which forces a string result, so an
// array collapsed to the literal label "Array". No runner carries that label,
// so the job queued forever and nothing logged an error — the failure mode that
// kept hanzoai/commerce and hanzoai/cloud from ever producing a build after
// their CI moved onto this forge.
func TestParseRunsOnExpression(t *testing.T) {
	for _, tc := range []struct {
		name   string
		inputs map[string]any
		yaml   string
		want   []string
	}{
		{
			name:   "array-valued expression expands to labels",
			inputs: map[string]any{"runner": `["hanzo-build-linux-amd64"]`},
			yaml: `
on: push
jobs:
  build:
    runs-on: ${{ fromJson(inputs.runner) }}
    steps:
      - run: echo hi
`,
			want: []string{"hanzo-build-linux-amd64"},
		},
		{
			name:   "multi-label array keeps every label",
			inputs: map[string]any{"runner": `["self-hosted","linux","amd64"]`},
			yaml: `
on: push
jobs:
  build:
    runs-on: ${{ fromJson(inputs.runner) }}
    steps:
      - run: echo hi
`,
			want: []string{"self-hosted", "linux", "amd64"},
		},
		{
			name:   "string-valued expression still yields one label",
			inputs: map[string]any{"pool": "ubuntu-22.04"},
			yaml: `
on: push
jobs:
  build:
    runs-on: ${{ inputs.pool }}
    steps:
      - run: echo hi
`,
			want: []string{"ubuntu-22.04"},
		},
		{
			name: "a plain literal is untouched",
			yaml: `
on: push
jobs:
  build:
    runs-on: ubuntu-22.04
    steps:
      - run: echo hi
`,
			want: []string{"ubuntu-22.04"},
		},
		{
			name: "a plain list is untouched",
			yaml: `
on: push
jobs:
  build:
    runs-on: [self-hosted, linux]
    steps:
      - run: echo hi
`,
			want: []string{"self-hosted", "linux"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := Parse([]byte(tc.yaml), WithInputs(tc.inputs))
			require.NoError(t, err)
			require.Len(t, got, 1)
			_, job := got[0].Job()
			assert.Equal(t, tc.want, job.RunsOn(),
				"runs-on must evaluate structurally; %q means the array was stringified", "Array")
		})
	}
}
