// Copyright 2026 Hanzo AI, Inc. All rights reserved.
// SPDX-License-Identifier: MIT

package mirror

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/hanzoai/git/models/db"
	git_model "github.com/hanzoai/git/models/git"
	access_model "github.com/hanzoai/git/models/perm/access"
	repo_model "github.com/hanzoai/git/models/repo"
	system_model "github.com/hanzoai/git/models/system"
	"github.com/hanzoai/git/models/unit"
	"github.com/hanzoai/git/models/unittest"
	user_model "github.com/hanzoai/git/models/user"
	"github.com/hanzoai/git/modules/cache"
	"github.com/hanzoai/git/modules/git"
	"github.com/hanzoai/git/modules/gitrepo"
	"github.com/hanzoai/git/modules/globallock"
	repo_module "github.com/hanzoai/git/modules/repository"
	"github.com/hanzoai/git/modules/setting"
	"github.com/hanzoai/git/modules/test"
	"github.com/hanzoai/git/modules/timeutil"
	notify_service "github.com/hanzoai/git/services/notify"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// heard records what the notifiers are told by the Sync* calls, which are how a
// pull mirror announces a fetch. A push through the hooks never uses them.
type heard struct {
	notify_service.NullNotifier
	synced []string
}

var heardNotifier = &heard{}

func init() { notify_service.RegisterNotifier(heardNotifier) }

func (h *heard) SyncPushCommits(_ context.Context, _ *user_model.User, _ *repo_model.Repository, opts *repo_module.PushUpdateOptions, _ *repo_module.PushCommits) {
	h.synced = append(h.synced, opts.RefFullName.String())
}

func (h *heard) SyncCreateRef(_ context.Context, _ *user_model.User, _ *repo_model.Repository, refFullName git.RefName, _ string) {
	h.synced = append(h.synced, refFullName.String())
}

// site is a bare repository driven by plumbing alone, so a test can build any
// history in it, force-move any ref, and read back where a ref stands.
type site struct {
	t    *testing.T
	path string
}

func newSite(t *testing.T, path string) *site {
	t.Helper()
	require.NoError(t, os.RemoveAll(path))
	require.NoError(t, git.InitRepository(t.Context(), path, true, git.Sha1ObjectFormat.Name()))
	return &site{t: t, path: path}
}

func (s *site) git(args ...string) string {
	s.t.Helper()
	cmd := exec.CommandContext(s.t.Context(), "git", args...)
	cmd.Dir = s.path
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.com",
		"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.com")
	out, err := cmd.CombinedOutput()
	require.NoError(s.t, err, string(out))
	return strings.TrimSpace(string(out))
}

// commit writes an empty commit with the given parent ("" for a root) and returns its ID.
func (s *site) commit(parent, message string) string {
	tree := s.git("hash-object", "-t", "tree", "-w", "--stdin")
	if parent == "" {
		return s.git("commit-tree", tree, "-m", message)
	}
	return s.git("commit-tree", tree, "-p", parent, "-m", message)
}

func (s *site) set(ref, id string) { s.git("update-ref", ref, id) }

func (s *site) delete(ref string) { s.git("update-ref", "-d", ref) }

// tip returns where ref stands, or "" if it does not exist.
func (s *site) tip(ref string) string { return s.git("for-each-ref", "--format=%(objectname)", ref) }

// rig is a forge repository with a push mirror to a stand-in for GitHub.
//
// The hook scripts are the ones the forge installs. They run a stand-in for the
// daemon, which records every call with the environment of the push and can be
// told to refuse a ref in pre-receive. What the real pre-receive refuses is the
// daemon's own business; what is pinned here is that the sync reaches it, as the
// sync user, ref by ref, and treats a refusal as one.
type rig struct {
	t      *testing.T
	repo   *repo_model.Repository
	mirror *repo_model.PushMirror
	forge  *site
	github *site
	dir    string
}

func newRig(t *testing.T) *rig {
	t.Helper()
	unittest.PrepareTestEnv(t)
	// With the queue off, a push to the mirror is dropped rather than sent to a queue this test never starts.
	t.Cleanup(test.MockVariableValue(&setting.Mirror.Enabled, false))
	t.Cleanup(test.MockVariableValue(&setting.Mirror.SyncUser, "user1"))
	*heardNotifier = heard{}
	require.NoError(t, cache.GetCache().ChiCache().Flush()) // the notices already given belong to other tests

	// org3/repo21: public, empty, with a code unit.
	repo := unittest.AssertExistsAndLoadBean(t, &repo_model.Repository{ID: 32})
	r := &rig{
		t:      t,
		repo:   repo,
		forge:  newSite(t, repo_model.RepoPath(repo.OwnerName, repo.Name)),
		github: newSite(t, filepath.Join(t.TempDir(), "github.git")),
		dir:    t.TempDir(),
	}

	daemon := filepath.Join(r.dir, "gitd")
	require.NoError(t, os.WriteFile(daemon, []byte(`#!/usr/bin/env bash
dir=`+r.dir+`
hook=$3
# Like the real hook binary, it does not look at an internal push.
[ "${GIT_INTERNAL_PUSH:-}" = true ] && exit 0
echo "hook=$hook pusher=$GIT_PUSHER_NAME id=$GIT_PUSHER_ID repo=$GIT_REPO_USER_NAME/$GIT_REPO_NAME internal=${GIT_INTERNAL_PUSH:-}" >> "$dir/hooks.log"
echo "call $hook" >> "$dir/hooks.log"
case $hook in
update) echo "update $4" >> "$dir/hooks.log" ;;
pre-receive|post-receive)
  # What the push brought: pre-receive runs with its objects in a quarantine, apart from the repository's own.
  if [ "$hook" = pre-receive ]; then
    echo "received $(find "${GIT_QUARANTINE_PATH:-/nonexistent}" -type f \( -name '*.pack' -o -path '*/[0-9a-f][0-9a-f]/*' \) 2>/dev/null | wc -l | tr -d ' ')" >> "$dir/hooks.log"
  fi
  while read -r old new ref; do
    echo "$hook $ref" >> "$dir/hooks.log"
    if [ "$hook" = pre-receive ] && grep -qxF "$ref" "$dir/refuse" 2>/dev/null; then
      echo "protected branch: unverified commit $new" >&2
      exit 1
    fi
  done
  if [ "$hook" = pre-receive ] && [ -e "$dir/delay" ]; then
    touch "$dir/entered"
    sleep "$(cat "$dir/delay")"
    touch "$dir/finished"
  fi ;;
esac
`), 0o755))
	t.Cleanup(test.MockVariableValue(&setting.AppPath, daemon))
	t.Cleanup(test.MockVariableValue(&setting.CustomConf, filepath.Join(r.dir, "app.ini")))
	require.NoError(t, gitrepo.CreateDelegateHooks(t.Context(), repo))

	require.NoError(t, gitrepo.GitRemoteAdd(t.Context(), repo, "upstream", r.github.path, gitrepo.RemoteOptionMirrorPush))
	r.mirror = &repo_model.PushMirror{RepoID: repo.ID, RemoteName: "upstream", RemoteAddress: "https://github.com/org3/repo21", SyncOnCommit: true}
	require.NoError(t, db.Insert(t.Context(), r.mirror))
	return r
}

func (r *rig) sync() bool {
	r.t.Helper()
	return SyncInboundMirror(r.t.Context(), r.mirror.ID)
}

// calls returns the refs the given hook was run for, in order.
func (r *rig) calls(hook string) []string {
	r.t.Helper()
	out, err := os.ReadFile(filepath.Join(r.dir, "hooks.log"))
	if os.IsNotExist(err) {
		return nil
	}
	require.NoError(r.t, err)
	var refs []string
	for line := range strings.SplitSeq(string(out), "\n") {
		if ref, ok := strings.CutPrefix(line, hook+" "); ok {
			refs = append(refs, ref)
		}
	}
	return refs
}

// received returns, for each push, how many objects files it carried to the repository.
func (r *rig) received() []string {
	r.t.Helper()
	out, err := os.ReadFile(filepath.Join(r.dir, "hooks.log"))
	if os.IsNotExist(err) {
		return nil
	}
	require.NoError(r.t, err)
	var counts []string
	for line := range strings.SplitSeq(string(out), "\n") {
		if count, ok := strings.CutPrefix(line, "received "); ok {
			counts = append(counts, count)
		}
	}
	return counts
}

// invocations returns how many times the given hook was run: one per push.
func (r *rig) invocations(hook string) int {
	r.t.Helper()
	out, err := os.ReadFile(filepath.Join(r.dir, "hooks.log"))
	if os.IsNotExist(err) {
		return 0
	}
	require.NoError(r.t, err)
	return strings.Count("\n"+string(out), "\ncall "+hook+"\n")
}

// forget clears the record of hook calls.
func (r *rig) forget() {
	r.t.Helper()
	require.NoError(r.t, os.RemoveAll(filepath.Join(r.dir, "hooks.log")))
}

// refuse makes pre-receive decline these refs, as it does for a protected branch.
func (r *rig) refuse(refs ...string) {
	r.t.Helper()
	require.NoError(r.t, os.WriteFile(filepath.Join(r.dir, "refuse"), []byte(strings.Join(refs, "\n")+"\n"), 0o644))
}

// notices returns the notices that name the repository, leaving out the fixtures.
func (r *rig) notices() []string {
	r.t.Helper()
	var notices []*system_model.Notice
	require.NoError(r.t, db.GetEngine(r.t.Context()).Find(&notices))
	var descs []string
	for _, n := range notices {
		if strings.Contains(n.Description, r.repo.FullName()) {
			descs = append(descs, n.Description)
		}
	}
	return descs
}

func TestInboundCreatesAbsentRefsThroughThePushPath(t *testing.T) {
	r := newRig(t)
	c1 := r.github.commit("", "one")
	c2 := r.github.commit(c1, "two")
	r.github.set("refs/heads/main", c1)
	require.True(t, r.sync()) // fills the empty repository
	r.forget()

	r.github.set("refs/heads/main", c2)
	r.github.set("refs/heads/topic", c1)
	r.github.git("tag", "light", c1)
	r.github.git("tag", "-a", "-m", "annotated", "v1", c2)
	require.True(t, r.sync())

	assert.Equal(t, c2, r.forge.tip("refs/heads/main"))
	assert.Equal(t, c1, r.forge.tip("refs/heads/topic"))
	assert.Equal(t, c1, r.forge.tip("refs/tags/light"))
	assert.Equal(t, r.github.tip("refs/tags/v1"), r.forge.tip("refs/tags/v1"), "an annotated tag keeps its tag object")
	assert.Empty(t, r.notices())

	// Each branch went through pre-receive and post-receive, which is where the
	// daemon enforces protection and starts Actions, pull request checks and the
	// push out, one push to a branch. The two tags are new together, so they came in one push without the hooks.
	want := []string{"refs/heads/main", "refs/heads/topic"}
	assert.ElementsMatch(t, want, r.calls("pre-receive"))
	assert.ElementsMatch(t, want, r.calls("post-receive"))
	assert.ElementsMatch(t, want, r.calls("update"))
	assert.Equal(t, 2, r.invocations("pre-receive"), "one push per branch: a refusal of one must not hold up the others")
	unittest.AssertExistsAndLoadBean(t, &repo_model.Release{RepoID: r.repo.ID, TagName: "v1"})

	// As the sync user, with the environment of a person's push: not an internal one that skips the hooks.
	out, err := os.ReadFile(filepath.Join(r.dir, "hooks.log"))
	require.NoError(t, err)
	assert.Contains(t, string(out), "pusher=user1 id=1 repo=org3/repo21 internal=\n")

	assert.Empty(t, heardNotifier.synced, "the Sync* calls are a pull mirror's; they do not start Actions")

	// Recorded only now that the forge has them.
	assert.Equal(t, c2, r.forge.tip("refs/upstream/heads/main"))
	assert.Equal(t, c1, r.forge.tip("refs/upstream/heads/topic"))
}

// An empty repository is filled with GitHub's default branch first, which becomes
// its default, then the other branches in one push, then the tags in one push
// that raises no push events: a release workflow would run for every old tag.
func TestInboundSeedsAnEmptyRepository(t *testing.T) {
	r := newRig(t)
	c1 := r.github.commit("", "one")
	c2 := r.github.commit(c1, "two")
	r.github.git("symbolic-ref", "HEAD", "refs/heads/trunk")
	r.github.set("refs/heads/trunk", c2)
	r.github.set("refs/heads/dev", c1)
	r.github.set("refs/heads/main", c1)
	for _, tag := range []string{"v1", "v2", "v3", "v4", "v5"} {
		r.github.git("tag", tag, c1)
	}
	r.github.git("tag", "-a", "-m", "annotated", "v6", c2)

	require.True(t, r.sync())

	assert.Equal(t, c2, r.forge.tip("refs/heads/trunk"))
	assert.Equal(t, c1, r.forge.tip("refs/heads/dev"))
	assert.Equal(t, c1, r.forge.tip("refs/heads/main"))
	assert.Equal(t, r.github.tip("refs/tags/v6"), r.forge.tip("refs/tags/v6"))
	assert.Equal(t, "refs/heads/trunk", r.forge.git("symbolic-ref", "HEAD"), "GitHub's default branch becomes the default")
	repo := unittest.AssertExistsAndLoadBean(t, &repo_model.Repository{ID: r.repo.ID})
	assert.Equal(t, "trunk", repo.DefaultBranch)

	// The default branch alone, then the rest in one push; the tags raise no hooks at all.
	assert.Equal(t, 2, r.invocations("pre-receive"))
	assert.Equal(t, []string{"refs/heads/trunk", "refs/heads/dev", "refs/heads/main"}, r.calls("pre-receive"))
	assert.NotContains(t, strings.Join(r.calls("post-receive"), " "), "refs/tags/")

	// But the releases exist, as a pull mirror makes them.
	for _, tag := range []string{"v1", "v2", "v3", "v4", "v5", "v6"} {
		unittest.AssertExistsAndLoadBean(t, &repo_model.Release{RepoID: r.repo.ID, TagName: tag})
	}
	assert.Equal(t, c2, r.forge.tip("refs/upstream/heads/trunk"))
	assert.Equal(t, c1, r.forge.tip("refs/upstream/tags/v1"))
	assert.Empty(t, r.notices())

	// Nothing is left to do.
	r.forget()
	require.True(t, r.sync())
	assert.Zero(t, r.invocations("pre-receive"))
}

func TestInboundFastForwardsOnlyWhatMoved(t *testing.T) {
	r := newRig(t)
	c1 := r.github.commit("", "one")
	r.github.set("refs/heads/main", c1)
	r.github.set("refs/heads/topic", c1)
	require.True(t, r.sync())
	r.forget()

	c2 := r.github.commit(c1, "two")
	r.github.set("refs/heads/main", c2)
	require.True(t, r.sync())

	assert.Equal(t, c2, r.forge.tip("refs/heads/main"))
	assert.Equal(t, c1, r.forge.tip("refs/heads/topic"))
	assert.Equal(t, []string{"refs/heads/main"}, r.calls("pre-receive"), "an unchanged branch is not pushed again")
	assert.Equal(t, []string{"refs/heads/main"}, r.calls("post-receive"))
	assert.Empty(t, r.notices())
}

func TestInboundEqualIsNoop(t *testing.T) {
	r := newRig(t)
	c1 := r.github.commit("", "one")
	r.github.set("refs/heads/main", c1)
	r.github.git("tag", "v1", c1)
	require.True(t, r.sync())
	r.forget()

	require.True(t, r.sync())
	assert.Empty(t, r.calls("pre-receive"))
	assert.Equal(t, c1, r.forge.tip("refs/heads/main"))
	assert.Empty(t, r.notices())
}

func TestInboundDivergedIsLeftAndReportedOnce(t *testing.T) {
	r := newRig(t)
	c1 := r.github.commit("", "one")
	r.github.set("refs/heads/main", c1)
	require.True(t, r.sync())
	r.forget()

	// Each side takes a commit the other lacks.
	mine := r.forge.commit(c1, "forge only")
	r.forge.set("refs/heads/main", mine)
	theirs := r.github.commit(c1, "github only")
	r.github.set("refs/heads/main", theirs)

	for range 5 {
		require.True(t, r.sync())
	}

	assert.Equal(t, mine, r.forge.tip("refs/heads/main"), "a diverged ref is never overwritten")
	assert.Empty(t, r.calls("pre-receive"), "and never pushed")
	require.Len(t, r.notices(), 1, "one notice per diverged ref, not one per event")
	assert.Contains(t, r.notices()[0], "refs/heads/main")
	assert.Contains(t, r.notices()[0], mine[:12], "the first report says what diverged")
	assert.Contains(t, r.notices()[0], theirs[:12])

	// Upstream moving on is the same condition: the ref is still diverged.
	r.github.set("refs/heads/main", r.github.commit(theirs, "github again"))
	require.True(t, r.sync())
	require.Len(t, r.notices(), 1)

	// A different ref, or a different reason, is a different one.
	r.github.set("refs/heads/other", r.github.commit(c1, "other"))
	r.forge.set("refs/heads/other", r.forge.commit(c1, "other, here"))
	require.True(t, r.sync())
	require.Len(t, r.notices(), 2)
}

func TestInboundKeepsRefAheadOfUpstream(t *testing.T) {
	r := newRig(t)
	c1 := r.github.commit("", "one")
	r.github.set("refs/heads/main", c1)
	require.True(t, r.sync())
	r.forget()

	// A push to the forge the push mirror has not carried out yet, while GitHub gained a branch.
	c2 := r.forge.commit(c1, "forge only")
	r.forge.set("refs/heads/main", c2)
	r.github.set("refs/heads/main", c1)
	r.github.set("refs/heads/other", c1)
	require.True(t, r.sync())

	assert.Equal(t, c2, r.forge.tip("refs/heads/main"))
	assert.Equal(t, []string{"refs/heads/other"}, r.calls("pre-receive"))
	assert.Empty(t, r.notices(), "an upstream that has not caught up is not a divergence")
}

func TestInboundKeepsRefUpstreamLacks(t *testing.T) {
	r := newRig(t)
	c1 := r.github.commit("", "one")
	r.github.set("refs/heads/main", c1)
	r.github.set("refs/heads/gone", c1)
	r.github.git("tag", "v1", c1)
	require.True(t, r.sync())
	r.forget()

	r.github.delete("refs/heads/gone")
	r.github.delete("refs/tags/v1")
	require.True(t, r.sync())

	assert.Equal(t, c1, r.forge.tip("refs/heads/gone"), "a ref the upstream dropped is not dropped here")
	assert.Equal(t, c1, r.forge.tip("refs/tags/v1"))
	assert.Empty(t, r.forge.tip("refs/upstream/heads/gone"), "the private namespace follows the upstream")
	assert.Empty(t, r.calls("pre-receive"))
	assert.Empty(t, r.notices())
}

func TestInboundTagsAreCreateOnly(t *testing.T) {
	r := newRig(t)
	c1 := r.github.commit("", "one")
	c2 := r.github.commit(c1, "two")
	r.github.set("refs/heads/main", c2)
	r.github.git("tag", "v1.0.0", c1)
	require.True(t, r.sync())
	r.forget()

	// Upstream moves a release tag forward, which a fast-forward rule would allow.
	r.github.git("tag", "-f", "v1.0.0", c2)
	// And replaces a signed-style annotated forge tag with a lightweight one on the same commit.
	r.forge.git("tag", "-a", "-m", "release notes", "v2", c1)
	annotated := r.forge.tip("refs/tags/v2")
	r.github.git("tag", "v2", c1)
	// A new tag still arrives.
	r.github.git("tag", "v3", c2)
	require.True(t, r.sync())

	assert.Equal(t, c1, r.forge.tip("refs/tags/v1.0.0"), "a tag is not re-pointed")
	assert.Equal(t, annotated, r.forge.tip("refs/tags/v2"), "an annotated tag is not retyped")
	assert.Equal(t, c2, r.forge.tip("refs/tags/v3"))
	assert.Equal(t, []string{"refs/tags/v3"}, r.calls("pre-receive"))
	require.Len(t, r.notices(), 2)
	assert.Contains(t, strings.Join(r.notices(), "\n"), "refs/tags/v1.0.0")
	assert.Contains(t, strings.Join(r.notices(), "\n"), "refs/tags/v2")

	// The same differences on the next event are not reported again.
	require.True(t, r.sync())
	assert.Len(t, r.notices(), 2)
}

func TestInboundRefusedRefIsReportedAndRetried(t *testing.T) {
	r := newRig(t)
	c1 := r.github.commit("", "one")
	r.github.set("refs/heads/main", c1)
	require.True(t, r.sync())
	r.forget()

	// A protected branch: pre-receive declines the sync user's push.
	r.refuse("refs/heads/main")
	c2 := r.github.commit(c1, "unreviewed")
	r.github.set("refs/heads/main", c2)
	r.github.set("refs/heads/free", c1)
	for range 3 {
		require.True(t, r.sync())
	}

	assert.Equal(t, c1, r.forge.tip("refs/heads/main"), "the refused ref did not move")
	assert.Equal(t, c1, r.forge.tip("refs/upstream/heads/main"), "and upstream's tip for it is not recorded as dealt with")
	assert.Equal(t, c1, r.forge.tip("refs/heads/free"), "and did not hold up the others")
	require.Len(t, r.notices(), 1, "refused once, reported once")
	assert.Contains(t, r.notices()[0], "refs/heads/main")
	assert.Contains(t, r.notices()[0], "protected branch")

	// Once it is allowed, which is a change of the rules, the next sync takes it with no new upstream commit.
	require.NoError(t, os.Remove(filepath.Join(r.dir, "refuse")))
	require.NoError(t, db.Insert(t.Context(), &git_model.ProtectedBranch{RepoID: r.repo.ID, RuleName: "main", CanPush: true}))
	require.True(t, r.sync())
	assert.Equal(t, c2, r.forge.tip("refs/heads/main"))
}

// A push cut short, by a deploy say, leaves the forge where it was. The tip must
// not be recorded as dealt with, or no later sync would look at it again.
func TestInboundCancelledPushIsRetried(t *testing.T) {
	r := newRig(t)
	c1 := r.github.commit("", "one")
	r.github.set("refs/heads/main", c1)
	require.True(t, r.sync())
	r.forget()

	c2 := r.github.commit(c1, "two")
	r.github.set("refs/heads/main", c2)
	require.NoError(t, os.WriteFile(filepath.Join(r.dir, "delay"), []byte("2"), 0o644))

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := make(chan bool, 1)
	go func() { done <- SyncInboundMirror(ctx, r.mirror.ID) }()
	exists := func(name string) func() bool {
		return func() bool { _, err := os.Stat(filepath.Join(r.dir, name)); return err == nil }
	}
	require.Eventually(t, exists("entered"), 10*time.Second, 10*time.Millisecond, "the push never reached pre-receive")
	cancel()

	assert.False(t, <-done)
	assert.Empty(t, r.notices(), "a push cut short is not a refusal")
	assert.Equal(t, c1, r.forge.tip("refs/upstream/heads/main"), "and the tip is not recorded as dealt with")

	// Whether the receiving side lands the ref anyway is up to it. The next sync does the rest.
	require.Eventually(t, exists("finished"), 10*time.Second, 10*time.Millisecond)
	time.Sleep(time.Second)
	require.NoError(t, os.Remove(filepath.Join(r.dir, "delay")))
	require.True(t, r.sync())
	assert.Equal(t, c2, r.forge.tip("refs/heads/main"))
	assert.Equal(t, c2, r.forge.tip("refs/upstream/heads/main"))
	assert.Empty(t, r.notices())
}

func TestInboundDoesNotResurrectDeletedBranch(t *testing.T) {
	r := newRig(t)
	c1 := r.github.commit("", "one")
	r.github.set("refs/heads/main", c1)
	r.github.set("refs/heads/merged-pr", c1)
	require.True(t, r.sync())

	// The forge deletes it; the push out never deletes on GitHub, so GitHub still has it.
	r.forge.delete("refs/heads/merged-pr")
	for range 2 {
		require.True(t, r.sync())
	}
	assert.Empty(t, r.forge.tip("refs/heads/merged-pr"), "a branch deleted here stays deleted while GitHub leaves it alone")

	// Unless GitHub moves it again.
	r.github.set("refs/heads/merged-pr", r.github.commit(c1, "more"))
	require.True(t, r.sync())
	assert.Equal(t, r.github.tip("refs/heads/merged-pr"), r.forge.tip("refs/heads/merged-pr"))
}

func TestInboundFirstSyncOfPopulatedRepoOnlyRecordsBranches(t *testing.T) {
	r := newRig(t)

	// The repository is in use, and has never synced inbound: it has main and none of the rest.
	c1 := r.forge.commit("", "one")
	r.forge.set("refs/heads/main", c1)
	r.github.git("fetch", r.forge.path, "refs/heads/main:refs/heads/main")
	r.github.set("refs/heads/long-deleted", c1)
	r.github.git("tag", "v1", c1)
	r.repo.IsEmpty = false
	require.NoError(t, repo_model.UpdateRepositoryColsNoAutoTime(t.Context(), r.repo, "is_empty"))

	require.True(t, r.sync())
	assert.Empty(t, r.forge.tip("refs/heads/long-deleted"), "a branch the forge lacks is not created by a first sync")
	assert.Equal(t, c1, r.forge.tip("refs/upstream/heads/long-deleted"), "but upstream is now on record")
	assert.Equal(t, c1, r.forge.tip("refs/tags/v1"), "a tag is immutable: the forge lacking one is just missing it")
	assert.Equal(t, []string{"refs/tags/v1"}, r.calls("pre-receive"))

	// From here on, a new branch on GitHub arrives.
	r.github.set("refs/heads/fresh", c1)
	require.True(t, r.sync())
	assert.Equal(t, c1, r.forge.tip("refs/heads/fresh"))
	assert.Empty(t, r.forge.tip("refs/heads/long-deleted"))
}

// A branch the forge created and pushed out, then deleted, is on GitHub and
// recorded, so GitHub having it is not news to the inbound sync.
func TestPushedOutBranchDeletedHereStaysDeleted(t *testing.T) {
	r := newRig(t)
	c1 := r.forge.commit("", "one")
	r.forge.set("refs/heads/main", c1)
	r.forge.set("refs/heads/feature", c1)
	r.forge.git("tag", "v1", c1)
	require.True(t, SyncPushMirror(t.Context(), r.mirror.ID))
	require.Equal(t, c1, r.github.tip("refs/heads/feature"))
	assert.Equal(t, c1, r.forge.tip("refs/upstream/heads/feature"), "what was pushed out is on record")
	assert.Equal(t, c1, r.forge.tip("refs/upstream/tags/v1"))

	// An inbound sync with nothing new establishes the baseline; then the branch goes.
	require.True(t, r.sync())
	r.forge.delete("refs/heads/feature")
	r.github.set("refs/heads/other", r.github.commit(c1, "elsewhere"))
	require.True(t, r.sync())

	assert.Empty(t, r.forge.tip("refs/heads/feature"), "pushed out, then deleted here: not brought back")
	assert.Equal(t, r.github.tip("refs/heads/other"), r.forge.tip("refs/heads/other"))
}

func TestInboundIgnoresOtherRefsUnderTheNamespace(t *testing.T) {
	r := newRig(t)
	c1 := r.github.commit("", "one")
	r.github.set("refs/heads/main", c1)
	require.True(t, r.sync())
	r.forget()

	// What a writer might seed: nothing but a fetch's own branches and tags is taken for upstream.
	r.forge.set("refs/upstream/pull/7/head", c1)
	r.forge.set("refs/upstream/meta/anything", c1)
	r.forge.set("refs/upstream/heads2/x", c1)
	r.github.set("refs/heads/other", c1)
	require.True(t, r.sync())

	assert.Empty(t, r.forge.tip("refs/pull/7/head"))
	assert.Empty(t, r.forge.tip("refs/meta/anything"))
	assert.Empty(t, r.forge.tip("refs/heads2/x"))
	assert.Equal(t, []string{"refs/heads/other"}, r.calls("pre-receive"))

	// A tip seeded under heads/ is overwritten by the fetch with what upstream really has.
	evil := r.forge.commit(c1, "evil")
	r.forge.set("refs/upstream/heads/main", evil)
	require.True(t, r.sync())
	assert.Equal(t, c1, r.forge.tip("refs/heads/main"))
}

func TestInboundHidesTheNamespaceFromClients(t *testing.T) {
	r := newRig(t)
	c1 := r.github.commit("", "one")
	r.github.set("refs/heads/main", c1)
	r.github.set("refs/heads/private-wip", c1)
	require.True(t, r.sync())
	require.NotEmpty(t, r.forge.tip("refs/upstream/heads/private-wip"))
	require.NotEmpty(t, r.forge.tip("refs/upstream/staging/heads/private-wip"))

	advertised := r.forge.git("ls-remote", r.forge.path)
	assert.Contains(t, advertised, "refs/heads/main")
	assert.NotContains(t, advertised, "refs/upstream/")
	assert.Equal(t, "refs/upstream", r.forge.git("config", "--get-all", "uploadpack.hideRefs"))
	// Hidden from every pusher: the sync's own push is the only one told of the staged tips.
	assert.Equal(t, "refs/upstream", r.forge.git("config", "--get-all", "receive.hideRefs"))
	assert.Equal(t, "false", r.forge.git("config", "--get", "uploadpack.allowAnySHA1InWant"))
}

func TestInboundSkipsArchivedRepo(t *testing.T) {
	r := newRig(t)
	c1 := r.github.commit("", "one")
	r.github.set("refs/heads/main", c1)
	require.True(t, r.sync())
	r.forget()

	r.repo.IsArchived = true
	require.NoError(t, repo_model.UpdateRepositoryColsNoAutoTime(t.Context(), r.repo, "is_archived"))
	r.github.set("refs/heads/main", r.github.commit(c1, "two"))

	assert.False(t, r.sync())
	assert.Equal(t, c1, r.forge.tip("refs/heads/main"), "an archived repository is read-only")
	assert.Empty(t, r.calls("pre-receive"))
}

func TestInboundNeedsASyncUserWithWrite(t *testing.T) {
	r := newRig(t)
	c1 := r.github.commit("", "one")
	r.github.set("refs/heads/main", c1)

	t.Cleanup(test.MockVariableValue(&setting.Mirror.SyncUser, ""))
	assert.False(t, r.sync(), "no sync user, no inbound sync")

	setting.Mirror.SyncUser = "nonesuch"
	assert.False(t, r.sync())

	// A user the repository gives no write access to.
	reader := unittest.AssertExistsAndLoadBean(t, &user_model.User{ID: 4})
	perm, err := access_model.GetDoerRepoPermission(t.Context(), r.repo, reader)
	require.NoError(t, err)
	require.False(t, perm.CanWrite(unit.TypeCode), "premise: user4 cannot write to the repository")
	setting.Mirror.SyncUser = reader.Name
	assert.False(t, r.sync())
	require.Len(t, r.notices(), 1)
	assert.Contains(t, r.notices()[0], reader.Name)

	assert.Empty(t, r.forge.tip("refs/heads/main"))
	assert.Empty(t, r.calls("pre-receive"))
}

func TestInboundSkipsWhatIsNoLongerARegularGitHubRepo(t *testing.T) {
	r := newRig(t)
	c1 := r.github.commit("", "one")
	r.github.set("refs/heads/main", c1)

	// A pull mirror has its own upstream and its own sync.
	require.NoError(t, db.Insert(t.Context(), &repo_model.Mirror{RepoID: r.repo.ID, Interval: time.Hour}))
	assert.False(t, r.sync())
	_, err := db.DeleteByBean(t.Context(), &repo_model.Mirror{RepoID: r.repo.ID})
	require.NoError(t, err)

	// A push mirror whose address no longer names this repository on GitHub.
	r.mirror.RemoteAddress = "https://example.com/org3/repo21"
	require.NoError(t, repo_model.UpdatePushMirror(t.Context(), r.mirror))
	assert.False(t, r.sync())

	assert.Empty(t, r.forge.tip("refs/heads/main"))
	assert.Empty(t, r.calls("pre-receive"))
}

func TestInboundFailsOnUnreachableRemote(t *testing.T) {
	r := newRig(t)
	require.NoError(t, os.RemoveAll(r.github.path))

	assert.False(t, r.sync())
	assert.False(t, r.sync())
	require.Len(t, r.notices(), 1, "the same failure is reported once")
	assert.Contains(t, r.notices()[0], r.repo.FullName())
}

// A push out takes no lock a pull takes: a clone of a pull mirror syncs inline,
// and must not wait for a slow push out.
func TestPushOutDoesNotWaitForPull(t *testing.T) {
	r := newRig(t)
	r.forge.set("refs/heads/main", r.forge.commit("", "one"))
	release, err := globallock.Lock(t.Context(), getRepoPullMirrorLockKey(r.repo.ID))
	require.NoError(t, err)
	defer release()

	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	start := time.Now()
	assert.True(t, SyncPushMirror(ctx, r.mirror.ID))
	assert.Less(t, time.Since(start), 4*time.Second)
}

func TestIsGitHubRepo(t *testing.T) {
	for address, want := range map[string]bool{
		"https://github.com/hanzo-inc/app":                true,
		"https://github.com/hanzo-inc/app.git":            true,
		"https://github.com/hanzo-inc/app/":               true,
		"https://github.com/hanzo-inc/app.git/":           true,
		"https://user:token@github.com/hanzo-inc/app.git": true,
		"https://x-access-token@github.com/hanzo-inc/app": true,
		"HTTPS://GitHub.com/Hanzo-Inc/APP":                true,
		"ssh://git@github.com/hanzo-inc/app.git":          true,
		"git@github.com:hanzo-inc/app.git":                true,

		"http://github.com/hanzo-inc/app":               false, // credentials in the clear
		"git://github.com/hanzo-inc/app":                false, // no credentials at all
		"https://github.com/hanzo-inc/other":            false,
		"https://github.com/other/app":                  false,
		"https://github.com/hanzo-inc/app-extra":        false,
		"https://github.com/hanzo-inc/app/tree/main":    false,
		"https://github.com/app":                        false,
		"https://gitlab.com/hanzo-inc/app":              false,
		"https://notgithub.com/hanzo-inc/app":           false,
		"https://github.com.evil.example/hanzo-inc/app": false,
		"https://evil.example/github.com/hanzo-inc/app": false,
		"/srv/git/github.com/hanzo-inc/app":             false,
		"file:///srv/github.com/hanzo-inc/app":          false,
		"":                                              false,

		// Ways to write a different host or path that look like this one.
		`https://github.com\@evil.com/hanzo-inc/app`:     false,
		`https://evil.com\@github.com/hanzo-inc/app`:     false,
		`https://github.com:80@evil.com/hanzo-inc/app`:   false,
		`https://github.com#@evil.com/hanzo-inc/app`:     false,
		`https://github.com?@evil.com/hanzo-inc/app`:     false,
		`https://user@github.com@evil.com/hanzo-inc/app`: false,
		`https://github.com%2f.evil.com/hanzo-inc/app`:   false,
		"https://github.com./hanzo-inc/app":              false,
		"https://github.com/hanzo-inc%2fapp":             false,
		"https://github.com/hanzo-inc/app/../app":        false,
		"https://github.com/hanzo-inc/./app":             false,
		"https://github.com//hanzo-inc/app":              false,
		"https://github.com:8080/hanzo-inc/app":          false,
		"https://github.com/hanzo-inc/app\n":             false,
		"ssh://git@github.com:2222/hanzo-inc/app":        false,
		"git@evil.com:github.com/hanzo-inc/app.git":      false,
		"git@github.com:evil/../hanzo-inc/app.git":       false,
		"https://github.com/hanzo-inc/app?x=1":           false,
		"https://github.com/hanzo-inc/app#frag":          false,
	} {
		assert.Equal(t, want, IsGitHubRepo(address, "hanzo-inc", "app"), "%q", address)
	}
	assert.False(t, IsGitHubRepo(" https://github.com/hanzo-inc/app", "hanzo-inc", "app"), "a leading space")
}

// The sweep looks for what an event missed in every push mirror of a regular
// repository to GitHub, on a clock of its own: a repository that pushes out all
// the time, or one that only pushes on commit, is no less likely to miss an event.
func TestSweepHealsWhatAnEventMissed(t *testing.T) {
	r := newRig(t)
	t.Cleanup(test.MockVariableValue(&setting.Mirror.Enabled, true))
	t.Cleanup(test.MockVariableValue(&mirrorQueue))
	queued := make(chan SyncRequest, 10)
	mirrorQueue = startQueue("mirror_sweep_test", func(items ...*SyncRequest) []*SyncRequest {
		for _, item := range items {
			queued <- *item
		}
		return nil
	})
	// sweep runs the sweep and returns what it queued: it waits for want requests
	// to come through the queue, then for any that should not.
	// SQLite matches LIKE without regard to case unless told not to, as the other databases do not.
	_, err := db.GetEngine(t.Context()).Exec("PRAGMA case_sensitive_like = ON")
	require.NoError(t, err)
	t.Cleanup(func() { _, _ = db.GetEngine(context.Background()).Exec("PRAGMA case_sensitive_like = OFF") })

	sweep := func(want int) []SyncRequest {
		require.NoError(t, Update(t.Context(), 0, 10))
		var got []SyncRequest
		wait := 5 * time.Second
		if want == 0 {
			wait = 400 * time.Millisecond
		}
		for {
			select {
			case req := <-queued:
				got = append(got, req)
			case <-time.After(wait):
				return got
			}
			if len(got) >= want {
				wait = 400 * time.Millisecond
			}
		}
	}
	inbound := []SyncRequest{{InboundMirrorType, r.mirror.ID}}

	// A busy one: it pushed out a moment ago and is not due for that for an hour.
	r.mirror.Interval = time.Hour
	r.mirror.LastUpdateUnix = timeutil.TimeStampNow()
	require.NoError(t, repo_model.UpdatePushMirror(t.Context(), r.mirror))
	assert.Equal(t, inbound, sweep(1), "due on its own clock, with no push out due")

	// It has just been looked at, so it is not due again straight away.
	assert.Empty(t, sweep(0))

	// Once the period has passed it is, whatever the push out has done since.
	r.mirror.LastInboundUnix = timeutil.TimeStampNow() - timeutil.TimeStamp(setting.Mirror.DefaultInterval/time.Second) - 1
	require.NoError(t, repo_model.UpdatePushMirrorLastInbound(t.Context(), r.mirror))
	assert.Equal(t, inbound, sweep(1))

	// A push mirror that only pushes on commit has no push out due, and is covered the same.
	r.mirror.Interval = 0
	require.NoError(t, repo_model.UpdatePushMirror(t.Context(), r.mirror))
	r.mirror.LastInboundUnix = 0
	require.NoError(t, repo_model.UpdatePushMirrorLastInbound(t.Context(), r.mirror))
	assert.Equal(t, inbound, sweep(1))

	// The address is matched in any letter case, as it is elsewhere.
	r.mirror.RemoteAddress = "https://GitHub.COM/Org3/Repo21.git"
	require.NoError(t, repo_model.UpdatePushMirror(t.Context(), r.mirror))
	r.mirror.LastInboundUnix = 0
	require.NoError(t, repo_model.UpdatePushMirrorLastInbound(t.Context(), r.mirror))
	assert.Equal(t, inbound, sweep(1))

	// Not one whose address is another repository, another host, or not here: the repository was renamed, say.
	for _, address := range []string{"https://github.com/org3/renamed", "https://example.com/org3/repo21"} {
		r.mirror.RemoteAddress = address
		require.NoError(t, repo_model.UpdatePushMirror(t.Context(), r.mirror))
		r.mirror.LastInboundUnix = 0
		require.NoError(t, repo_model.UpdatePushMirrorLastInbound(t.Context(), r.mirror))
		assert.Empty(t, sweep(0), address)
	}

	// Nor with nobody to pull in as.
	r.mirror.RemoteAddress = "https://github.com/org3/repo21"
	require.NoError(t, repo_model.UpdatePushMirror(t.Context(), r.mirror))
	r.mirror.LastInboundUnix = 0
	require.NoError(t, repo_model.UpdatePushMirrorLastInbound(t.Context(), r.mirror))
	setting.Mirror.SyncUser = ""
	assert.Empty(t, sweep(0))
}

// Tags the forge lacks, more than one, come in one push. A push event for each
// would run every old tag's release workflow.
func TestInboundImportsSeveralTagsInOnePush(t *testing.T) {
	r := newRig(t)
	c1 := r.github.commit("", "one")
	r.github.set("refs/heads/main", c1)
	require.True(t, r.sync())
	r.forget()

	for _, tag := range []string{"v1", "v2", "v3", "v4", "v5"} {
		r.github.git("tag", tag, c1)
	}
	require.True(t, r.sync())
	for _, tag := range []string{"v1", "v2", "v3", "v4", "v5"} {
		assert.Equal(t, c1, r.forge.tip("refs/tags/"+tag))
		unittest.AssertExistsAndLoadBean(t, &repo_model.Release{RepoID: r.repo.ID, TagName: tag})
	}
	assert.Zero(t, r.invocations("pre-receive"), "no protection to apply, and no push event per tag")

	// One new tag is an ordinary push, as a person's would be.
	r.forget()
	r.github.git("tag", "v6", c1)
	require.True(t, r.sync())
	assert.Equal(t, []string{"refs/tags/v6"}, r.calls("pre-receive"))

	// The rules for tags are applied in Go, as pre-receive would: a tag the sync user
	// may not create is left, and noticed once; the rest still come in one push, unhooked.
	require.NoError(t, db.Insert(t.Context(), &git_model.ProtectedTag{RepoID: r.repo.ID, NamePattern: "v*"}))
	r.forget()
	for _, tag := range []string{"v7", "v8", "w1", "w2", "w3"} {
		r.github.git("tag", tag, c1)
	}
	for range 3 {
		require.True(t, r.sync())
	}
	assert.Empty(t, r.forge.tip("refs/tags/v7"))
	assert.Empty(t, r.forge.tip("refs/tags/v8"))
	assert.Equal(t, c1, r.forge.tip("refs/tags/w3"))
	assert.Zero(t, r.invocations("pre-receive"), "the allowed ones came in one push without the hooks")
	unittest.AssertExistsAndLoadBean(t, &repo_model.Release{RepoID: r.repo.ID, TagName: "w1"})
	require.Len(t, r.notices(), 2, "one for each tag, however many syncs")
	assert.Contains(t, strings.Join(r.notices(), "\n"), "refs/tags/v7")

	// The sync user allowed to create them, they come: one push.
	rule := unittest.AssertExistsAndLoadBean(t, &git_model.ProtectedTag{RepoID: r.repo.ID})
	rule.AllowlistUserIDs = []int64{1}
	require.NoError(t, git_model.UpdateProtectedTag(t.Context(), rule))
	require.True(t, r.sync())
	assert.Equal(t, c1, r.forge.tip("refs/tags/v7"))
	assert.Equal(t, c1, r.forge.tip("refs/tags/v8"))
	assert.Zero(t, r.invocations("pre-receive"))
}

// What a branch is protected by has no bearing on a tag: pre-receive looks at the
// protected tags alone, so a repository that protects only main still has its old
// tags come in one push, with no hooks.
func TestInboundBranchProtectionDoesNotHookTags(t *testing.T) {
	r := newRig(t)
	require.NoError(t, db.Insert(t.Context(), &git_model.ProtectedBranch{RepoID: r.repo.ID, RuleName: "main"}))
	c1 := r.github.commit("", "one")
	r.github.set("refs/heads/main", c1)
	for i := range 12 {
		r.github.git("tag", fmt.Sprintf("v%d", i), c1)
	}

	require.True(t, r.sync())

	assert.Equal(t, c1, r.forge.tip("refs/tags/v11"))
	assert.Equal(t, []string{"refs/heads/main"}, r.calls("pre-receive"), "the branch went through the hooks, the twelve tags did not")
	assert.Equal(t, 1, r.invocations("pre-receive"))
	for i := range 12 {
		unittest.AssertExistsAndLoadBean(t, &repo_model.Release{RepoID: r.repo.ID, TagName: fmt.Sprintf("v%d", i)})
	}
}

// A seed that stops partway is carried on by the next run, however the
// repository looks by then, and its tags still come in one push.
func TestInboundSeedResumesAfterAPartialFailure(t *testing.T) {
	r := newRig(t)
	c1 := r.github.commit("", "one")
	r.github.git("symbolic-ref", "HEAD", "refs/heads/trunk")
	r.github.set("refs/heads/trunk", c1)
	r.github.set("refs/heads/b1", c1)
	r.github.set("refs/heads/b2", c1)
	for _, tag := range []string{"v1", "v2", "v3"} {
		r.github.git("tag", tag, c1)
	}

	r.refuse("refs/heads/b1")
	assert.False(t, r.sync())
	assert.Equal(t, c1, r.forge.tip("refs/heads/trunk"), "the default branch went in first")
	assert.Empty(t, r.forge.tip("refs/heads/b2"), "the rest was one push, which was refused")
	assert.Empty(t, r.forge.tip("refs/tags/v1"))
	require.NoError(t, os.Remove(filepath.Join(r.dir, "refuse")))
	r.forget()

	require.True(t, r.sync())
	assert.Equal(t, c1, r.forge.tip("refs/heads/b1"))
	assert.Equal(t, c1, r.forge.tip("refs/heads/b2"))
	assert.Equal(t, c1, r.forge.tip("refs/tags/v3"))
	assert.Equal(t, []string{"refs/heads/b1", "refs/heads/b2"}, r.calls("pre-receive"), "the tags raised no hooks")
	for _, tag := range []string{"v1", "v2", "v3"} {
		unittest.AssertExistsAndLoadBean(t, &repo_model.Release{RepoID: r.repo.ID, TagName: tag})
	}
}

// What a bot opens on GitHub is never brought in: it would run here with the
// organization's secrets. It is not fetched at all, so it is not in the repository
// to be asked for by id, in whatever case the name is.
func TestInboundNeverBringsInBotBranches(t *testing.T) {
	bots := []string{
		"refs/heads/dependabot/npm_and_yarn/x", "refs/heads/renovate/foo", "refs/heads/gh-readonly-queue/main/pr-1",
		"refs/heads/copilot/fix-1", "refs/heads/release-please--branches--main", "refs/heads/changeset-release/main",
	}
	for _, filled := range []bool{false, true} {
		r := newRig(t)
		c1 := r.github.commit("", "one")
		r.github.set("refs/heads/main", c1)
		if filled {
			require.True(t, r.sync()) // the repository has content now: the loop, not the seed
			r.forget()
		}
		var tips []string
		for _, bot := range bots {
			tip := r.github.commit(c1, "bot "+bot)
			tips = append(tips, tip)
			r.github.set(bot, tip)
		}
		cased := r.github.commit(c1, "bot, in capitals")
		r.github.set("refs/heads/Dependabot/Capital", cased)
		r.github.set("refs/heads/feature", c1)
		// Not a bot's, whatever it looks like.
		r.github.set("refs/heads/deps/update", c1)

		require.True(t, r.sync())

		assert.Equal(t, c1, r.forge.tip("refs/heads/feature"), "filled=%v", filled)
		assert.Equal(t, c1, r.forge.tip("refs/heads/deps/update"), "filled=%v", filled)
		for i, bot := range bots {
			assert.Empty(t, r.forge.tip(bot), "%s filled=%v", bot, filled)
			assert.Empty(t, r.forge.tip(recordName(bot)), "never fetched, so nothing to record")
			_, err := exec.Command("git", "-C", r.forge.path, "cat-file", "-e", tips[i]).CombinedOutput()
			assert.Error(t, err, "%s: its objects are not in the repository", bot)
		}
		assert.Empty(t, r.forge.tip("refs/heads/Dependabot/Capital"), "filled=%v", filled)
		_, err := exec.Command("git", "-C", r.forge.path, "cat-file", "-e", cased).CombinedOutput()
		assert.Error(t, err, "a name in capitals is not fetched either: its commit is not in the repository")
		assert.Empty(t, r.forge.tip("refs/upstream/heads/Dependabot/Capital"), "not kept, whatever the case")
		assert.Empty(t, r.forge.tip("refs/upstream/staging/heads/Dependabot/Capital"))
		assert.NotContains(t, strings.Join(r.calls("pre-receive"), " "), "ependabot")
		assert.Empty(t, r.notices())
	}
}

// Every commit of a branch that requires signatures is refused by name, and the
// name carries the commit: it is one condition, not one per commit.
func TestInboundNoticeIgnoresCommitsInTheReason(t *testing.T) {
	r := newRig(t)
	c1 := r.github.commit("", "one")
	r.github.set("refs/heads/main", c1)
	require.True(t, r.sync())
	r.refuse("refs/heads/main")

	for _, message := range []string{"two", "three", "four"} {
		r.github.set("refs/heads/main", r.github.commit(r.github.tip("refs/heads/main"), message))
		require.True(t, r.sync())
	}

	require.Len(t, r.notices(), 1)
	assert.Contains(t, r.notices()[0], "unverified commit")
}

// A fetch that finds no refs says nothing about the ones recorded.
func TestInboundKeepsRecordsWhenTheRemoteAnswersEmpty(t *testing.T) {
	r := newRig(t)
	c1 := r.github.commit("", "one")
	r.github.set("refs/heads/main", c1)
	r.github.set("refs/heads/dev", c1)
	require.True(t, r.sync())
	r.forget()

	r.github.delete("refs/heads/main")
	r.github.delete("refs/heads/dev")
	require.True(t, r.sync())
	assert.Equal(t, c1, r.forge.tip("refs/upstream/heads/main"))
	assert.Equal(t, c1, r.forge.tip("refs/upstream/heads/dev"))

	// When it answers again, nothing is new.
	r.github.set("refs/heads/main", c1)
	r.github.set("refs/heads/dev", c1)
	r.forge.delete("refs/heads/dev")
	require.True(t, r.sync())
	assert.Empty(t, r.forge.tip("refs/heads/dev"), "a branch deleted here stays deleted across the outage")
	assert.Empty(t, r.calls("pre-receive"))

	// A ref gone while others remain is no longer recorded.
	r.github.delete("refs/heads/dev")
	require.True(t, r.sync())
	assert.Empty(t, r.forge.tip("refs/upstream/heads/dev"))
	assert.Equal(t, c1, r.forge.tip("refs/upstream/heads/main"))
}

// Filling a repository from itself does not send it its own history: the push is
// told, by what receive-pack advertises to it alone, that the repository has the staged tips.
func TestInboundSeedDoesNotSendHistoryBack(t *testing.T) {
	r := newRig(t)
	tip := ""
	for i := range 250 {
		tip = r.github.commit(tip, fmt.Sprintf("commit %d", i))
	}
	r.github.set("refs/heads/main", tip)

	require.True(t, r.sync())

	assert.Equal(t, tip, r.forge.tip("refs/heads/main"))
	assert.Equal(t, []string{"0"}, r.received(), "the push carried nothing: the repository already has all of it")

	// Told to nobody else: not to a client, not to a person who pushes.
	assert.NotContains(t, r.forge.git("receive-pack", "--advertise-refs", "."), "refs/upstream/")
	assert.NotContains(t, r.forge.git("upload-pack", "--advertise-refs", "."), "refs/upstream/")
}

// A seed does not go on writing to a repository archived while it ran.
func TestInboundSeedStopsWhenArchivedMidway(t *testing.T) {
	r := newRig(t)
	c1 := r.github.commit("", "one")
	r.github.git("symbolic-ref", "HEAD", "refs/heads/trunk")
	r.github.set("refs/heads/trunk", c1)
	r.github.set("refs/heads/other", c1)
	require.NoError(t, os.WriteFile(filepath.Join(r.dir, "delay"), []byte("1"), 0o644))

	done := make(chan bool, 1)
	go func() { done <- SyncInboundMirror(t.Context(), r.mirror.ID) }()
	require.Eventually(t, func() bool { _, err := os.Stat(filepath.Join(r.dir, "entered")); return err == nil }, 10*time.Second, 10*time.Millisecond)
	r.repo.IsArchived = true
	require.NoError(t, repo_model.UpdateRepositoryColsNoAutoTime(t.Context(), r.repo, "is_archived"))

	assert.False(t, <-done)
	assert.Equal(t, c1, r.forge.tip("refs/heads/trunk"), "the push under way finished")
	assert.Empty(t, r.forge.tip("refs/heads/other"), "and the next one was not made")
}

// A push that fails for one tag may have landed the others. What landed gets its
// release and its record all the same.
func TestInboundMakesReleasesOfTagsThatLandedInAFailedPush(t *testing.T) {
	r := newRig(t)
	c1 := r.github.commit("", "one")
	r.github.set("refs/heads/main", c1)
	require.True(t, r.sync())
	r.forget()

	// The forge has the tag "rel", which keeps it from having "rel/1".
	r.forge.git("tag", "rel", c1)
	for _, tag := range []string{"a", "b", "c", "rel/1"} {
		r.github.git("tag", tag, c1)
	}
	require.True(t, r.sync())

	assert.Empty(t, r.forge.tip("refs/tags/rel/1"))
	for _, tag := range []string{"a", "b", "c"} {
		assert.Equal(t, c1, r.forge.tip("refs/tags/"+tag))
		assert.Equal(t, c1, r.forge.tip("refs/upstream/tags/"+tag), "recorded: it is there")
		unittest.AssertExistsAndLoadBean(t, &repo_model.Release{RepoID: r.repo.ID, TagName: tag})
	}
	assert.Empty(t, r.forge.tip("refs/upstream/tags/rel/1"), "and the one that did not land is not recorded")
	require.Len(t, r.notices(), 1)
}

// The hooks declining a tag are not asked again until its tip, or the rules, change.
func TestInboundDoesNotAskTheHooksAgainAboutADeclinedTag(t *testing.T) {
	r := newRig(t)
	c1 := r.github.commit("", "one")
	c2 := r.github.commit(c1, "two")
	r.github.set("refs/heads/main", c2)
	require.True(t, r.sync())
	r.forget()

	r.refuse("refs/tags/v1")
	r.github.git("tag", "v1", c1)
	for range 3 {
		require.True(t, r.sync())
	}
	assert.Equal(t, 1, r.invocations("pre-receive"), "asked once, not at every sync")
	assert.Empty(t, r.forge.tip("refs/tags/v1"))
	require.Len(t, r.notices(), 1)
	assert.Contains(t, r.notices()[0], "protected branch")

	// Upstream's tag is a different tag now.
	r.github.git("tag", "-f", "v1", c2)
	require.True(t, r.sync())
	assert.Equal(t, 2, r.invocations("pre-receive"))

	// And the rules are different.
	require.NoError(t, db.Insert(t.Context(), &git_model.ProtectedTag{RepoID: r.repo.ID, NamePattern: "nothing-matches-this"}))
	require.True(t, r.sync())
	assert.Equal(t, 3, r.invocations("pre-receive"))
	require.True(t, r.sync())
	assert.Equal(t, 3, r.invocations("pre-receive"))
}

// What the sync skips is not for a client to ask for by id: not in the
// repository if it was never fetched, and not served to a client that speaks the
// protocol that checks, if it was. (The newer protocol serves any object it is
// asked for; that is why the objects are not brought in.)
func TestInboundSkippedTipsCannotBeFetchedById(t *testing.T) {
	r := newRig(t)
	r.repo.IsEmpty = false
	require.NoError(t, repo_model.UpdateRepositoryColsNoAutoTime(t.Context(), r.repo, "is_empty"))
	c1 := r.forge.commit("", "one")
	r.forge.set("refs/heads/main", c1)
	r.github.git("fetch", r.forge.path, "refs/heads/main:refs/heads/main")
	// The forge allows any id in a want, for partial clones.
	r.forge.git("config", "uploadpack.allowAnySHA1InWant", "true")

	// A branch the forge lacks: on a first sync it is recorded, and held, not created.
	old := r.github.commit(c1, "an old branch the forge deleted")
	r.github.set("refs/heads/old", old)
	require.True(t, r.sync())
	require.Equal(t, old, r.forge.tip("refs/upstream/heads/old"))
	assert.Empty(t, r.forge.tip("refs/heads/old"))

	fetch := func(protocol string) error {
		clone := t.TempDir()
		for _, args := range [][]string{{"init", "-q"}, {"-c", "protocol.version=" + protocol, "fetch", r.forge.path, old}} {
			cmd := exec.Command("git", args...)
			cmd.Dir = clone
			if out, err := cmd.CombinedOutput(); err != nil {
				return fmt.Errorf("%w: %s", err, out)
			}
		}
		return nil
	}
	assert.Error(t, fetch("0"), "not by its id, where the protocol checks")

	// Which it would, were the forge's setting still in force: the test asks the right question.
	r.forge.git("config", "uploadpack.allowAnySHA1InWant", "true")
	assert.NoError(t, fetch("0"))
}

// What an earlier version left of a bot's branch, staged or recorded, is removed.
func TestInboundRemovesWhatWasKeptOfBotBranches(t *testing.T) {
	r := newRig(t)
	c1 := r.github.commit("", "one")
	r.github.set("refs/heads/main", c1)
	require.True(t, r.sync())

	r.forge.set("refs/upstream/staging/heads/dependabot/old", c1)
	r.forge.set("refs/upstream/heads/renovate/old", c1)
	r.forge.set("refs/upstream/heads/Copilot/old", c1)
	r.github.set("refs/heads/other", c1)
	require.True(t, r.sync())

	assert.Empty(t, r.forge.tip("refs/upstream/staging/heads/dependabot/old"))
	assert.Empty(t, r.forge.tip("refs/upstream/heads/renovate/old"))
	assert.Empty(t, r.forge.tip("refs/upstream/heads/Copilot/old"))
	assert.Equal(t, c1, r.forge.tip("refs/upstream/heads/main"))
}

// A name that looks like an object id can be read as the object, so it is not taken.
func TestInboundSkipsNamesLikeObjectIds(t *testing.T) {
	r := newRig(t)
	c1 := r.github.commit("", "one")
	r.github.set("refs/heads/main", c1)
	named := r.github.commit(c1, "on a branch named like an id")
	r.github.set("refs/heads/"+c1, named)
	r.github.git("tag", strings.ToUpper(c1), c1)
	r.github.git("tag", "v1", c1)

	require.True(t, r.sync())
	require.True(t, r.sync())

	assert.Equal(t, c1, r.forge.tip("refs/heads/main"))
	assert.Equal(t, c1, r.forge.tip("refs/tags/v1"))
	assert.Empty(t, r.forge.tip("refs/heads/"+c1))
	assert.Empty(t, r.forge.tip("refs/tags/"+strings.ToUpper(c1)))
	assert.Empty(t, r.forge.tip("refs/upstream/heads/"+c1))
	_, err := exec.Command("git", "-C", r.forge.path, "cat-file", "-e", named).CombinedOutput()
	assert.Error(t, err, "never fetched: its commit is not in the repository")
	assert.Len(t, r.notices(), 2, "one for each, once")
}

// git reads <id> as a ref's name before it reads it as an object, and a writer
// may have made refs/heads/<id> or refs/tags/<id>. The sync pushes the object.
func TestInboundPushCannotBeRedirectedByRefsNamedLikeTheTip(t *testing.T) {
	r := newRig(t)
	c1 := r.github.commit("", "one")
	r.github.set("refs/heads/main", c1)
	require.True(t, r.sync())
	r.forget()

	attacker := r.forge.commit("", "the writer's commit")
	decoyBranch := func(id string) { r.forge.set("refs/heads/"+id, attacker) }
	decoyTag := func(id string) { r.forge.set("refs/tags/"+id, attacker) }
	decoyAnnotated := func(id string) { r.forge.git("tag", "-a", "-m", "decoy", id, attacker) }

	// A branch that moves forward, and one that is new.
	moved := r.github.commit(c1, "two")
	fresh := r.github.commit(c1, "fresh")
	r.github.set("refs/heads/main", moved)
	r.github.set("refs/heads/fresh", fresh)
	decoyBranch(moved)
	decoyTag(fresh)

	// Tags together, then a tag alone: lightweight and annotated, with a decoy of either kind for each.
	r.github.git("tag", "light", c1)
	r.github.git("tag", "-a", "-m", "annotated", "annotated", moved)
	annotated := r.github.tip("refs/tags/annotated")
	r.github.git("tag", "-a", "-m", "annotated too", "annotated2", moved)
	annotated2 := r.github.tip("refs/tags/annotated2")
	decoyBranch(c1)
	decoyAnnotated(annotated)
	decoyTag(annotated2)

	require.True(t, r.sync())
	assert.Equal(t, moved, r.forge.tip("refs/heads/main"), "a decoy named for the tip did not take its place")
	assert.Equal(t, fresh, r.forge.tip("refs/heads/fresh"))
	assert.Equal(t, c1, r.forge.tip("refs/tags/light"))
	assert.Equal(t, annotated, r.forge.tip("refs/tags/annotated"))
	assert.Equal(t, annotated2, r.forge.tip("refs/tags/annotated2"))
	assert.Equal(t, "tag", r.forge.git("for-each-ref", "--format=%(objecttype)", "refs/tags/annotated"), "an annotated tag stays a tag object")

	// One tag, which goes the ordinary way.
	single := r.github.commit(moved, "single")
	r.github.git("tag", "single", single)
	decoyBranch(single)
	require.True(t, r.sync())
	assert.Equal(t, single, r.forge.tip("refs/tags/single"))

	// The writer's own refs are what they were.
	assert.Equal(t, attacker, r.forge.tip("refs/heads/"+moved))
	assert.Equal(t, attacker, r.forge.tip("refs/heads/"+c1))
}

// A protected branch refuses the same push every time, and there may be many.
func TestInboundDoesNotAskTheHooksAgainAboutDeclinedBranches(t *testing.T) {
	r := newRig(t)
	c1 := r.github.commit("", "one")
	r.github.set("refs/heads/main", c1)
	require.True(t, r.sync())
	r.forget()

	branches := []string{"refs/heads/b1", "refs/heads/b2", "refs/heads/b3"}
	r.refuse(branches...)
	for _, branch := range branches {
		r.github.set(branch, c1)
	}
	for range 3 {
		require.True(t, r.sync())
	}
	assert.Equal(t, 3, r.invocations("pre-receive"), "each asked once, not at every sync")
	assert.Len(t, r.notices(), 3)

	// One of them moves, which is a different push to refuse.
	r.github.set("refs/heads/b1", r.github.commit(c1, "b1 moves"))
	require.True(t, r.sync())
	assert.Equal(t, 4, r.invocations("pre-receive"))

	// The rules change: all are asked again.
	require.NoError(t, db.Insert(t.Context(), &git_model.ProtectedBranch{RepoID: r.repo.ID, RuleName: "b*"}))
	require.True(t, r.sync())
	assert.Equal(t, 7, r.invocations("pre-receive"))
	require.True(t, r.sync())
	assert.Equal(t, 7, r.invocations("pre-receive"))
}

// A bot's branch made after the remote was listed is not in the list, so it is
// kept out of the fetch by its prefix as well as by its name.
func TestFetchRefspecs(t *testing.T) {
	offered := map[string]string{
		"refs/heads/main":                       "1",
		"refs/heads/Dependabot/zz":              "2",
		"refs/heads/renovate/x":                 "3",
		"refs/heads/" + strings.Repeat("a", 40): "4",
		"refs/tags/v1":                          "5",
	}

	refspecs := fetchRefspecs(offered)

	assert.Equal(t, []string{
		"+refs/heads/*:refs/upstream/staging/heads/*",
		"+refs/tags/*:refs/upstream/staging/tags/*",
	}, refspecs[:2])
	for _, prefix := range botBranches {
		assert.Contains(t, refspecs, "^refs/heads/"+prefix+"*")
	}
	for _, ref := range []string{"refs/heads/Dependabot/zz", "refs/heads/renovate/x", "refs/heads/" + strings.Repeat("a", 40)} {
		assert.Contains(t, refspecs, "^"+ref)
	}
	assert.NotContains(t, refspecs, "^refs/heads/main")
	assert.NotContains(t, refspecs, "^refs/tags/v1")
}
