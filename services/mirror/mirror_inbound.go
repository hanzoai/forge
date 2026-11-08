// Copyright 2026 Hanzo AI, Inc. All rights reserved.
// SPDX-License-Identifier: MIT

package mirror

import (
	"context"
	"fmt"
	"maps"
	"slices"
	"strings"
	"time"

	"github.com/hanzoai/git/models/db"
	repo_model "github.com/hanzoai/git/models/repo"
	system_model "github.com/hanzoai/git/models/system"
	"github.com/hanzoai/git/modules/git"
	"github.com/hanzoai/git/modules/git/gitcmd"
	"github.com/hanzoai/git/modules/gitrepo"
	"github.com/hanzoai/git/modules/globallock"
	"github.com/hanzoai/git/modules/log"
	"github.com/hanzoai/git/modules/process"
	"github.com/hanzoai/git/modules/proxy"
	repo_module "github.com/hanzoai/git/modules/repository"
	"github.com/hanzoai/git/modules/setting"
	"github.com/hanzoai/git/modules/util"
)

// upstreamPrefix is the namespace an inbound fetch writes the remote's branches
// and tags into. Nothing else writes there, so the fetch may force it.
const upstreamPrefix = "refs/upstream/"

// SyncInboundMirror fast-forwards the repository from the remote of push mirror
// mirrorID, for a repository with no pull mirror whose remote took a push of its own.
// Branches and tags the remote has and the repository lacks are created, and ones
// the repository holds an ancestor of are advanced. Everything else is left as it is:
// a diverged ref is reported, a ref the remote lacks is kept, and nothing is forced.
// What changed is announced as pushes, which carries it back out through the push mirror.
func SyncInboundMirror(ctx context.Context, mirrorID int64) bool {
	log.Trace("SyncInboundMirror [mirror: %d]", mirrorID)
	defer func() {
		err := recover()
		if err == nil {
			return
		}
		log.Error("PANIC whilst SyncInboundMirror[%d] Panic: %v\nStacktrace: %s", mirrorID, err, log.Stack(2))
	}()

	m, exist, err := db.GetByID[repo_model.PushMirror](ctx, mirrorID)
	if err != nil || !exist {
		log.Error("SyncInboundMirror [mirror: %d]: push mirror not found: %v", mirrorID, err)
		return false
	}
	if m.GetRepository(ctx) == nil {
		return false
	}

	releaser, err := globallock.Lock(ctx, getRepoMirrorLockKey(m.RepoID))
	if err != nil {
		log.Error("globallock.Lock(): %v", err)
		return false
	}
	defer releaser()

	ctx, _, finished := process.GetManager().AddContext(ctx, fmt.Sprintf("Fast-forwarding %s/%s from %s", m.Repo.OwnerName, m.Repo.Name, m.RemoteName))
	defer finished()

	results, ok := runInbound(ctx, m)
	if !ok {
		return false
	}
	if !announce(ctx, m.Repo, results) {
		return false
	}
	log.Trace("SyncInboundMirror [mirror: %d][repo: %-v]: %d refs updated", m.ID, m.Repo, len(results))
	return true
}

// runInbound fetches the remote of m and advances the refs of its repository.
// It returns true if it finished without error.
func runInbound(ctx context.Context, m *repo_model.PushMirror) ([]*repo_module.SyncResult, bool) {
	repo := m.Repo
	remoteURL, ok := fetchableRemote(ctx, repo, m.RemoteName)
	if !ok {
		return nil, false
	}

	// Never follow HTTP redirects, see runSync.
	fetch := gitcmd.NewCommand("fetch", "--no-tags", "--prune").AddConfig("http.followRedirects", "false").
		AddDynamicArguments(m.RemoteName, "+refs/heads/*:"+upstreamPrefix+"heads/*", "+refs/tags/*:"+upstreamPrefix+"tags/*").
		WithTimeout(time.Duration(setting.Git.Timeout.Mirror) * time.Second).
		WithEnv(proxy.EnvWithProxy(remoteURL.URL))
	if _, stderr, err := gitrepo.RunCmdString(ctx, repo, fetch); err != nil {
		// sanitize the output, since it may contain the remote address, which may contain a password
		stderrMessage := util.SanitizeCredentialURLs(stderr)
		log.Error("SyncInboundMirror [repo: %-v]: failed to fetch %s:\nStderr: %s\nErr: %v", repo, m.RemoteName, stderrMessage, err)
		if err := system_model.CreateRepositoryNotice("Failed to fetch %s into %s: %s", m.RemoteName, repo.FullName(), stderrMessage); err != nil {
			log.Error("CreateRepositoryNotice: %v", err)
		}
		return nil, false
	}

	if err := advance(ctx, repo); err != nil {
		log.Error("SyncInboundMirror [repo: %-v]: %v", repo, err)
		return nil, false
	}
	if err := gitrepo.WriteCommitGraph(ctx, repo); err != nil {
		log.Error("SyncInboundMirror [repo: %-v]: %v", repo, err)
	}

	gitRepo, err := gitrepo.OpenRepository(ctx, repo)
	if err != nil {
		log.Error("SyncInboundMirror [repo: %-v]: failed to OpenRepository: %v", repo, err)
		return nil, false
	}
	results := reconcile(ctx, repo, gitRepo)
	gitRepo.Close()

	if err := dropBranchCaches(ctx, repo); err != nil {
		log.Error("SyncInboundMirror [repo: %-v]: failed to GetBranches: %v", repo, err)
		return nil, false
	}
	return results, true
}

// advance moves each branch and tag of repo to the tip the remote holds for it
// in the upstream namespace, when that only adds history.
func advance(ctx context.Context, repo *repo_model.Repository) error {
	stdout, _, err := gitrepo.RunCmdString(ctx, repo, gitcmd.NewCommand("for-each-ref",
		"--format=%(objectname) %(refname)", "refs/heads/", "refs/tags/", upstreamPrefix))
	if err != nil {
		return fmt.Errorf("for-each-ref: %w", err)
	}
	tips := map[string]string{}
	for line := range strings.SplitSeq(stdout, "\n") {
		if id, name, ok := strings.Cut(line, " "); ok {
			tips[name] = id
		}
	}

	zero := git.ObjectFormatFromName(repo.ObjectFormatName).EmptyObjectID().String()
	for _, name := range slices.Sorted(maps.Keys(tips)) {
		short, ok := strings.CutPrefix(name, upstreamPrefix)
		if !ok {
			continue
		}
		ref, tip := "refs/"+short, tips[name]
		old, have := tips[ref]
		var err error
		switch {
		case !have:
			err = gitrepo.SwapRef(ctx, repo, ref, tip, zero)
		case old != tip:
			err = moveRef(ctx, repo, ref, old, tip)
		}
		if err != nil {
			log.Warn("SyncInboundMirror [repo: %-v]: %s: %v", repo, ref, err)
		}
	}
	return nil
}

// moveRef moves ref from old to tip when old is an ancestor of tip. A ref ahead
// of tip is left, since the remote has only not caught up; a diverged one is
// left and reported.
func moveRef(ctx context.Context, repo *repo_model.Repository, ref, old, tip string) error {
	forward, err := gitrepo.IsAncestor(ctx, repo, old, tip)
	if err != nil {
		return err
	}
	if forward {
		return gitrepo.SwapRef(ctx, repo, ref, tip, old)
	}
	behind, err := gitrepo.IsAncestor(ctx, repo, tip, old)
	if err != nil {
		return err
	}
	if !behind {
		log.Warn("SyncInboundMirror [repo: %-v]: %s diverged from upstream, left untouched", repo, ref)
		if err := system_model.CreateRepositoryNotice("Inbound sync of %s left %s untouched: it diverged from upstream", repo.FullName(), ref); err != nil {
			log.Error("CreateRepositoryNotice: %v", err)
		}
	}
	return nil
}
