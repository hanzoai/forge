// Copyright 2026 Hanzo AI, Inc. All rights reserved.
// SPDX-License-Identifier: MIT

package mirror

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"
	"time"

	"github.com/hanzoai/git/models/db"
	access_model "github.com/hanzoai/git/models/perm/access"
	repo_model "github.com/hanzoai/git/models/repo"
	system_model "github.com/hanzoai/git/models/system"
	"github.com/hanzoai/git/models/unit"
	user_model "github.com/hanzoai/git/models/user"
	"github.com/hanzoai/git/modules/git"
	"github.com/hanzoai/git/modules/git/gitcmd"
	giturl "github.com/hanzoai/git/modules/git/url"
	"github.com/hanzoai/git/modules/gitrepo"
	"github.com/hanzoai/git/modules/globallock"
	"github.com/hanzoai/git/modules/log"
	"github.com/hanzoai/git/modules/process"
	"github.com/hanzoai/git/modules/proxy"
	repo_module "github.com/hanzoai/git/modules/repository"
	"github.com/hanzoai/git/modules/setting"
	"github.com/hanzoai/git/modules/util"
)

// IsGitHubRepo reports whether address names github.com/ownerName/repoName, in
// any letter case, with or without credentials, a trailing ".git" or "/".
// Anything else is no: another host, a port, a query or fragment, extra or
// encoded path segments.
func IsGitHubRepo(address, ownerName, repoName string) bool {
	u, err := giturl.ParseGitURL(address)
	if err != nil || !strings.EqualFold(u.Hostname(), "github.com") || u.Port() != "" {
		return false
	}
	switch u.Scheme {
	case "http", "https", "ssh", "git":
	default:
		return false
	}
	if u.RawQuery != "" || u.ForceQuery || u.Fragment != "" {
		return false
	}
	path := strings.TrimPrefix(u.EscapedPath(), "/")
	path = strings.TrimSuffix(strings.TrimSuffix(path, "/"), ".git")
	return strings.EqualFold(path, ownerName+"/"+repoName)
}

// isRegular reports whether repo has no pull mirror, and so no upstream of its own to fetch from.
func isRegular(ctx context.Context, repo *repo_model.Repository) bool {
	_, err := repo_model.GetMirrorByRepoID(ctx, repo.ID)
	return errors.Is(err, repo_model.ErrMirrorNotExist)
}

// GitHubPushMirror returns the push mirror of the regular repo whose remote is
// the GitHub repository of the same owner and name, or nil.
func GitHubPushMirror(ctx context.Context, repo *repo_model.Repository) *repo_model.PushMirror {
	if !isRegular(ctx, repo) {
		return nil
	}
	mirrors, _, err := repo_model.GetPushMirrorsByRepoID(ctx, repo.ID, db.ListOptionsAll)
	if err != nil {
		log.Error("GitHubPushMirror: unable to list push mirrors of %s: %v", repo.FullName(), err)
		return nil
	}
	for _, m := range mirrors {
		if IsGitHubRepo(m.RemoteAddress, repo.OwnerName, repo.Name) {
			return m
		}
	}
	return nil
}

// SyncInboundMirror brings what the GitHub remote of push mirror mirrorID gained
// into the repository, as pushes by the [mirror] SYNC_USER. Going through the
// push path means the pre-receive hook enforces what it does for anyone's push
// (protected branches and tags, approvals, status checks, signatures, quota),
// and the post-receive hook starts Actions, webhooks, pull request checks and
// the push out to GitHub, as for a person's push.
//
// Only refs whose upstream tip changed since the last sync are looked at, and
// only to add history: an absent ref is created, a branch the forge holds an
// ancestor of is fast-forwarded. A tag is never moved, a diverged ref is never
// forced, and nothing is deleted because upstream lacks it. A ref the forge would
// not take is reported in a notice, once, and looked at again on the next sync.
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
	repoID := m.RepoID

	// The same lock as a pull: both move refs by fetching.
	releaser, err := globallock.Lock(ctx, getRepoPullMirrorLockKey(repoID))
	if err != nil {
		log.Error("globallock.Lock(): %v", err)
		return false
	}
	defer releaser()

	// What the queue item saw may be stale by now, so look again under the lock.
	m, exist, err = db.GetByID[repo_model.PushMirror](ctx, mirrorID)
	if err != nil || !exist {
		log.Error("SyncInboundMirror [mirror: %d]: push mirror gone: %v", mirrorID, err)
		return false
	}
	repo, err := repo_model.GetRepositoryByID(ctx, repoID)
	if err != nil {
		log.Error("SyncInboundMirror [mirror: %d]: %v", mirrorID, err)
		return false
	}
	user, ok := inboundUser(ctx, m, repo)
	if !ok {
		return false
	}

	ctx, _, finished := process.GetManager().AddContext(ctx, fmt.Sprintf("Fast-forwarding %s from %s", repo.FullName(), m.RemoteName))
	defer finished()

	return runInbound(ctx, m, repo, user)
}

// inboundUser returns the user the sync pushes as, or false if the repository is
// not one the inbound sync may write to.
func inboundUser(ctx context.Context, m *repo_model.PushMirror, repo *repo_model.Repository) (*user_model.User, bool) {
	if repo.IsArchived {
		log.Trace("SyncInboundMirror [repo: %-v]: archived, skipped", repo)
		return nil, false
	}
	if !isRegular(ctx, repo) || !IsGitHubRepo(m.RemoteAddress, repo.OwnerName, repo.Name) {
		log.Trace("SyncInboundMirror [repo: %-v]: no longer a regular repository mirrored to GitHub, skipped", repo)
		return nil, false
	}
	if setting.Mirror.SyncUser == "" {
		log.Warn("SyncInboundMirror [repo: %-v]: [mirror] SYNC_USER is not set, skipped", repo)
		return nil, false
	}
	user, err := user_model.GetUserByName(ctx, setting.Mirror.SyncUser)
	if err != nil {
		log.Error("SyncInboundMirror [repo: %-v]: sync user %q: %v", repo, setting.Mirror.SyncUser, err)
		return nil, false
	}
	perm, err := access_model.GetDoerRepoPermission(ctx, repo, user)
	if err != nil {
		log.Error("SyncInboundMirror [repo: %-v]: permission of %s: %v", repo, user.Name, err)
		return nil, false
	}
	if !perm.CanWrite(unit.TypeCode) {
		notify(fmt.Sprintf("Inbound sync of %s is off: sync user %s cannot write to it", repo.FullName(), user.Name))
		return nil, false
	}
	return user, true
}

// notify reports a condition in a notice, once for as long as the notice stays.
func notify(desc string) {
	log.Warn("%s", desc)
	if err := system_model.CreateRepositoryNoticeOnce(desc); err != nil {
		log.Error("CreateRepositoryNoticeOnce: %v", err)
	}
}

// runInbound fetches the remote of m and pushes what it gained. It returns true
// if it finished, whatever it left alone.
func runInbound(ctx context.Context, m *repo_model.PushMirror, repo *repo_model.Repository, user *user_model.User) bool {
	remoteURL, ok := fetchableRemote(ctx, repo, m.RemoteName)
	if !ok {
		return false
	}

	// Hidden from clients, and from pushes, so only this sync writes there.
	for _, key := range []string{"uploadpack.hideRefs", "receive.hideRefs"} {
		if err := gitrepo.GitConfigSet(ctx, repo, key, strings.TrimSuffix(git.UpstreamPrefix, "/")); err != nil {
			log.Error("SyncInboundMirror [repo: %-v]: %s: %v", repo, key, err)
			return false
		}
	}

	before, err := upstreamTips(ctx, repo)
	if err != nil {
		log.Error("SyncInboundMirror [repo: %-v]: %v", repo, err)
		return false
	}

	// Never follow HTTP redirects, see runSync. The namespace is ours, so forcing it is safe.
	fetch := gitcmd.NewCommand("fetch", "--no-tags", "--prune").AddConfig("http.followRedirects", "false").
		AddDynamicArguments(m.RemoteName,
			"+"+git.BranchPrefix+"*:"+git.UpstreamPrefix+"heads/*",
			"+"+git.TagPrefix+"*:"+git.UpstreamPrefix+"tags/*").
		WithTimeout(time.Duration(setting.Git.Timeout.Mirror) * time.Second).
		WithEnv(proxy.EnvWithProxy(remoteURL.URL))
	if _, stderr, err := gitrepo.RunCmdString(ctx, repo, fetch); err != nil {
		// sanitize the output, since it may contain the remote address, which may contain a password
		stderrMessage := util.SanitizeCredentialURLs(stderr)
		log.Error("SyncInboundMirror [repo: %-v]: failed to fetch %s:\nStderr: %s\nErr: %v", repo, m.RemoteName, stderrMessage, util.SanitizeErrorCredentialURLs(err))
		notify(fmt.Sprintf("Inbound sync of %s failed to fetch %s: %s", repo.FullName(), m.RemoteName, stderrMessage))
		return false
	}

	after, err := upstreamTips(ctx, repo)
	if err != nil {
		log.Error("SyncInboundMirror [repo: %-v]: %v", repo, err)
		return false
	}
	here, err := forgeTips(ctx, repo)
	if err != nil {
		log.Error("SyncInboundMirror [repo: %-v]: %v", repo, err)
		return false
	}

	// The first sync of a repository that already has content only records
	// where upstream stands. GitHub keeps every branch the forge ever deleted,
	// since the push out never deletes, so creating whatever the forge lacks
	// would bring them all back at once.
	create := len(before) > 0 || repo.IsEmpty

	for _, name := range slices.Sorted(maps.Keys(after)) {
		if before[name] == after[name] {
			continue
		}
		ref := "refs/" + strings.TrimPrefix(name, git.UpstreamPrefix)
		if err := advance(ctx, repo, user, name, ref, after[name], here[ref], create); err != nil {
			// Already reported where it was found. Forget that this tip was seen, so the next sync looks at it again.
			log.Trace("SyncInboundMirror [repo: %-v]: %s: %v", repo, ref, err)
			forget(ctx, repo, name, before)
		}
	}
	return true
}

// upstreamTips returns where the fetched branches and tags stood, by ref name.
// Anything else under the namespace is ignored: it was not put there by a fetch.
func upstreamTips(ctx context.Context, repo *repo_model.Repository) (map[string]string, error) {
	return listTips(ctx, repo, git.UpstreamPrefix+"heads/", git.UpstreamPrefix+"tags/")
}

// forgeTips returns where the repository's own branches and tags stand, by ref name.
func forgeTips(ctx context.Context, repo *repo_model.Repository) (map[string]string, error) {
	return listTips(ctx, repo, git.BranchPrefix, git.TagPrefix)
}

func listTips(ctx context.Context, repo *repo_model.Repository, prefixes ...string) (map[string]string, error) {
	stdout, _, err := gitrepo.RunCmdString(ctx, repo, gitcmd.NewCommand("for-each-ref",
		"--format=%(objectname) %(refname)").AddDynamicArguments(prefixes...))
	if err != nil {
		return nil, fmt.Errorf("for-each-ref: %w", err)
	}
	tips := map[string]string{}
	for line := range strings.SplitSeq(stdout, "\n") {
		if id, name, ok := strings.Cut(line, " "); ok {
			tips[name] = id
		}
	}
	return tips, nil
}

// advance pushes the upstream tip at name to ref as user, when that only adds
// history. old is where ref stands now, empty if it does not exist. It returns
// an error for a ref it could not or would not move, after reporting it.
func advance(ctx context.Context, repo *repo_model.Repository, user *user_model.User, name, ref, tip, old string, create bool) error {
	switch {
	case old == tip:
		return nil
	case old == "":
		if !create {
			return nil
		}
	case strings.HasPrefix(ref, git.TagPrefix):
		// Release tags are the record of what shipped. Even a tag the git push
		// would move is not ours to move.
		desc := fmt.Sprintf("Inbound sync of %s left %s untouched: the forge has it at %s, upstream at %s", repo.FullName(), ref, short(old), short(tip))
		notify(desc)
		return errors.New("tag differs")
	default:
		forward, err := gitrepo.IsAncestor(ctx, repo, old, tip)
		if err != nil {
			log.Error("SyncInboundMirror [repo: %-v]: %s: %v", repo, ref, err)
			return err
		}
		if !forward {
			behind, err := gitrepo.IsAncestor(ctx, repo, tip, old)
			if err != nil {
				log.Error("SyncInboundMirror [repo: %-v]: %s: %v", repo, ref, err)
				return err
			}
			if behind {
				return nil // upstream has only not caught up
			}
			notify(fmt.Sprintf("Inbound sync of %s left %s untouched: the forge at %s and upstream at %s have diverged", repo.FullName(), ref, short(old), short(tip)))
			return errors.New("diverged")
		}
	}

	// Not forced: the receiving side refuses anything but an addition, as well as
	// whatever its hooks refuse.
	if err := gitrepo.Push(ctx, repo, repo, git.PushOptions{
		Branch:  name + ":" + ref,
		Env:     repo_module.PushingEnvironment(user, repo),
		Timeout: time.Duration(setting.Git.Timeout.Mirror) * time.Second,
	}); err != nil {
		reason := util.SanitizeErrorCredentialURLs(err).Error()
		if rejected, ok := errors.AsType[*git.ErrPushRejected](err); ok {
			reason = rejected.Message // what the hooks said, without the local paths around it
		}
		notify(fmt.Sprintf("Inbound sync of %s could not move %s from %s to %s: %s", repo.FullName(), ref, short(old), short(tip), reason))
		return errors.New("refused")
	}
	return nil
}

// forget puts the upstream ref at name back where it stood before the fetch.
func forget(ctx context.Context, repo *repo_model.Repository, name string, before map[string]string) {
	var err error
	if prev, ok := before[name]; ok {
		err = gitrepo.UpdateRef(ctx, repo, name, prev)
	} else {
		err = gitrepo.RemoveRef(ctx, repo, name)
	}
	if err != nil {
		log.Error("SyncInboundMirror [repo: %-v]: unable to reset %s: %v", repo, name, err)
	}
}

func short(id string) string {
	if len(id) > 12 {
		return id[:12]
	}
	return id
}
