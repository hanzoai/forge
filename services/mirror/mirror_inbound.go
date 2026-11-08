// Copyright 2026 Hanzo AI, Inc. All rights reserved.
// SPDX-License-Identifier: MIT

package mirror

import (
	"context"
	"crypto/sha1"
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
	"github.com/hanzoai/git/modules/cache"
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
	"github.com/hanzoai/git/modules/timeutil"
	"github.com/hanzoai/git/modules/util"
)

const (
	// noticeTTL is how long a reported condition is not reported again. It is a
	// reminder, not a record: what persists past it is reported once more.
	noticeTTL = 7 * 24 * 60 * 60

	// primedKey is the repository setting that says the first inbound sync is done.
	primedKey = "hanzo.inboundprimed"
)

// githubRepoPath returns the "owner/name" that address names on github.com, in any
// letter case, without credentials, a trailing ".git" or "/". Anything else is
// not an address of a GitHub repository: another host, a port, a query or
// fragment, extra or encoded path segments.
func githubRepoPath(address string) (string, bool) {
	u, err := giturl.ParseGitURL(address)
	if err != nil || !strings.EqualFold(u.Hostname(), "github.com") || u.Port() != "" {
		return "", false
	}
	switch u.Scheme {
	case "http", "https", "ssh", "git":
	default:
		return "", false
	}
	if u.RawQuery != "" || u.ForceQuery || u.Fragment != "" {
		return "", false
	}
	path := strings.TrimPrefix(u.EscapedPath(), "/")
	return strings.TrimSuffix(strings.TrimSuffix(path, "/"), ".git"), true
}

// IsGitHubRepo reports whether address names github.com/ownerName/repoName.
func IsGitHubRepo(address, ownerName, repoName string) bool {
	path, ok := githubRepoPath(address)
	return ok && strings.EqualFold(path, ownerName+"/"+repoName)
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
// push path means the pre-receive hook applies what it does to anyone's direct
// push (permission and the protected branch rules for pushes: whitelist,
// signatures, protected files), and the post-receive hook starts Actions,
// webhooks, pull request checks and the push out to GitHub, as for a person's
// push. It does not apply what only gates a merge, nor a quota.
//
// The remote is fetched into a staging namespace, and a ref's upstream tip is
// recorded under refs/upstream only once the forge has dealt with it, so a push
// that was cut short or refused is looked at again. Only refs whose upstream tip
// is not yet recorded are looked at, and only to add history: an absent ref is
// created, a branch the forge holds an ancestor of is fast-forwarded. A tag is
// never moved, a diverged ref is never forced, and nothing is deleted because
// upstream lacks it. A ref the forge would not take is reported in a notice.
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
	if !isRegular(ctx, repo) {
		log.Warn("SyncInboundMirror [repo: %-v]: has a pull mirror now, skipped", repo)
		return nil, false
	}
	if !IsGitHubRepo(m.RemoteAddress, repo.OwnerName, repo.Name) {
		// The repository was renamed here, or its push mirror was pointed elsewhere.
		log.Warn("SyncInboundMirror [repo: %-v]: push mirror %s names %q, not github.com/%s, so inbound sync is off", repo, m.RemoteName, m.RemoteAddress, repo.FullName())
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
		notify(repo, "", "sync user cannot write", fmt.Sprintf("Inbound sync of %s is off: sync user %s cannot write to it", repo.FullName(), user.Name))
		return nil, false
	}
	return user, true
}

// notify reports a condition in a notice, once per (repository, ref, reason) for
// noticeTTL. The description says more than the key does: the commits involved
// change as upstream moves, and moving on is not a new condition.
func notify(repo *repo_model.Repository, ref, reason, desc string) {
	sum := sha1.Sum([]byte(ref + "\x00" + reason))
	key := fmt.Sprintf("inbound-notice:%d:%x", repo.ID, sum)
	if cache.GetCache().IsExist(key) {
		log.Trace("%s", desc)
		return
	}
	log.Warn("%s", desc)
	if err := system_model.CreateRepositoryNotice(desc); err != nil {
		log.Error("CreateRepositoryNotice: %v", err)
		return
	}
	if err := cache.GetCache().Put(key, "1", noticeTTL); err != nil {
		log.Error("Unable to remember notice %s: %v", key, err)
	}
}

// Record names, staging names and forge refs of one branch or tag:
//
//	refs/upstream/heads/x          the upstream tip the forge has dealt with
//	refs/upstream/staging/heads/x  where the fetch puts the upstream tip
//	refs/heads/x                   the forge's own
func stagingName(record string) string {
	return git.UpstreamPrefix + "staging/" + strings.TrimPrefix(record, git.UpstreamPrefix)
}

func forgeRef(record string) string {
	return "refs/" + strings.TrimPrefix(record, git.UpstreamPrefix)
}

func recordName(ref string) string {
	return git.UpstreamPrefix + strings.TrimPrefix(ref, "refs/")
}

// runInbound fetches the remote of m and pushes what it gained. It returns true
// if it finished, whatever it left alone.
func runInbound(ctx context.Context, m *repo_model.PushMirror, repo *repo_model.Repository, user *user_model.User) bool {
	remoteURL, ok := fetchableRemote(ctx, repo, m.RemoteName)
	if !ok {
		return false
	}
	if err := hideUpstream(ctx, repo); err != nil {
		log.Error("SyncInboundMirror [repo: %-v]: %v", repo, err)
		return false
	}

	recorded, err := listTips(ctx, repo, git.UpstreamPrefix+"heads/", git.UpstreamPrefix+"tags/")
	if err != nil {
		log.Error("SyncInboundMirror [repo: %-v]: %v", repo, err)
		return false
	}

	// Never follow HTTP redirects, see runSync. The staging namespace is ours, so forcing it is safe.
	fetch := gitcmd.NewCommand("fetch", "--no-tags", "--prune").AddConfig("http.followRedirects", "false").
		AddDynamicArguments(m.RemoteName,
			"+"+git.BranchPrefix+"*:"+stagingName(git.UpstreamPrefix+"heads/*"),
			"+"+git.TagPrefix+"*:"+stagingName(git.UpstreamPrefix+"tags/*")).
		WithTimeout(mirrorTimeout()).
		WithEnv(proxy.EnvWithProxy(remoteURL.URL))
	if _, stderr, err := gitrepo.RunCmdString(ctx, repo, fetch); err != nil {
		// sanitize the output, since it may contain the remote address, which may contain a password
		stderrMessage := util.SanitizeCredentialURLs(stderr)
		log.Error("SyncInboundMirror [repo: %-v]: failed to fetch %s:\nStderr: %s\nErr: %v", repo, m.RemoteName, stderrMessage, util.SanitizeErrorCredentialURLs(err))
		notify(repo, "", "fetch: "+stderrMessage, fmt.Sprintf("Inbound sync of %s failed to fetch %s: %s", repo.FullName(), m.RemoteName, stderrMessage))
		return false
	}

	staged, err := listTips(ctx, repo, stagingName(git.UpstreamPrefix+"heads/"), stagingName(git.UpstreamPrefix+"tags/"))
	if err != nil {
		log.Error("SyncInboundMirror [repo: %-v]: %v", repo, err)
		return false
	}
	// Keyed by record name, as the record is.
	upstream := make(map[string]string, len(staged))
	for name, id := range staged {
		upstream[git.UpstreamPrefix+strings.TrimPrefix(name, git.UpstreamPrefix+"staging/")] = id
	}

	// From here on the work is the forge's to finish, whatever happens to the request.
	settle := context.WithoutCancel(ctx)

	m.LastInboundUnix = timeutil.TimeStampNow()
	if err := repo_model.UpdatePushMirrorLastInbound(settle, m); err != nil {
		log.Error("SyncInboundMirror [repo: %-v]: unable to record the time: %v", repo, err)
	}

	// A ref upstream no longer has is no longer recorded, so that it is new if it comes back.
	var gone []string
	for name := range recorded {
		if _, ok := upstream[name]; !ok {
			gone = append(gone, name)
		}
	}
	if err := writeRecords(settle, repo, nil, gone); err != nil {
		log.Error("SyncInboundMirror [repo: %-v]: %v", repo, err)
	}

	changed := map[string]string{}
	for name, id := range upstream {
		if recorded[name] != id {
			changed[name] = id
		}
	}

	here, err := forgeTips(ctx, repo)
	if err != nil {
		log.Error("SyncInboundMirror [repo: %-v]: %v", repo, err)
		return false
	}
	hasBranch := false
	for ref := range here {
		hasBranch = hasBranch || strings.HasPrefix(ref, git.BranchPrefix)
	}
	empty := repo.IsEmpty && !hasBranch
	primed := isPrimed(ctx, repo)

	if empty {
		if !primed {
			markPrimed(settle, repo)
		}
		return len(changed) == 0 || seed(ctx, m, repo, user, remoteURL, changed)
	}

	for _, name := range slices.Sorted(maps.Keys(changed)) {
		if ctx.Err() != nil {
			return false
		}
		// Look again at the repository and the ref, which others may have changed since this run began.
		current, err := repo_model.GetRepositoryByID(ctx, repo.ID)
		if err != nil {
			log.Error("SyncInboundMirror [repo: %-v]: %v", repo, err)
			return false
		}
		if current.IsArchived {
			log.Warn("SyncInboundMirror [repo: %-v]: archived while syncing, stopped", repo)
			return false
		}
		ref := forgeRef(name)
		old, err := forgeTip(ctx, repo, ref)
		if err != nil {
			log.Error("SyncInboundMirror [repo: %-v]: %v", repo, err)
			return false
		}

		// An absent branch is created only once upstream has been recorded: GitHub
		// keeps every branch the forge ever deleted, since the push out never
		// deletes. A tag is immutable, so one the forge lacks is always created.
		create := primed || strings.HasPrefix(ref, git.TagPrefix)
		if err := advance(ctx, repo, user, stagingName(name), ref, changed[name], old, create); err != nil {
			// Reported where it was found. Not recorded, so the next sync looks at this tip again.
			log.Trace("SyncInboundMirror [repo: %-v]: %s: %v", repo, ref, err)
			continue
		}
		if err := gitrepo.UpdateRef(settle, repo, name, changed[name]); err != nil {
			log.Error("SyncInboundMirror [repo: %-v]: unable to record %s: %v", repo, name, err)
		}
	}
	if ctx.Err() != nil {
		return false
	}
	if !primed {
		markPrimed(settle, repo)
	}
	return true
}

func mirrorTimeout() time.Duration {
	return time.Duration(setting.Git.Timeout.Mirror) * time.Second
}

// hideUpstream hides the namespace from clients, and closes it to their pushes, so only the sync writes there.
func hideUpstream(ctx context.Context, repo *repo_model.Repository) error {
	for _, key := range []string{"uploadpack.hideRefs", "receive.hideRefs"} {
		if err := gitrepo.GitConfigSet(ctx, repo, key, strings.TrimSuffix(git.UpstreamPrefix, "/")); err != nil {
			return fmt.Errorf("%s: %w", key, err)
		}
	}
	return nil
}

func isPrimed(ctx context.Context, repo *repo_model.Repository) bool {
	out, _, err := gitrepo.RunCmdString(ctx, repo, gitcmd.NewCommand("config", "--get").AddDynamicArguments(primedKey))
	return err == nil && strings.TrimSpace(out) == "true"
}

func markPrimed(ctx context.Context, repo *repo_model.Repository) {
	if err := gitrepo.GitConfigSet(ctx, repo, primedKey, "true"); err != nil {
		log.Error("SyncInboundMirror [repo: %-v]: %v", repo, err)
	}
}

// listTips returns where the refs under the prefixes stand, by ref name.
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

// forgeTips returns where the repository's own branches and tags stand, by ref name.
func forgeTips(ctx context.Context, repo *repo_model.Repository) (map[string]string, error) {
	return listTips(ctx, repo, git.BranchPrefix, git.TagPrefix)
}

// forgeTip returns where one of the repository's refs stands, or "" if it does not exist.
func forgeTip(ctx context.Context, repo *repo_model.Repository, ref string) (string, error) {
	tips, err := listTips(ctx, repo, ref)
	return tips[ref], err
}

// writeRecords sets and drops recorded upstream tips in one transaction.
func writeRecords(ctx context.Context, repo *repo_model.Repository, set map[string]string, drop []string) error {
	var commands strings.Builder
	for _, name := range slices.Sorted(maps.Keys(set)) {
		fmt.Fprintf(&commands, "update %s %s\n", name, set[name])
	}
	for _, name := range drop {
		fmt.Fprintf(&commands, "delete %s\n", name)
	}
	if commands.Len() == 0 {
		return nil
	}
	if _, _, err := gitrepo.RunCmdString(ctx, repo, gitcmd.NewCommand("update-ref", "--stdin").WithStdinBytes([]byte(commands.String()))); err != nil {
		return fmt.Errorf("update-ref: %w", err)
	}
	return nil
}

// recordPushed records the tips the push out has just put on GitHub, as the ones
// the forge has dealt with. A branch pushed out and then deleted here is then
// not new to the next inbound sync, which would bring it back.
func recordPushed(ctx context.Context, repo *repo_model.Repository, pushed map[string]string) {
	if err := hideUpstream(ctx, repo); err != nil {
		log.Error("SyncPushMirror [repo: %-v]: %v", repo, err)
		return
	}
	recorded, err := listTips(ctx, repo, git.UpstreamPrefix+"heads/", git.UpstreamPrefix+"tags/")
	if err != nil {
		log.Error("SyncPushMirror [repo: %-v]: %v", repo, err)
		return
	}
	set := map[string]string{}
	for ref, id := range pushed {
		if name := recordName(ref); recorded[name] != id {
			set[name] = id
		}
	}
	if err := writeRecords(ctx, repo, set, nil); err != nil {
		log.Error("SyncPushMirror [repo: %-v]: %v", repo, err)
	}
}

// advance pushes the staged upstream tip at name to ref as user, when that only
// adds history. old is where ref stands now, empty if it does not exist. It
// returns nil when the tip is dealt with, including when it is left alone for
// good reason, and an error for a ref it could not or would not move, after
// reporting it.
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
		notify(repo, ref, "tag differs", fmt.Sprintf("Inbound sync of %s left %s untouched: the forge has it at %s, upstream at %s", repo.FullName(), ref, short(old), short(tip)))
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
			notify(repo, ref, "diverged", fmt.Sprintf("Inbound sync of %s left %s untouched: the forge at %s and upstream at %s have diverged", repo.FullName(), ref, short(old), short(tip)))
			return errors.New("diverged")
		}
	}

	// Not forced: the receiving side refuses anything but an addition, as well as
	// whatever its hooks refuse.
	err := gitrepo.Push(ctx, repo, repo, git.PushOptions{
		Branch:  name + ":" + ref,
		Env:     repo_module.PushingEnvironment(user, repo),
		Timeout: mirrorTimeout(),
	})
	if err == nil {
		return nil
	}
	if ctx.Err() != nil {
		return ctx.Err() // cut short, not refused
	}
	reason := refusal(err)
	notify(repo, ref, "refused: "+reason, fmt.Sprintf("Inbound sync of %s could not move %s from %s to %s: %s", repo.FullName(), ref, short(old), short(tip), reason))
	return errors.New("refused")
}

// refusal says why a push failed: what the hooks said if they declined it, without the local paths around it.
func refusal(err error) string {
	if rejected, ok := errors.AsType[*git.ErrPushRejected](err); ok {
		return rejected.Message
	}
	return util.SanitizeErrorCredentialURLs(err).Error()
}

// seed fills an empty repository from the remote. GitHub's default branch goes
// first, and becomes the repository's, then the other branches in one push.
// The tags go in one push that skips the hooks: an empty repository has no
// protection to apply, and a push event per historical tag would run every
// release workflow the repository has for each of them. The releases are made
// from the tags, as a pull mirror makes them.
func seed(ctx context.Context, m *repo_model.PushMirror, repo *repo_model.Repository, user *user_model.User, remoteURL *giturl.GitURL, changed map[string]string) bool {
	settle := context.WithoutCancel(ctx)
	var branches, tags []string
	for _, name := range slices.Sorted(maps.Keys(changed)) {
		if strings.HasPrefix(forgeRef(name), git.TagPrefix) {
			tags = append(tags, name)
		} else {
			branches = append(branches, name)
		}
	}

	// push puts the staged tips of names on their refs in one push, and records them.
	push := func(env []string, names []string) bool {
		if len(names) == 0 {
			return true
		}
		refspecs := make([]string, len(names))
		for i, name := range names {
			refspecs[i] = stagingName(name) + ":" + forgeRef(name)
		}
		if err := gitrepo.Push(ctx, repo, repo, git.PushOptions{Refspecs: refspecs, Env: env, Timeout: mirrorTimeout()}); err != nil {
			if ctx.Err() == nil {
				reason := refusal(err)
				notify(repo, forgeRef(names[0]), "refused: "+reason, fmt.Sprintf("Inbound sync of %s could not fill it from %s, starting at %s: %s", repo.FullName(), m.RemoteName, forgeRef(names[0]), reason))
			}
			return false
		}
		set := map[string]string{}
		for _, name := range names {
			set[name] = changed[name]
		}
		if err := writeRecords(settle, repo, set, nil); err != nil {
			log.Error("SyncInboundMirror [repo: %-v]: %v", repo, err)
		}
		return true
	}

	if def := defaultBranch(ctx, m, repo, remoteURL, branches); def != "" {
		branch := strings.TrimPrefix(def, git.UpstreamPrefix+"heads/")
		if repo.DefaultBranch != branch {
			repo.DefaultBranch = branch
			if err := repo_model.UpdateDefaultBranch(ctx, repo); err != nil {
				log.Error("SyncInboundMirror [repo: %-v]: unable to set the default branch: %v", repo, err)
				return false
			}
			if err := gitrepo.SetDefaultBranch(ctx, repo, branch); err != nil {
				log.Error("SyncInboundMirror [repo: %-v]: unable to set HEAD: %v", repo, err)
				return false
			}
		}
		if !push(repo_module.PushingEnvironment(user, repo), []string{def}) {
			return false
		}
		branches = slices.DeleteFunc(branches, func(name string) bool { return name == def })
	}
	if !push(repo_module.PushingEnvironment(user, repo), branches) {
		return false
	}
	if !push(repo_module.InternalPushingEnvironment(user, repo), tags) {
		return false
	}
	if len(tags) == 0 {
		return true
	}

	gitRepo, err := gitrepo.OpenRepository(ctx, repo)
	if err != nil {
		log.Error("SyncInboundMirror [repo: %-v]: %v", repo, err)
		return false
	}
	defer gitRepo.Close()
	if _, err := repo_module.SyncReleasesWithTags(ctx, repo, gitRepo); err != nil {
		log.Error("SyncInboundMirror [repo: %-v]: failed to synchronize tags to releases: %v", repo, err)
		return false
	}
	return true
}

// defaultBranch returns the record name of GitHub's default branch among branches,
// or else of main, master or the first of them; "" if there are none.
func defaultBranch(ctx context.Context, m *repo_model.PushMirror, repo *repo_model.Repository, remoteURL *giturl.GitURL, branches []string) string {
	ls := gitcmd.NewCommand("ls-remote", "--symref").AddConfig("http.followRedirects", "false").
		AddDynamicArguments(m.RemoteName, "HEAD").
		WithTimeout(mirrorTimeout()).
		WithEnv(proxy.EnvWithProxy(remoteURL.URL))
	if out, _, err := gitrepo.RunCmdString(ctx, repo, ls); err == nil {
		for line := range strings.SplitSeq(out, "\n") {
			if target, ok := strings.CutPrefix(line, "ref: "); ok {
				target, _, _ = strings.Cut(target, "\t")
				if name := recordName(target); slices.Contains(branches, name) {
					return name
				}
			}
		}
	}
	for _, guess := range []string{"main", "master"} {
		if name := git.UpstreamPrefix + "heads/" + guess; slices.Contains(branches, name) {
			return name
		}
	}
	if len(branches) > 0 {
		return branches[0]
	}
	return ""
}

func short(id string) string {
	if len(id) > 12 {
		return id[:12]
	}
	return id
}
