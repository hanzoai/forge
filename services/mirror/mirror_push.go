// Copyright 2021 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package mirror

import (
	"context"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strings"
	"time"

	"github.com/hanzoai/git/models/db"
	repo_model "github.com/hanzoai/git/models/repo"
	"github.com/hanzoai/git/modules/git"
	giturl "github.com/hanzoai/git/modules/git/url"
	"github.com/hanzoai/git/modules/gitrepo"
	"github.com/hanzoai/git/modules/lfs"
	"github.com/hanzoai/git/modules/log"
	"github.com/hanzoai/git/modules/process"
	"github.com/hanzoai/git/modules/proxy"
	"github.com/hanzoai/git/modules/repository"
	"github.com/hanzoai/git/modules/setting"
	"github.com/hanzoai/git/modules/timeutil"
	"github.com/hanzoai/git/modules/util"
	"github.com/hanzoai/git/services/migrations"
	repo_service "github.com/hanzoai/git/services/repository"
)

var stripExitStatus = regexp.MustCompile(`exit status \d+ - `)

// sameRemote reports whether two remote addresses name the same repository,
// ignoring credentials, scheme and a trailing .git — the forms the same target
// is spelled in.
func sameRemote(a, b string) bool {
	parse := func(s string) (string, bool) {
		u, err := giturl.ParseGitURL(s)
		if err != nil || u.Host == "" {
			return "", false
		}
		return strings.ToLower(u.Host + "/" + strings.Trim(strings.TrimSuffix(u.Path, ".git"), "/")), true
	}
	ka, oka := parse(a)
	kb, okb := parse(b)
	return oka && okb && ka == kb
}

// refuseUpstreamCycle rejects a push mirror that targets the repo's own pull
// upstream. Both directions are force-mirrors with no merge step, so a cycle is
// a clobber race: each side overwrites the other with whatever it last saw and
// the loser's commits are gone. The illegal state is refused rather than
// documented.
func refuseUpstreamCycle(ctx context.Context, repo *repo_model.Repository, addr string) error {
	if !repo.IsMirror {
		return nil
	}
	pullMirror, err := repo_model.GetMirrorByRepoID(ctx, repo.ID)
	if err != nil {
		return nil // no upstream to cycle with
	}
	upstream, err := gitrepo.GitRemoteGetURL(ctx, repo, pullMirror.GetRemoteName())
	if err != nil {
		return nil
	}
	if sameRemote(upstream.String(), addr) {
		return util.NewInvalidArgumentErrorf("push mirror target is this repository's pull upstream: mirroring both ways force-overwrites in both directions and loses commits")
	}
	return nil
}

// AddPushMirrorRemote registers the push mirror remote.
func AddPushMirrorRemote(ctx context.Context, m *repo_model.PushMirror, addr string) error {
	if err := refuseUpstreamCycle(ctx, m.Repo, addr); err != nil {
		return err
	}

	addRemoteAndConfig := func(storageRepo gitrepo.Repository, addr string) error {
		if err := gitrepo.GitRemoteAdd(ctx, storageRepo, m.RemoteName, addr, gitrepo.RemoteOptionMirrorPush); err != nil {
			return err
		}
		if err := gitrepo.GitConfigAdd(ctx, storageRepo, "remote."+m.RemoteName+".push", "+refs/heads/*:refs/heads/*"); err != nil {
			return err
		}
		return gitrepo.GitConfigAdd(ctx, storageRepo, "remote."+m.RemoteName+".push", "+refs/tags/*:refs/tags/*")
	}

	if err := addRemoteAndConfig(m.Repo, addr); err != nil {
		return err
	}

	if repo_service.HasWiki(ctx, m.Repo) {
		wikiRemoteURL := repository.WikiRemoteURL(ctx, addr)
		if len(wikiRemoteURL) > 0 {
			if err := addRemoteAndConfig(m.Repo.WikiStorageRepo(), wikiRemoteURL); err != nil {
				return err
			}
		}
	}

	return nil
}

// RemovePushMirrorRemote removes the push mirror remote.
func RemovePushMirrorRemote(ctx context.Context, m *repo_model.PushMirror) error {
	_ = m.GetRepository(ctx)
	if err := gitrepo.GitRemoteRemove(ctx, m.Repo, m.RemoteName); err != nil {
		return err
	}

	if repo_service.HasWiki(ctx, m.Repo) {
		if err := gitrepo.GitRemoteRemove(ctx, m.Repo.WikiStorageRepo(), m.RemoteName); err != nil {
			// The wiki remote may not exist
			log.Warn("Wiki Remote[%d] could not be removed: %v", m.ID, err)
		}
	}

	return nil
}

// SyncPushMirror starts the sync of the push mirror and schedules the next run.
func SyncPushMirror(ctx context.Context, mirrorID int64) bool {
	log.Trace("SyncPushMirror [mirror: %d]", mirrorID)
	defer func() {
		err := recover()
		if err == nil {
			return
		}
		// There was a panic whilst syncPushMirror...
		log.Error("PANIC whilst syncPushMirror[%d] Panic: %v\nStacktrace: %s", mirrorID, err, log.Stack(2))
	}()

	// TODO: Handle "!exist" better
	m, exist, err := db.GetByID[repo_model.PushMirror](ctx, mirrorID)
	if err != nil || !exist {
		log.Error("GetPushMirrorByID [%d]: %v", mirrorID, err)
		return false
	}

	_ = m.GetRepository(ctx)

	m.LastError = ""

	ctx, _, finished := process.GetManager().AddContext(ctx, fmt.Sprintf("Syncing PushMirror %s/%s to %s", m.Repo.OwnerName, m.Repo.Name, m.RemoteName))
	defer finished()

	log.Trace("SyncPushMirror [mirror: %d][repo: %-v]: Running Sync", m.ID, m.Repo)
	err = runPushSync(ctx, m)
	if err != nil {
		log.Error("SyncPushMirror [mirror: %d][repo: %-v]: %v", m.ID, m.Repo, err)
		m.LastError = stripExitStatus.ReplaceAllLiteralString(err.Error(), "")
	}

	m.LastUpdateUnix = timeutil.TimeStampNow()

	if err := repo_model.UpdatePushMirror(ctx, m); err != nil {
		log.Error("UpdatePushMirror [%d]: %v", m.ID, err)

		return false
	}

	log.Trace("SyncPushMirror [mirror: %d][repo: %-v]: Finished", m.ID, m.Repo)

	return err == nil
}

func runPushSync(ctx context.Context, m *repo_model.PushMirror) error {
	timeout := time.Duration(setting.Git.Timeout.Mirror) * time.Second

	performPush := func(repo *repo_model.Repository, isWiki bool) error {
		var storageRepo gitrepo.Repository = repo
		if isWiki {
			storageRepo = repo.WikiStorageRepo()
		}
		remoteURL, err := gitrepo.GitRemoteGetURL(ctx, storageRepo, m.RemoteName)
		if err != nil {
			log.Error("GetRemoteURL(%s) Error %v", storageRepo.RelativePath(), err)
			return errors.New("Unexpected error")
		}

		if setting.LFS.StartServer {
			log.Trace("SyncMirrors [repo: %-v]: syncing LFS objects...", m.Repo)

			gitRepo, err := gitrepo.OpenRepository(ctx, storageRepo)
			if err != nil {
				log.Error("OpenRepository: %v", err)
				return errors.New("Unexpected error")
			}
			defer gitRepo.Close()

			lfsClient, err := lfs.NewClientFromEndpoint(remoteURL.String(), "", migrations.NewMigrationHTTPTransport())
			if err != nil {
				return err
			}
			if err := pushAllLFSObjects(ctx, gitRepo, lfsClient); err != nil {
				return util.SanitizeErrorCredentialURLs(err)
			}
		}

		log.Trace("Pushing %s mirror[%d] remote %s", storageRepo.RelativePath(), m.ID, m.RemoteName)

		envs := proxy.EnvWithProxy(remoteURL.URL)
		if err := gitrepo.PushToExternal(ctx, storageRepo, git.PushOptions{
			Remote:  m.RemoteName,
			Force:   true,
			Mirror:  true,
			Timeout: timeout,
			Env:     envs,
		}); err != nil {
			log.Error("Error pushing %s mirror[%d] remote %s: %v", storageRepo.RelativePath(), m.ID, m.RemoteName, err)

			return util.SanitizeErrorCredentialURLs(err)
		}

		return nil
	}

	err := performPush(m.Repo, false)
	if err != nil {
		return err
	}

	if repo_service.HasWiki(ctx, m.Repo) {
		if _, err := gitrepo.GitRemoteGetURL(ctx, m.Repo.WikiStorageRepo(), m.RemoteName); err == nil {
			err := performPush(m.Repo, true)
			if err != nil {
				return err
			}
		} else if !errors.Is(err, util.ErrNotExist) {
			log.Error("GetRemote of wiki failed: %v", err)
		}
	}

	return nil
}

func pushAllLFSObjects(ctx context.Context, gitRepo *git.Repository, lfsClient lfs.Client) error {
	contentStore := lfs.NewContentStore()

	pointerChan := make(chan lfs.PointerBlob)
	errChan := make(chan error, 1)
	go func() {
		errChan <- lfs.SearchPointerBlobs(ctx, gitRepo, pointerChan)
	}()

	uploadObjects := func(pointers []lfs.Pointer) error {
		err := lfsClient.Upload(ctx, pointers, func(p lfs.Pointer, objectError error) (io.ReadCloser, error) {
			if objectError != nil {
				return nil, objectError
			}

			content, err := contentStore.Get(p)
			if err != nil {
				log.Error("Error reading LFS object %v: %v", p, err)
			}
			return content, err
		})
		if err != nil {
			select {
			case <-ctx.Done():
				return nil
			default:
			}
		}
		return err
	}

	var batch []lfs.Pointer
	for pointerBlob := range pointerChan {
		exists, err := contentStore.Exists(pointerBlob.Pointer)
		if err != nil {
			log.Error("Error checking if LFS object %v exists: %v", pointerBlob.Pointer, err)
			return err
		}
		if !exists {
			log.Trace("Skipping missing LFS object %v", pointerBlob.Pointer)
			continue
		}

		batch = append(batch, pointerBlob.Pointer)
		if len(batch) >= lfsClient.BatchSize() {
			if err := uploadObjects(batch); err != nil {
				return err
			}
			batch = nil
		}
	}
	if len(batch) > 0 {
		if err := uploadObjects(batch); err != nil {
			return err
		}
	}

	err := <-errChan
	if err != nil {
		log.Error("Error enumerating LFS objects for repository: %v", err)
	}

	return err
}

func syncPushMirrorWithSyncOnCommit(ctx context.Context, repoID int64) {
	pushMirrors, err := repo_model.GetPushMirrorsSyncedOnCommit(ctx, repoID)
	if err != nil {
		log.Error("repo_model.GetPushMirrorsSyncedOnCommit failed: %v", err)
		return
	}

	for _, mirror := range pushMirrors {
		AddPushMirrorToQueue(mirror.ID)
	}
}
