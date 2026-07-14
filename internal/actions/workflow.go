// Copyright 2025 The Hanzo Authors. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

// Package actions is Hanzo Git's server-side CI plane: on a push it scans the
// pushed commit tree for a workflow directory and expands the workflow files
// it finds into runnable jobs for the git-act-runner.
//
// Workflow-directory detection is decomplected into a pure policy — the ordered
// [WorkflowDirs] preference and the [DetectDir] selector — over a small [Tree]
// seam, so the git-object backend (a commit's root tree) and a test fake are
// interchangeable. The GitHub Actions YAML and event semantics are identical
// across every supported directory; only the containing directory differs, so
// nothing downstream (job parsing, event matching, dispatch) needs to know
// which one a repo uses. See docs/PORT_PLAN.md §5.2.
package actions

import "strings"

// NativeDir is Hanzo Git's own workflow home. A repo adopts it to run natively;
// it takes precedence over the compat aliases in [WorkflowDirs].
const NativeDir = ".hanzo/workflows"

// WorkflowDirs is the ordered scan preference. The first directory that exists
// in a commit tree supplies that repo's workflows, so a repo may adopt the
// native home or keep an existing layout unchanged:
//
//  1. .hanzo/workflows  — native, branded home (wins when present)
//  2. .gitea/workflows  — compat alias for repos migrated from a Gitea host
//  3. .github/workflows — compat alias for repos migrated from GitHub
//
// This is the generalized form of the upstream two-dir list (.gitea/workflows
// then .github/workflows); the native .hanzo/workflows is prepended so it wins.
var WorkflowDirs = []string{
	NativeDir,
	".gitea/workflows",
	".github/workflows",
}

// Entry is one item in a workflow directory. IsFile lets [DetectDir] skip
// sub-directories and other non-regular entries when collecting definitions.
type Entry struct {
	Name   string
	IsFile bool
}

// Tree is the git-object seam [DetectDir] reads. A pushed commit's root tree
// implements it in production; tests supply a fake. Dir reports whether path
// exists as a directory in the tree and, if so, returns its immediate entries.
type Tree interface {
	Dir(path string) (entries []Entry, ok bool)
}

// DetectDir returns the workflow directory in effect for a tree — the first of
// [WorkflowDirs] that exists — together with the workflow files it contains
// (regular *.yml/*.yaml entries). ok is false when the repo defines none.
//
// The first existing directory wins even if it holds no workflow files, so a
// repo that adopts .hanzo/workflows is never silently served from a stale
// .github/workflows.
func DetectDir(t Tree) (dir string, files []Entry, ok bool) {
	for _, d := range WorkflowDirs {
		entries, present := t.Dir(d)
		if !present {
			continue
		}
		return d, workflowFiles(entries), true
	}
	return "", nil, false
}

// IsWorkflowFile reports whether a directory entry name is a workflow
// definition (a .yml or .yaml file).
func IsWorkflowFile(name string) bool {
	return strings.HasSuffix(name, ".yml") || strings.HasSuffix(name, ".yaml")
}

func workflowFiles(entries []Entry) []Entry {
	files := make([]Entry, 0, len(entries))
	for _, e := range entries {
		if e.IsFile && IsWorkflowFile(e.Name) {
			files = append(files, e)
		}
	}
	return files
}
