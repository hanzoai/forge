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

	"github.com/hanzoai/git/models/db"
	git_model "github.com/hanzoai/git/models/git"
	repo_model "github.com/hanzoai/git/models/repo"
	system_model "github.com/hanzoai/git/models/system"
	"github.com/hanzoai/git/models/unittest"
	user_model "github.com/hanzoai/git/models/user"
	"github.com/hanzoai/git/modules/git"
	"github.com/hanzoai/git/modules/gitrepo"
	repo_module "github.com/hanzoai/git/modules/repository"
	"github.com/hanzoai/git/modules/setting"
	"github.com/hanzoai/git/modules/test"
	notify_service "github.com/hanzoai/git/services/notify"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// heard records the pushes the forge was told about, as Actions and webhooks are.
type heard struct {
	notify_service.NullNotifier
	pushes []string // "<ref> <old>..<new>"
	made   []string // refs announced as created
}

var heardNotifier = &heard{}

func init() { notify_service.RegisterNotifier(heardNotifier) }

func (h *heard) SyncPushCommits(_ context.Context, _ *user_model.User, _ *repo_model.Repository, opts *repo_module.PushUpdateOptions, _ *repo_module.PushCommits) {
	h.pushes = append(h.pushes, opts.RefFullName.String()+" "+opts.OldCommitID+".."+opts.NewCommitID)
}

func (h *heard) SyncCreateRef(_ context.Context, _ *user_model.User, _ *repo_model.Repository, refFullName git.RefName, _ string) {
	h.made = append(h.made, refFullName.String())
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
type rig struct {
	t      *testing.T
	repo   *repo_model.Repository
	mirror *repo_model.PushMirror
	forge  *site
	github *site
}

func newRig(t *testing.T) *rig {
	t.Helper()
	unittest.PrepareTestEnv(t)
	// With the queue off, a push to the mirror is dropped rather than sent to a queue this test never starts.
	t.Cleanup(test.MockVariableValue(&setting.Mirror.Enabled, false))
	*heardNotifier = heard{}

	repo := unittest.AssertExistsAndLoadBean(t, &repo_model.Repository{ID: 6})
	r := &rig{
		t:      t,
		repo:   repo,
		forge:  newSite(t, repo_model.RepoPath(repo.OwnerName, repo.Name)),
		github: newSite(t, filepath.Join(t.TempDir(), "github.git")),
	}
	require.NoError(t, gitrepo.GitRemoteAdd(t.Context(), repo, "upstream", r.github.path, gitrepo.RemoteOptionMirrorPush))
	r.mirror = &repo_model.PushMirror{RepoID: repo.ID, RemoteName: "upstream", RemoteAddress: "https://github.com/user10/repo6", SyncOnCommit: true}
	require.NoError(t, db.Insert(t.Context(), r.mirror))
	return r
}

func (r *rig) sync() bool {
	r.t.Helper()
	return SyncInboundMirror(r.t.Context(), r.mirror.ID)
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

func TestInboundCreatesAbsentRefs(t *testing.T) {
	r := newRig(t)
	c1 := r.github.commit("", "one")
	c2 := r.github.commit(c1, "two")
	r.github.set("refs/heads/main", c2)
	r.github.set("refs/heads/topic", c1)
	r.github.git("tag", "light", c1)
	r.github.git("tag", "-a", "-m", "annotated", "v1", c2)

	require.True(t, r.sync())

	assert.Equal(t, c2, r.forge.tip("refs/heads/main"))
	assert.Equal(t, c1, r.forge.tip("refs/heads/topic"))
	assert.Equal(t, c1, r.forge.tip("refs/tags/light"))
	assert.Equal(t, r.github.tip("refs/tags/v1"), r.forge.tip("refs/tags/v1"))

	// The forge sees them as pushes: branch table, releases, and the empty repository's default branch.
	unittest.AssertExistsAndLoadBean(t, &git_model.Branch{RepoID: r.repo.ID, Name: "main", CommitID: c2})
	unittest.AssertExistsAndLoadBean(t, &git_model.Branch{RepoID: r.repo.ID, Name: "topic", CommitID: c1})
	unittest.AssertExistsAndLoadBean(t, &repo_model.Release{RepoID: r.repo.ID, TagName: "v1"})
	repo := unittest.AssertExistsAndLoadBean(t, &repo_model.Repository{ID: r.repo.ID})
	assert.False(t, repo.IsEmpty)
	assert.Equal(t, "main", repo.DefaultBranch)
	assert.Empty(t, r.notices())

	// ...and are announced as pushes, which is what starts Actions and the push mirror.
	assert.ElementsMatch(t, []string{"refs/heads/main", "refs/heads/topic", "refs/tags/light", "refs/tags/v1"}, heardNotifier.made)
	assert.Contains(t, heardNotifier.pushes, "refs/heads/main "+strings.Repeat("0", 40)+".."+c2)
}

func TestInboundFastForwards(t *testing.T) {
	r := newRig(t)
	c1 := r.github.commit("", "one")
	r.github.set("refs/heads/main", c1)
	require.True(t, r.sync())
	require.Equal(t, c1, r.forge.tip("refs/heads/main"))

	*heardNotifier = heard{}

	c2 := r.github.commit(c1, "two")
	r.github.set("refs/heads/main", c2)
	require.True(t, r.sync())

	assert.Equal(t, c2, r.forge.tip("refs/heads/main"))
	unittest.AssertExistsAndLoadBean(t, &git_model.Branch{RepoID: r.repo.ID, Name: "main", CommitID: c2})
	assert.Empty(t, r.notices())
	assert.Equal(t, []string{"refs/heads/main " + c1 + ".." + c2}, heardNotifier.pushes)
	assert.Empty(t, heardNotifier.made)
}

func TestInboundEqualIsNoop(t *testing.T) {
	r := newRig(t)
	c1 := r.github.commit("", "one")
	r.github.set("refs/heads/main", c1)
	r.github.git("tag", "v1", c1)
	require.True(t, r.sync())

	*heardNotifier = heard{}

	require.True(t, r.sync())
	assert.Empty(t, heardNotifier.pushes, "nothing changed, so nothing is announced")
	assert.Empty(t, heardNotifier.made)
	assert.Equal(t, c1, r.forge.tip("refs/heads/main"))
	assert.Empty(t, r.notices())
}

func TestInboundDivergedIsLeftAndReported(t *testing.T) {
	r := newRig(t)
	c1 := r.github.commit("", "one")
	r.github.set("refs/heads/main", c1)
	require.True(t, r.sync())

	// Each side takes a commit the other lacks.
	mine := r.forge.commit(c1, "forge only")
	r.forge.set("refs/heads/main", mine)
	theirs := r.github.commit(c1, "github only")
	r.github.set("refs/heads/main", theirs)

	require.True(t, r.sync())

	assert.Equal(t, mine, r.forge.tip("refs/heads/main"), "a diverged ref is never overwritten")
	assert.Equal(t, theirs, r.forge.tip("refs/upstream/heads/main"), "the upstream tip is kept for a person to compare")
	require.Len(t, r.notices(), 1)
	assert.Contains(t, r.notices()[0], r.repo.FullName())
	assert.Contains(t, r.notices()[0], "refs/heads/main")
}

func TestInboundKeepsRefAheadOfUpstream(t *testing.T) {
	r := newRig(t)
	c1 := r.github.commit("", "one")
	r.github.set("refs/heads/main", c1)
	require.True(t, r.sync())

	// A push to the forge the push mirror has not carried out yet.
	c2 := r.forge.commit(c1, "forge only")
	r.forge.set("refs/heads/main", c2)

	require.True(t, r.sync())

	assert.Equal(t, c2, r.forge.tip("refs/heads/main"))
	assert.Empty(t, r.notices(), "an upstream that has not caught up is not a divergence")
}

func TestInboundKeepsRefUpstreamLacks(t *testing.T) {
	r := newRig(t)
	c1 := r.github.commit("", "one")
	r.github.set("refs/heads/main", c1)
	r.github.set("refs/heads/gone", c1)
	r.github.git("tag", "v1", c1)
	require.True(t, r.sync())
	require.Equal(t, c1, r.forge.tip("refs/heads/gone"))

	r.github.delete("refs/heads/gone")
	r.github.delete("refs/tags/v1")
	require.True(t, r.sync())

	assert.Equal(t, c1, r.forge.tip("refs/heads/gone"), "a ref the upstream dropped is not dropped here")
	assert.Equal(t, c1, r.forge.tip("refs/tags/v1"))
	assert.Empty(t, r.forge.tip("refs/upstream/heads/gone"), "the private namespace follows the upstream")
	assert.Empty(t, r.notices())
}

func TestInboundDivergedTagIsLeft(t *testing.T) {
	r := newRig(t)
	c1 := r.github.commit("", "one")
	r.github.set("refs/heads/main", c1)
	r.github.git("tag", "v1", c1)
	require.True(t, r.sync())

	// The upstream retags v1 onto history the forge does not contain.
	other := r.github.commit("", "elsewhere")
	r.github.git("tag", "-f", "v1", other)
	require.True(t, r.sync())

	assert.Equal(t, c1, r.forge.tip("refs/tags/v1"))
	require.Len(t, r.notices(), 1)
	assert.Contains(t, r.notices()[0], "refs/tags/v1")
}

func TestInboundFailsOnUnreachableRemote(t *testing.T) {
	r := newRig(t)
	require.NoError(t, os.RemoveAll(r.github.path))

	assert.False(t, r.sync())
	require.Len(t, r.notices(), 1)
	assert.Contains(t, r.notices()[0], r.repo.FullName())
}
