// Copyright 2026 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package misc

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"strings"

	repo_model "github.com/hanzoai/git/models/repo"
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
// one upstream push reaches every platform we mirror to in one hop.
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

	// The mirror row is the single test for "has an upstream": a plain repo or
	// a push-only repo has none and must never be queued for a fetch.
	if _, err := repo_model.GetMirrorByRepoID(req.Context(), repo.ID); err != nil {
		_, _ = w.Write([]byte("ok"))
		return
	}

	mirror_service.AddPullMirrorToQueue(repo.ID)
	log.Trace("sync: queued pull for %s", repo.FullName())
	_, _ = w.Write([]byte("queued"))
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
