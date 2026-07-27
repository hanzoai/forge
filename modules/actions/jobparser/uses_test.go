// Copyright 2026 Hanzo AI, Inc. All rights reserved.
// Copyright 2026 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package jobparser

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseUses(t *testing.T) {
	t.Run("LocalSameRepo", func(t *testing.T) {
		cases := []struct {
			name string
			in   string
			want UsesRef
		}{
			{
				name: "gitea dir, .yml",
				in:   "./.hanzo/workflows/build.yml",
				want: UsesRef{Kind: UsesKindLocalSameRepo, Path: ".hanzo/workflows/build.yml"},
			},
			{
				name: "github dir, .yml",
				in:   "./.github/workflows/build.yml",
				want: UsesRef{Kind: UsesKindLocalSameRepo, Path: ".github/workflows/build.yml"},
			},
			{
				name: "gitea dir, .yaml",
				in:   "./.hanzo/workflows/build.yaml",
				want: UsesRef{Kind: UsesKindLocalSameRepo, Path: ".hanzo/workflows/build.yaml"},
			},
			{
				name: "filename containing dots is allowed",
				in:   "./.hanzo/workflows/foo..bar.yml",
				want: UsesRef{Kind: UsesKindLocalSameRepo, Path: ".hanzo/workflows/foo..bar.yml"},
			},
			{
				name: "nested subdirectory",
				in:   "./.hanzo/workflows/sub/build.yml",
				want: UsesRef{Kind: UsesKindLocalSameRepo, Path: ".hanzo/workflows/sub/build.yml"},
			},
			{
				// ParseUses is dir-agnostic; the allowed directories (WORKFLOW_DIRS / SCOPED_WORKFLOW_DIRS) are enforced by ResolveUses.
				name: "scoped workflows dir parses",
				in:   "./.hanzo/scoped_workflows/lib.yml",
				want: UsesRef{Kind: UsesKindLocalSameRepo, Path: ".hanzo/scoped_workflows/lib.yml"},
			},
			{
				name: "non-default dir parses (allowlist enforced downstream)",
				in:   "./.hanzo/custom_workflows/x.yaml",
				want: UsesRef{Kind: UsesKindLocalSameRepo, Path: ".hanzo/custom_workflows/x.yaml"},
			},
			{
				name: "leading/trailing whitespace is trimmed",
				in:   "  ./.hanzo/workflows/build.yml  ",
				want: UsesRef{Kind: UsesKindLocalSameRepo, Path: ".hanzo/workflows/build.yml"},
			},
		}
		for _, c := range cases {
			t.Run(c.name, func(t *testing.T) {
				got, err := ParseUses(c.in)
				require.NoError(t, err)
				assert.Equal(t, c.want, *got)
			})
		}
	})

	t.Run("LocalCrossRepo", func(t *testing.T) {
		cases := []struct {
			name string
			in   string
			want UsesRef
		}{
			{
				name: "gitea dir, simple ref",
				in:   "owner/repo/.hanzo/workflows/build.yml@v1",
				want: UsesRef{
					Kind:  UsesKindLocalCrossRepo,
					Owner: "owner",
					Repo:  "repo",
					Path:  ".hanzo/workflows/build.yml",
					Ref:   "v1",
				},
			},
			{
				name: "github dir, branch ref",
				in:   "owner/repo/.github/workflows/build.yml@main",
				want: UsesRef{
					Kind:  UsesKindLocalCrossRepo,
					Owner: "owner",
					Repo:  "repo",
					Path:  ".github/workflows/build.yml",
					Ref:   "main",
				},
			},
			{
				name: ".yaml extension",
				in:   "owner/repo/.hanzo/workflows/build.yaml@abc123",
				want: UsesRef{
					Kind:  UsesKindLocalCrossRepo,
					Owner: "owner",
					Repo:  "repo",
					Path:  ".hanzo/workflows/build.yaml",
					Ref:   "abc123",
				},
			},
			{
				name: "ref with slashes (refs/heads/feature)",
				in:   "owner/repo/.hanzo/workflows/build.yml@refs/heads/feature",
				want: UsesRef{
					Kind:  UsesKindLocalCrossRepo,
					Owner: "owner",
					Repo:  "repo",
					Path:  ".hanzo/workflows/build.yml",
					Ref:   "refs/heads/feature",
				},
			},
			{
				name: "nested subdirectory under workflows",
				in:   "owner/repo/.hanzo/workflows/sub/build.yml@v1",
				want: UsesRef{
					Kind:  UsesKindLocalCrossRepo,
					Owner: "owner",
					Repo:  "repo",
					Path:  ".hanzo/workflows/sub/build.yml",
					Ref:   "v1",
				},
			},
			{
				name: "scoped workflows dir parses (allowlist enforced by ResolveUses)",
				in:   "owner/repo/.hanzo/scoped_workflows/lib.yml@v1",
				want: UsesRef{
					Kind:  UsesKindLocalCrossRepo,
					Owner: "owner",
					Repo:  "repo",
					Path:  ".hanzo/scoped_workflows/lib.yml",
					Ref:   "v1",
				},
			},
		}
		for _, c := range cases {
			t.Run(c.name, func(t *testing.T) {
				got, err := ParseUses(c.in)
				require.NoError(t, err)
				assert.Equal(t, c.want, *got)
			})
		}
	})

	t.Run("Errors", func(t *testing.T) {
		cases := []struct {
			name string
			in   string
		}{
			{name: "empty string", in: ""},
			{name: "whitespace only", in: "   "},

			// Same-repo malformed (note: a wrong *directory* parses and should be rejected by the caller)
			{name: "same-repo with @ref", in: "./.hanzo/workflows/build.yml@v1"},
			{name: "same-repo wrong extension", in: "./.hanzo/workflows/build.txt"},
			{name: "same-repo missing extension", in: "./.hanzo/workflows/build"},
			{name: "same-repo absolute path", in: "/.hanzo/workflows/build.yml"},
			{name: "same-repo path traversal", in: "./.hanzo/workflows/../escape.yml"},
			{name: "same-repo double slash", in: "./.hanzo/workflows//build.yml"},
			{name: "same-repo redundant ./", in: "./.hanzo/workflows/./build.yml"},

			// Cross-repo malformed
			{name: "cross-repo missing @ref", in: "owner/repo/.hanzo/workflows/build.yml"},
			{name: "cross-repo empty ref", in: "owner/repo/.hanzo/workflows/build.yml@"},
			{name: "cross-repo missing owner", in: "/repo/.hanzo/workflows/build.yml@v1"},
			{name: "cross-repo missing repo", in: "owner//.hanzo/workflows/build.yml@v1"},
			{name: "cross-repo wrong extension", in: "owner/repo/.hanzo/workflows/build.txt@v1"},
			{name: "cross-repo path traversal", in: "owner/repo/.hanzo/workflows/../escape.yml@v1"},
			{name: "cross-repo double slash in path", in: "owner/repo/.hanzo/workflows//build.yml@v1"},
			// owner/repo with chars Gitea's name validators reject
			{name: "cross-repo owner with space", in: "bad owner/repo/.hanzo/workflows/build.yml@v1"},
			{name: "cross-repo repo with @", in: "owner/re@po/.hanzo/workflows/build.yml@v1"},
		}
		for _, c := range cases {
			t.Run(c.name, func(t *testing.T) {
				_, err := ParseUses(c.in)
				assert.Error(t, err)
			})
		}
	})
}
