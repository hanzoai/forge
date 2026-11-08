// Copyright 2026 Hanzo AI, Inc. All rights reserved.
// SPDX-License-Identifier: MIT

package mirror

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/hanzoai/git/models/db"
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
  while read -r old new ref; do
    echo "$hook $ref" >> "$dir/hooks.log"
    if [ "$hook" = pre-receive ] && grep -qxF "$ref" "$dir/refuse" 2>/dev/null; then
      echo "protected branch" >&2
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

	// Each ref went through pre-receive and post-receive, which is where the
	// daemon enforces protection and starts Actions, pull request checks and the push out.
	want := []string{"refs/heads/main", "refs/heads/topic", "refs/tags/light", "refs/tags/v1"}
	assert.ElementsMatch(t, want, r.calls("pre-receive"))
	assert.ElementsMatch(t, want, r.calls("post-receive"))
	assert.ElementsMatch(t, want, r.calls("update"))
	assert.Equal(t, 4, r.invocations("pre-receive"), "one push per ref: a refusal of one must not hold up the others")

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

	// Once it is allowed, the next sync takes it with no new upstream commit.
	require.NoError(t, os.Remove(filepath.Join(r.dir, "refuse")))
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
	assert.Equal(t, "refs/upstream", r.forge.git("config", "--get", "uploadpack.hideRefs"))
	assert.Equal(t, "refs/upstream", r.forge.git("config", "--get", "receive.hideRefs"))
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
		"http://github.com/hanzo-inc/app":                 true,
		"https://github.com/hanzo-inc/app.git":            true,
		"https://github.com/hanzo-inc/app/":               true,
		"https://github.com/hanzo-inc/app.git/":           true,
		"https://user:token@github.com/hanzo-inc/app.git": true,
		"https://x-access-token@github.com/hanzo-inc/app": true,
		"HTTPS://GitHub.com/Hanzo-Inc/APP":                true,
		"ssh://git@github.com/hanzo-inc/app.git":          true,
		"git@github.com:hanzo-inc/app.git":                true,

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
		" https://github.com/hanzo-inc/app":              false,
		"https://github.com/hanzo-inc/app\n":             false,
		"ssh://git@github.com:2222/hanzo-inc/app":        false,
		"git@evil.com:github.com/hanzo-inc/app.git":      false,
		"git@github.com:evil/../hanzo-inc/app.git":       false,
		"https://github.com/hanzo-inc/app?x=1":           false,
		"https://github.com/hanzo-inc/app#frag":          false,
	} {
		assert.Equal(t, want, IsGitHubRepo(address, "hanzo-inc", "app"), "%q", address)
	}
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
