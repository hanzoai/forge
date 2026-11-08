// Copyright 2026 Hanzo AI, Inc. All rights reserved.
// SPDX-License-Identifier: MIT

package misc

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"strings"

	"github.com/hanzoai/git/models/db"
	repo_model "github.com/hanzoai/git/models/repo"
	giturl "github.com/hanzoai/git/modules/git/url"
	"github.com/hanzoai/git/modules/log"
	"github.com/hanzoai/git/modules/setting"
	mirror_service "github.com/hanzoai/git/services/mirror"
)

// maxSyncBody bounds the read: a push payload is a few KiB, and an unbounded
// read ahead of authentication is a memory DoS.
const maxSyncBody = 1 << 20

// Sync is the one entry point for "this repo changed upstream, take it now".
// It exists because an org-level webhook is a single static URL that cannot
// carry a token header; authenticated callers already have the software's
// per-repo mirror-sync APIs, so this adds no second door for them.
//
// Direction is a property of the repo, not of this route — a pull mirror
// fetches, and that fetch propagates to its push mirrors (services/mirror), so
// one upstream push reaches every platform we mirror to in one hop. A regular
// repo pushes to its platform through a push mirror; when that platform is the
// one speaking, the event fast-forwards the repo from the push mirror's remote,
// so a push to either side reaches the other.
//
// Mounted ahead of the session middleware because the credential is the payload
// itself: HMAC-SHA256 over the exact bytes in X-Hub-Signature-256, keyed by
// [mirror] SYNC_SECRET. Fail-closed — an unset secret refuses everything.
//
// A verified event naming a repo we do not mirror is 200, not 404: a delivery
// log is not a repository-existence oracle, and red deliveries for repos we
// deliberately skip would train the reader to ignore red.
func Sync(w http.ResponseWriter, req *http.Request) {
	secret := setting.Mirror.SyncSecret
	if secret == "" {
		http.Error(w, "sync is not configured", http.StatusServiceUnavailable)
		return
	}

	body, err := io.ReadAll(io.LimitReader(req.Body, maxSyncBody))
	if err != nil {
		http.Error(w, "unreadable body", http.StatusBadRequest)
		return
	}

	if !validSignature(secret, req.Header.Get("X-Hub-Signature-256"), body) {
		http.Error(w, "invalid signature", http.StatusUnauthorized)
		return
	}

	ownerName, repoName, ok := repoFromPayload(body)
	if !ok {
		// An event naming no repository: a ping, or one we do not act on.
		_, _ = w.Write([]byte("ok"))
		return
	}

	repo, err := repo_model.GetRepositoryByOwnerAndName(req.Context(), ownerName, repoName)
	if err != nil {
		_, _ = w.Write([]byte("ok"))
		return
	}

	// The mirror row is the test for "has an upstream"; failing that, a push
	// mirror to the repo the event names is the way back in. A repo with
	// neither must never be queued for a fetch.
	if _, err := repo_model.GetMirrorByRepoID(req.Context(), repo.ID); err == nil {
		queuePull(repo.ID)
		log.Trace("sync: queued pull for %s", repo.FullName())
		_, _ = w.Write([]byte("queued"))
		return
	}

	if m := pushMirrorTo(req.Context(), repo, ownerName, repoName); m != nil {
		queueInbound(m.ID)
		log.Trace("sync: queued inbound for %s", repo.FullName())
		_, _ = w.Write([]byte("queued"))
		return
	}

	_, _ = w.Write([]byte("ok"))
}

// The queue entry points, held in variables so a test can see what was queued
// without running a sync.
var (
	queuePull    = mirror_service.AddPullMirrorToEventQueue
	queueInbound = mirror_service.AddInboundMirrorToEventQueue
)

// pushMirrorTo returns the push mirror of repo whose remote is the GitHub
// repository ownerName/repoName, or nil if it has none.
func pushMirrorTo(ctx context.Context, repo *repo_model.Repository, ownerName, repoName string) *repo_model.PushMirror {
	mirrors, _, err := repo_model.GetPushMirrorsByRepoID(ctx, repo.ID, db.ListOptionsAll)
	if err != nil {
		log.Error("sync: unable to list push mirrors of %s: %v", repo.FullName(), err)
		return nil
	}
	for _, m := range mirrors {
		if isGitHubRepo(m.RemoteAddress, ownerName, repoName) {
			return m
		}
	}
	return nil
}

// isGitHubRepo reports whether address names github.com/ownerName/repoName,
// whatever its scheme or credentials, with or without a trailing ".git" or "/",
// and in any letter case.
func isGitHubRepo(address, ownerName, repoName string) bool {
	u, err := giturl.ParseGitURL(address)
	if err != nil || !strings.EqualFold(u.Hostname(), "github.com") {
		return false
	}
	path := strings.TrimSuffix(strings.Trim(u.Path, "/"), ".git")
	return strings.EqualFold(path, ownerName+"/"+repoName)
}

// repoFromPayload reads the repository a push event names, a field every
// platform whose events we accept spells the same way.
func repoFromPayload(body []byte) (ownerName, repoName string, ok bool) {
	var payload struct {
		Repository struct {
			FullName string `json:"full_name"`
		} `json:"repository"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return "", "", false
	}
	ownerName, repoName, ok = strings.Cut(payload.Repository.FullName, "/")
	return ownerName, repoName, ok && ownerName != "" && repoName != ""
}

// validSignature reports whether sigHeader is the "sha256=<hex>" HMAC of body
// under secret. Constant-time; a malformed header is simply invalid.
func validSignature(secret, sigHeader string, body []byte) bool {
	hexDigest, ok := strings.CutPrefix(sigHeader, "sha256=")
	if !ok {
		return false
	}
	sent, err := hex.DecodeString(hexDigest)
	if err != nil {
		return false
	}
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	return hmac.Equal(sent, mac.Sum(nil))
}
