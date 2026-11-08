// Copyright 2026 Hanzo AI, Inc. All rights reserved.
// SPDX-License-Identifier: MIT

package mirror

import (
	"context"
	"crypto/sha1"
	"errors"
	"fmt"
	"maps"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/hanzoai/git/models/db"
	git_model "github.com/hanzoai/git/models/git"
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

// botBranches are the branch name prefixes bots open on GitHub, in lower case.
// They are never brought in: what a push here runs is the repository's Actions,
// with the organization's secrets, and a bot's branch carries code its author
// did not write.
var botBranches = []string{
	"dependabot/",
	"renovate/",
	"gh-readonly-queue/",
	"copilot/",
	"release-please--",
	"changeset-release/",
}

// isBotBranch reports whether ref is a branch a bot opened, whatever the case of its name.
func isBotBranch(ref string) bool {
	name, ok := strings.CutPrefix(ref, git.BranchPrefix)
	if !ok {
		return false
	}
	name = strings.ToLower(name)
	return slices.ContainsFunc(botBranches, func(prefix string) bool { return strings.HasPrefix(name, prefix) })
}

// isObjectNamed reports whether ref is a branch or tag named like an object id,
// which git may read as the object, or as the ref, depending on where it is asked.
func isObjectNamed(ref string) bool {
	return git.IsObjectName(strings.TrimPrefix(strings.TrimPrefix(ref, git.BranchPrefix), git.TagPrefix))
}

// hexRun finds what in a refusal changes with each commit, and so is not part of its reason.
var hexRun = regexp.MustCompile(`[0-9a-fA-F]{7,}`)

// githubRepoPath returns the "owner/name" that address names on github.com, in any
// letter case, without credentials, a trailing ".git" or "/". Anything else is
// not an address of a GitHub repository: another host, a port, a query or
// fragment, extra or encoded path segments.
func githubRepoPath(address string) (string, bool) {
	u, err := giturl.ParseGitURL(address)
	if err != nil || !strings.EqualFold(u.Hostname(), "github.com") || u.Port() != "" {
		return "", false
	}
	switch u.Scheme { // not http, which carries the credentials in the clear, nor git, which has none
	case "https", "ssh":
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
// change as upstream moves, and moving on is not a new condition. For the same
// reason a commit id in the reason, as in "unverified commit <sha>", is left out of the key.
func notify(repo *repo_model.Repository, ref, reason, desc string) {
	sum := sha1.Sum([]byte(ref + "\x00" + hexRun.ReplaceAllString(reason, "")))
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
	refspecs := []string{
		"+" + git.BranchPrefix + "*:" + stagingName(git.UpstreamPrefix+"heads/*"),
		"+" + git.TagPrefix + "*:" + stagingName(git.UpstreamPrefix+"tags/*"),
	}
	if git.DefaultFeatures().CheckVersionAtLeast("2.29") {
		// Not fetched at all, so what is never brought in is not in the repository for anyone to ask for by id.
		for _, prefix := range botBranches {
			refspecs = append(refspecs, "^"+git.BranchPrefix+prefix+"*")
		}
	}
	fetch := gitcmd.NewCommand("fetch", "--no-tags", "--prune").AddConfig("http.followRedirects", "false").
		AddDynamicArguments(append([]string{m.RemoteName}, refspecs...)...).
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

	// What is never brought in is not kept: neither staged nor recorded, whatever an
	// earlier version left, and whatever the case of the name, which the fetch's
	// exclusions cannot match.
	var skipped []string
	for name := range upstream {
		ref := forgeRef(name)
		switch {
		case isBotBranch(ref):
		case isObjectNamed(ref):
			notify(repo, ref, "object-named", fmt.Sprintf("Inbound sync of %s did not bring in %s: a name like an object id can be read as the object, so it is not taken", repo.FullName(), ref))
		default:
			continue
		}
		skipped = append(skipped, name)
	}
	for name := range recorded {
		if ref := forgeRef(name); (isBotBranch(ref) || isObjectNamed(ref)) && !slices.Contains(skipped, name) {
			skipped = append(skipped, name)
		}
	}
	if len(skipped) > 0 {
		drop := make([]string, 0, 2*len(skipped))
		for _, name := range skipped {
			drop = append(drop, name, stagingName(name))
			delete(upstream, name)
			delete(recorded, name)
		}
		if err := writeRecords(settle, repo, nil, drop); err != nil {
			log.Error("SyncInboundMirror [repo: %-v]: %v", repo, err)
		}
	}

	m.LastInboundUnix = timeutil.TimeStampNow()
	if err := repo_model.UpdatePushMirrorLastInbound(settle, m); err != nil {
		log.Error("SyncInboundMirror [repo: %-v]: unable to record the time: %v", repo, err)
	}

	// A ref upstream no longer has, while it has others, is no longer recorded,
	// so that it is new if it comes back. A fetch that finds no refs at all says
	// nothing about any of them: a remote that is down or answering empty must
	// not make every ref new when it recovers.
	var gone []string
	if len(upstream) > 0 {
		for name := range recorded {
			if _, ok := upstream[name]; !ok {
				gone = append(gone, name)
			}
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

	// An empty repository is filled branch by branch before anything else. Once
	// it is marked, a run that stops partway is carried on by the loop below,
	// whatever the repository then looks like.
	if empty {
		if !primed {
			markPrimed(settle, repo)
			primed = true
		}
		if !seed(ctx, m, repo, user, remoteURL, changed) {
			return false
		}
	}

	branchRules := branchRulesFingerprint(ctx, repo, user)

	var fresh []string // tags the forge lacks, imported together below
	for _, name := range slices.Sorted(maps.Keys(changed)) {
		if ctx.Err() != nil || !writable(ctx, repo) {
			return false
		}
		// Look again at the ref, which others may have changed since this run began.
		ref := forgeRef(name)
		old, err := forgeTip(ctx, repo, ref)
		if err != nil {
			log.Error("SyncInboundMirror [repo: %-v]: %v", repo, err)
			return false
		}
		if old == "" && strings.HasPrefix(ref, git.TagPrefix) {
			fresh = append(fresh, name)
			continue
		}

		// A branch the hooks declined is not offered to them again, while neither
		// its tip nor the rules have changed: a protected branch refuses the same push
		// every time, and there may be many.
		key := declinedKey(repo, ref, changed[name], branchRules)
		if branchRules != "" && cache.GetCache().IsExist(key) {
			continue
		}
		// An absent branch is created only once upstream has been recorded: GitHub
		// keeps every branch the forge ever deleted, since the push out never
		// deletes.
		if err := advance(ctx, repo, user, ref, changed[name], old, primed); err != nil {
			// Reported where it was found. Not recorded, so the next sync looks at this tip again.
			log.Trace("SyncInboundMirror [repo: %-v]: %s: %v", repo, ref, err)
			if branchRules != "" && errors.Is(err, errDeclined) {
				if err := cache.GetCache().Put(key, "1", declinedTTL); err != nil {
					log.Error("Unable to remember %s: %v", key, err)
				}
			}
			continue
		}
		if err := gitrepo.UpdateRef(settle, repo, name, changed[name]); err != nil {
			log.Error("SyncInboundMirror [repo: %-v]: unable to record %s: %v", repo, name, err)
		}
	}
	importTags(ctx, repo, user, fresh, changed)
	if ctx.Err() != nil {
		return false
	}
	if !primed {
		markPrimed(settle, repo)
	}
	return true
}

// writable reports whether the repository may still be written to. It is looked
// up again each time, since it may have been archived since the run began.
func writable(ctx context.Context, repo *repo_model.Repository) bool {
	current, err := repo_model.GetRepositoryByID(ctx, repo.ID)
	if err != nil {
		log.Error("SyncInboundMirror [repo: %-v]: %v", repo, err)
		return false
	}
	if current.IsArchived {
		log.Warn("SyncInboundMirror [repo: %-v]: archived while syncing, stopped", repo)
		return false
	}
	return true
}

// objectName names an object by its id and by nothing else. A refspec's source
// that is only an id is read as a ref's name first, and a writer may have made
// refs/heads/<id> or refs/tags/<id> to be read in its place; ^{object} is the
// object itself, whatever refs there are.
func objectName(id string) string {
	return id + "^{object}"
}

// pushBatch pushes the staged tips of names to their refs in one push, as env, and
// records the ones that arrived. A push that failed may still have landed some of
// its refs, so what is recorded, and returned, is what the repository holds now.
func pushBatch(ctx context.Context, repo *repo_model.Repository, env, names []string, tips map[string]string) (landed []string, err error) {
	refspecs := make([]string, len(names))
	for i, name := range names {
		refspecs[i] = objectName(tips[name]) + ":" + forgeRef(name)
	}
	err = gitrepo.Push(ctx, repo, repo, git.PushOptions{Refspecs: refspecs, Env: env, ReceivePack: advertiseStaging(), Timeout: mirrorTimeout()})

	settle := context.WithoutCancel(ctx)
	here, listErr := forgeTips(settle, repo)
	if listErr != nil {
		log.Error("SyncInboundMirror [repo: %-v]: %v", repo, listErr)
		return nil, err
	}
	set := map[string]string{}
	for _, name := range names {
		if here[forgeRef(name)] == tips[name] {
			set[name] = tips[name]
			landed = append(landed, name)
		}
	}
	if recordErr := writeRecords(settle, repo, set, nil); recordErr != nil {
		log.Error("SyncInboundMirror [repo: %-v]: %v", repo, recordErr)
	}
	return landed, err
}

// declinedTTL is how long a tag or branch the hooks declined is not offered to them again. They
// decline for a reason, which may last or may be an outage, so they are asked again.
const declinedTTL = 60 * 60

// tagRulesFingerprint says which protected tag rules, and which user, a decision
// about a tag was taken under.
func tagRulesFingerprint(rules []*git_model.ProtectedTag, userID int64) string {
	lines := make([]string, len(rules))
	for i, rule := range rules {
		lines[i] = fmt.Sprintf("%s|%v|%v", rule.NamePattern, rule.AllowlistUserIDs, rule.AllowlistTeamIDs)
	}
	slices.Sort(lines)
	return fmt.Sprintf("%x", sha1.Sum(fmt.Appendf(nil, "%d\n%s", userID, strings.Join(lines, "\n"))))
}

// branchRulesFingerprint says which protected branch rules, and which user, a
// decision about a branch was taken under; "" if they cannot be read.
func branchRulesFingerprint(ctx context.Context, repo *repo_model.Repository, user *user_model.User) string {
	rules, err := git_model.FindRepoProtectedBranchRules(ctx, repo.ID)
	if err != nil {
		log.Error("SyncInboundMirror [repo: %-v]: %v", repo, err)
		return ""
	}
	var lines strings.Builder
	fmt.Fprintf(&lines, "%d\n", user.ID)
	for _, rule := range rules {
		fmt.Fprintf(&lines, "%d|%d\n", rule.ID, rule.UpdatedUnix)
	}
	return fmt.Sprintf("%x", sha1.Sum([]byte(lines.String())))
}

// declinedKey names a ref's tip as the hooks declined it, under the rules of the day.
func declinedKey(repo *repo_model.Repository, ref, tip, fingerprint string) string {
	return fmt.Sprintf("inbound-declined:%d:%x", repo.ID, sha1.Sum([]byte(ref+"\x00"+tip+"\x00"+fingerprint)))
}

// importTags creates the tags the forge lacks. A tag the protected tag rules keep
// the sync user from creating is left, and noticed. One tag is pushed like any ref.
// Several are pushed together, and without the hooks: a push event for each would
// run, for every old tag, every workflow that a tag push starts, which for a
// release workflow is a release. The rules are the only part of the hooks that
// bears on a tag, and are applied here, right before the push, so the releases
// are made from the tags afterwards, as a pull mirror makes them. Where the rules
// cannot be read, the hooks decide.
func importTags(ctx context.Context, repo *repo_model.Repository, user *user_model.User, names []string, tips map[string]string) {
	if len(names) == 0 || !writable(ctx, repo) {
		return
	}
	settle := context.WithoutCancel(ctx)

	// A hooked push of one tag. If the hooks decline it, they are not asked again
	// for as long as neither the tag's tip nor the rules have changed.
	hooked := func(name, fingerprint string) {
		key := declinedKey(repo, forgeRef(name), tips[name], fingerprint)
		if cache.GetCache().IsExist(key) {
			return
		}
		err := advance(ctx, repo, user, forgeRef(name), tips[name], "", true)
		switch {
		case err == nil:
			if err := writeRecords(settle, repo, map[string]string{name: tips[name]}, nil); err != nil {
				log.Error("SyncInboundMirror [repo: %-v]: unable to record %s: %v", repo, name, err)
			}
		case errors.Is(err, errDeclined):
			if err := cache.GetCache().Put(key, "1", declinedTTL); err != nil {
				log.Error("Unable to remember %s: %v", key, err)
			}
		}
	}
	// A hooked push of all of them, which the hooks accept or refuse as a whole. If
	// they refuse it, they are asked about the tags one at a time, so that the ones
	// that are allowed arrive.
	hookedBatch := func(names []string) {
		_, err := pushBatch(ctx, repo, repo_module.PushingEnvironment(user, repo), names, tips)
		if err == nil || ctx.Err() != nil {
			return
		}
		reason := refusal(err)
		notify(repo, forgeRef(names[0]), "refused: "+reason, fmt.Sprintf("Inbound sync of %s was refused %d new tags, the first of them %s: %s", repo.FullName(), len(names), forgeRef(names[0]), reason))
		for _, name := range names {
			if ctx.Err() != nil || !writable(ctx, repo) {
				return
			}
			if here, err := forgeTip(ctx, repo, forgeRef(name)); err == nil && here == "" {
				hooked(name, "")
			}
		}
	}

	rules, err := git_model.GetProtectedTags(ctx, repo.ID)
	if err != nil {
		log.Error("SyncInboundMirror [repo: %-v]: %v", repo, err)
		hookedBatch(names)
		return
	}
	fingerprint := tagRulesFingerprint(rules, user.ID)

	var allowed []string
	for _, name := range names {
		ok, err := git_model.IsUserAllowedToControlTag(ctx, rules, strings.TrimPrefix(forgeRef(name), git.TagPrefix), user.ID)
		if err != nil {
			log.Error("SyncInboundMirror [repo: %-v]: %v", repo, err)
			hookedBatch(names)
			return
		}
		if ok {
			allowed = append(allowed, name)
			continue
		}
		ref := forgeRef(name)
		notify(repo, ref, "protected tag", fmt.Sprintf("Inbound sync of %s left %s untouched: a protected tag rule keeps %s from creating it", repo.FullName(), ref, user.Name))
	}

	switch len(allowed) {
	case 0:
		return
	case 1:
		hooked(allowed[0], fingerprint)
		return
	}

	_, err = pushBatch(ctx, repo, repo_module.InternalPushingEnvironment(user, repo), allowed, tips)
	if err != nil && ctx.Err() == nil {
		reason := refusal(err)
		notify(repo, forgeRef(allowed[0]), "failed: "+reason, fmt.Sprintf("Inbound sync of %s failed to create some of its %d new tags, the first of them %s: %s", repo.FullName(), len(allowed), forgeRef(allowed[0]), reason))
	}
	// Whatever arrived has no release, whether or not the push says it failed.
	gitRepo, openErr := gitrepo.OpenRepository(settle, repo)
	if openErr != nil {
		log.Error("SyncInboundMirror [repo: %-v]: %v", repo, openErr)
		return
	}
	defer gitRepo.Close()
	if _, syncErr := repo_module.SyncReleasesWithTags(settle, repo, gitRepo); syncErr != nil {
		log.Error("SyncInboundMirror [repo: %-v]: failed to synchronize tags to releases: %v", repo, syncErr)
	}
}

func mirrorTimeout() time.Duration {
	return time.Duration(setting.Git.Timeout.Mirror) * time.Second
}

// hideUpstream hides the namespace from clients, and closes it to their pushes,
// so only the sync writes there. The staging refs are advertised to the sync's own
// pushes alone, see advertiseStaging. Nothing under it can be fetched by id by a
// client that speaks the old protocol; the new one asks for any object, so what
// is not to be seen is also not brought in, see botBranches.
func hideUpstream(ctx context.Context, repo *repo_model.Repository) error {
	hide := strings.TrimSuffix(git.UpstreamPrefix, "/")
	for key, value := range map[string]string{
		"uploadpack.hideRefs":           hide,
		"receive.hideRefs":              hide,
		"uploadpack.allowAnySHA1InWant": "false", // the forge allows it, for partial clones
	} {
		if err := gitrepo.GitConfigSetAll(ctx, repo, key, value); err != nil {
			return fmt.Errorf("%s: %w", key, err)
		}
	}
	return nil
}

// advertiseStaging is the command that receives the sync's own pushes: receive-pack
// that advertises the staging refs, and nobody else's does. A push is told which
// commits the repository has from what it advertises, and without this a push of
// the staged tips to the same repository would carry their whole history back to
// it. The last hideRefs pattern that matches a ref decides, and this one comes last.
// (The environment cannot carry this: git clears it for a push to a local path.)
func advertiseStaging() string {
	return fmt.Sprintf("%s -c receive.hideRefs=!%sstaging receive-pack", util.ShellEscape(gitcmd.GitExecutable), git.UpstreamPrefix)
}

// configValue returns the repository setting key, or "" if it has none.
func configValue(ctx context.Context, repo *repo_model.Repository, key string) string {
	out, _, err := gitrepo.RunCmdString(ctx, repo, gitcmd.NewCommand("config", "--get").AddDynamicArguments(key))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(out)
}

func isPrimed(ctx context.Context, repo *repo_model.Repository) bool {
	return configValue(ctx, repo, primedKey) == "true"
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

// advance pushes the staged upstream tip to ref as user, when that only
// adds history. old is where ref stands now, empty if it does not exist. It
// returns nil when the tip is dealt with, including when it is left alone for
// good reason, and an error for a ref it could not or would not move, after
// reporting it.
func advance(ctx context.Context, repo *repo_model.Repository, user *user_model.User, ref, tip, old string, create bool) error {
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
		Branch:      objectName(tip) + ":" + ref,
		Env:         repo_module.PushingEnvironment(user, repo),
		ReceivePack: advertiseStaging(),
		Timeout:     mirrorTimeout(),
	})
	if err == nil {
		return nil
	}
	if ctx.Err() != nil {
		return ctx.Err() // cut short, not refused
	}
	reason := refusal(err)
	notify(repo, ref, "refused: "+reason, fmt.Sprintf("Inbound sync of %s could not move %s from %s to %s: %s", repo.FullName(), ref, short(old), short(tip), reason))
	if _, declined := errors.AsType[*git.ErrPushRejected](err); declined {
		return errDeclined
	}
	return errors.New("failed")
}

// errDeclined is advance's error for a push that the receiving side's hooks declined.
var errDeclined = errors.New("declined")

// refusal says why a push failed: what the hooks said if they declined it, without the local paths around it.
func refusal(err error) string {
	if rejected, ok := errors.AsType[*git.ErrPushRejected](err); ok {
		return rejected.Message
	}
	return util.SanitizeErrorCredentialURLs(err).Error()
}

// seed fills an empty repository with the branches changed holds. GitHub's
// default branch goes first, and becomes the repository's, then the other
// branches in one push. A branch is deleted from changed once it is in, so
// the tags, which are left, are imported with the rest of the run, and a seed
// that stops partway is simply carried on by it.
func seed(ctx context.Context, m *repo_model.PushMirror, repo *repo_model.Repository, user *user_model.User, remoteURL *giturl.GitURL, changed map[string]string) bool {
	var branches []string
	for _, name := range slices.Sorted(maps.Keys(changed)) {
		if !strings.HasPrefix(forgeRef(name), git.TagPrefix) {
			branches = append(branches, name)
		}
	}

	push := func(names []string) bool {
		if len(names) == 0 {
			return true
		}
		if ctx.Err() != nil || !writable(ctx, repo) {
			return false
		}
		landed, err := pushBatch(ctx, repo, repo_module.PushingEnvironment(user, repo), names, changed)
		for _, name := range landed {
			delete(changed, name)
		}
		if err != nil {
			if ctx.Err() == nil {
				reason := refusal(err)
				notify(repo, forgeRef(names[0]), "refused: "+reason, fmt.Sprintf("Inbound sync of %s could not fill it from %s, starting at %s: %s", repo.FullName(), m.RemoteName, forgeRef(names[0]), reason))
			}
			return false
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
		if !push([]string{def}) {
			return false
		}
		branches = slices.DeleteFunc(branches, func(name string) bool { return name == def })
	}
	return push(branches)
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
