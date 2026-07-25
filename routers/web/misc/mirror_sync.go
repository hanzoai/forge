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

// maxMirrorWebhookBody caps the read: a push payload is a few KiB, and an
// unbounded read on an unauthenticated endpoint is a memory DoS.
const maxMirrorWebhookBody = 1 << 20 // 1 MiB

// MirrorGitHubWebhook is the PUSH half of mirror freshness. A pull mirror is
// otherwise only as fresh as its interval; with one org-level webhook per
// upstream org pointed here, a push upstream syncs the matching mirror
// IMMEDIATELY. The payload names the repo, so ONE webhook covers every repo in
// an org — nothing is configured per-repo.
//
// Unauthenticated by mount (it sits beside /api/healthz, ahead of the session
// middleware) because the credential is the payload itself: HMAC-SHA256 over
// the exact bytes, in X-Hub-Signature-256, keyed by [mirror]
// GITHUB_WEBHOOK_SECRET. Fail-closed — an unset secret refuses everything, and
// a bad signature is 401 before anything is parsed or looked up.
//
// Honest no-ops (200, nothing queued): the ping event, a repo we do not mirror
// (or do not mirror YET — the reconciler creates it on its next pass), and a
// repo that is not a pull mirror. 200 for all of them on purpose: a webhook
// delivery log is not a repository-existence oracle, and a red delivery would
// train the reader to ignore red.
func MirrorGitHubWebhook(w http.ResponseWriter, req *http.Request) {
	secret := setting.Mirror.GithubWebhookSecret
	if secret == "" {
		http.Error(w, "mirror webhook is not configured", http.StatusServiceUnavailable)
		return
	}

	body, err := io.ReadAll(io.LimitReader(req.Body, maxMirrorWebhookBody))
	if err != nil {
		http.Error(w, "unreadable body", http.StatusBadRequest)
		return
	}

	if !validMirrorSignature(secret, req.Header.Get("X-Hub-Signature-256"), body) {
		http.Error(w, "invalid signature", http.StatusUnauthorized)
		return
	}

	var payload struct {
		Repository struct {
			FullName string `json:"full_name"`
		} `json:"repository"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		http.Error(w, "unparseable payload", http.StatusBadRequest)
		return
	}

	ownerName, repoName, ok := strings.Cut(payload.Repository.FullName, "/")
	if !ok || ownerName == "" || repoName == "" {
		// ping, or an event that names no repository.
		_, _ = w.Write([]byte("ok"))
		return
	}

	repo, err := repo_model.GetRepositoryByOwnerAndName(req.Context(), ownerName, repoName)
	if err != nil {
		// Not mirrored here (yet) — the reconciler owns creation, not us.
		_, _ = w.Write([]byte("ok"))
		return
	}

	// GetMirrorByRepoID is the single check for "is a pull mirror": a push
	// mirror or a plain repo has no row and must never be queued for a pull.
	if _, err := repo_model.GetMirrorByRepoID(req.Context(), repo.ID); err != nil {
		_, _ = w.Write([]byte("ok"))
		return
	}

	mirror_service.AddPullMirrorToQueue(repo.ID)
	log.Trace("mirror webhook: queued pull for %s", repo.FullName())
	_, _ = w.Write([]byte("queued"))
}

// validMirrorSignature reports whether sigHeader is GitHub's
// "sha256=<hex>" HMAC of body under secret. Constant-time; any malformed
// header is simply invalid.
func validMirrorSignature(secret, sigHeader string, body []byte) bool {
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
