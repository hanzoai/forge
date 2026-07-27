// Copyright 2026 The Hanzo Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package git

import (
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// A push mirror's remote is created with --mirror=push, which persists
// remote.<name>.mirror=true. Git then applies mirror mode from that config
// alone and refuses any refspec, so a push that carries one dies with
// "--mirror can't be combined with refspecs". Every push mirror was silently
// dead across releases because nothing exercised this path.
//
// The push must also stay additive: mirror mode brings prune and force with it,
// and a mirror that can delete or rewrite refs on the far side is how a repo
// loses history it is only supposed to be copying.
func TestPushRefspecsOverrideMirrorConfig(t *testing.T) {
	run := func(dir string, args ...string) string {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = append(cmd.Environ(),
			"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@e",
			"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@e")
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
		}
		return string(out)
	}

	root := t.TempDir()
	src, dst := filepath.Join(root, "src"), filepath.Join(root, "dst")

	run(root, "init", "-q", "--bare", dst)
	run(root, "init", "-q", src)
	run(src, "commit", "-q", "--allow-empty", "-m", "one")
	run(src, "remote", "add", "--mirror=push", "up", dst)

	if got := strings.TrimSpace(run(src, "config", "--get", "remote.up.mirror")); got != "true" {
		t.Fatalf("remote.up.mirror = %q, want true — the premise of this test", got)
	}

	// Seed the far side, then give it a ref the source does not have. Mirror
	// mode would prune that ref; an additive push must leave it alone.
	if err := Push(t.Context(), src, PushOptions{
		Remote:   "up",
		Refspecs: []string{"refs/heads/*:refs/heads/*"},
	}); err != nil {
		t.Fatalf("seed push rejected — the mirror config was not overridden: %v", err)
	}
	run(dst, "update-ref", "refs/heads/only-there", strings.TrimSpace(run(dst, "rev-parse", "HEAD")))

	if err := Push(t.Context(), src, PushOptions{
		Remote:   "up",
		Refspecs: []string{"refs/heads/*:refs/heads/*", "refs/tags/*:refs/tags/*"},
	}); err != nil {
		t.Fatalf("refspec push rejected — the mirror config was not overridden: %v", err)
	}

	if !strings.Contains(run(dst, "show-ref"), "refs/heads/only-there") {
		t.Error("a ref only the remote had was pruned; the push is not additive")
	}
	if got := strings.TrimSpace(run(src, "config", "--get", "remote.up.mirror")); got != "true" {
		t.Errorf("stored remote.up.mirror = %q, want it left as the mirror machinery wrote it", got)
	}

	// Diverge the far side. An additive push reports the rejection instead of
	// overwriting, even though remote.up.push carries a force '+'.
	run(dst, "update-ref", "refs/heads/master", strings.TrimSpace(run(dst, "rev-parse", "refs/heads/only-there")))
	before := run(dst, "rev-parse", "refs/heads/master")
	run(src, "commit", "-q", "--allow-empty", "-m", "two")
	if err := Push(t.Context(), src, PushOptions{
		Remote:   "up",
		Refspecs: []string{"refs/heads/master:refs/heads/master"},
	}); err == nil {
		t.Error("a diverged ref was accepted; the push is forcing")
	}
	if run(dst, "rev-parse", "refs/heads/master") != before {
		t.Error("a diverged ref was overwritten")
	}
}
