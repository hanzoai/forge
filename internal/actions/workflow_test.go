// Copyright 2025 The Hanzo Authors. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package actions

import (
	"reflect"
	"testing"
)

// fakeTree is an in-memory [Tree]: directories that exist map to their entries.
type fakeTree map[string][]Entry

func (f fakeTree) Dir(path string) ([]Entry, bool) {
	e, ok := f[path]
	return e, ok
}

func yml(names ...string) []Entry {
	es := make([]Entry, len(names))
	for i, n := range names {
		es[i] = Entry{Name: n, IsFile: true}
	}
	return es
}

func TestDetectDir(t *testing.T) {
	ci := yml("ci.yml")
	cases := []struct {
		name    string
		tree    fakeTree
		wantDir string
		wantOK  bool
	}{
		{
			"native wins over both compat aliases",
			fakeTree{".hanzo/workflows": ci, ".gitea/workflows": ci, ".github/workflows": ci},
			".hanzo/workflows", true,
		},
		{
			"native detected on its own",
			fakeTree{".hanzo/workflows": ci},
			".hanzo/workflows", true,
		},
		{
			"gitea compat when native absent",
			fakeTree{".gitea/workflows": ci, ".github/workflows": ci},
			".gitea/workflows", true,
		},
		{
			// The load-bearing compat guarantee: an unchanged GitHub-style repo
			// still runs on Hanzo's runners.
			"github compat still detected on its own",
			fakeTree{".github/workflows": ci},
			".github/workflows", true,
		},
		{
			"no workflow directory present",
			fakeTree{"src": yml("main.go")},
			"", false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir, files, ok := DetectDir(tc.tree)
			if ok != tc.wantOK || dir != tc.wantDir {
				t.Fatalf("DetectDir = (%q, %v), want (%q, %v)", dir, ok, tc.wantDir, tc.wantOK)
			}
			if ok && len(files) != 1 {
				t.Fatalf("want 1 workflow file, got %d: %+v", len(files), files)
			}
		})
	}
}

// A directory that exists but holds no workflow files still wins detection, so
// adopting .hanzo/workflows never falls back to a stale .github/workflows.
func TestDetectDirEmptyNativeWinsOverPopulatedCompat(t *testing.T) {
	tree := fakeTree{
		".hanzo/workflows":  {},
		".github/workflows": yml("ci.yml"),
	}
	dir, files, ok := DetectDir(tree)
	if !ok || dir != ".hanzo/workflows" || len(files) != 0 {
		t.Fatalf("DetectDir = (%q, %v, %d files), want (.hanzo/workflows, true, 0 files)", dir, ok, len(files))
	}
}

func TestDetectDirFiltersToYAMLFiles(t *testing.T) {
	tree := fakeTree{NativeDir: {
		{Name: "ci.yml", IsFile: true},
		{Name: "deploy.yaml", IsFile: true},
		{Name: "README.md", IsFile: true},  // non-workflow file
		{Name: "nested", IsFile: false},    // sub-directory
		{Name: "ci.yml.bak", IsFile: true}, // wrong suffix
	}}
	_, files, ok := DetectDir(tree)
	if !ok {
		t.Fatal("expected native dir detected")
	}
	got := []string{}
	for _, f := range files {
		got = append(got, f.Name)
	}
	want := []string{"ci.yml", "deploy.yaml"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("workflow files = %v, want %v", got, want)
	}
}

func TestWorkflowDirsOrder(t *testing.T) {
	want := []string{".hanzo/workflows", ".gitea/workflows", ".github/workflows"}
	if !reflect.DeepEqual(WorkflowDirs, want) {
		t.Fatalf("WorkflowDirs = %v, want %v", WorkflowDirs, want)
	}
	if WorkflowDirs[0] != NativeDir {
		t.Fatalf("native dir must be first, got %q", WorkflowDirs[0])
	}
}

func TestIsWorkflowFile(t *testing.T) {
	for name, want := range map[string]bool{
		"ci.yml":      true,
		"deploy.yaml": true,
		"README.md":   false,
		"ci.yml.bak":  false,
		"noext":       false,
	} {
		if got := IsWorkflowFile(name); got != want {
			t.Errorf("IsWorkflowFile(%q) = %v, want %v", name, got, want)
		}
	}
}
