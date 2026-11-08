// Copyright 2026 Hanzo AI, Inc. All rights reserved.
// SPDX-License-Identifier: MIT

package misc

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"

	repo_model "github.com/hanzoai/git/models/repo"
	"github.com/hanzoai/git/modules/log"
	"github.com/hanzoai/git/modules/setting"
	mirror_service "github.com/hanzoai/git/services/mirror"
)

const (
	// maxSyncBody bounds the read: GitHub caps a webhook payload at 25 MiB, and an
	// unbounded read ahead of authentication is a memory DoS.
	maxSyncBody = 25 << 20
	// keepSyncBody is how much of the payload is held to read the repository from.
	// The rest is only hashed, so the memory a delivery takes does not grow with it.
	keepSyncBody = 1 << 20
)

// Sync is the one entry point for "this repo changed upstream, take it now".
// It exists because an org-level webhook is a single static URL that cannot
// carry a token header; authenticated callers already have the software's
// per-repo mirror-sync APIs, so this adds no second door for them.
//
// Direction is a property of the repo, not of this route — a pull mirror
// fetches, and that fetch propagates to its push mirrors (services/mirror), so
// one upstream push reaches every platform we mirror to in one hop. A regular
// repo pushes to its platform through a push mirror; when that platform is the
// one speaking, the event brings what it gained in from the push mirror's remote
// as pushes by the sync user, so a push to either side reaches the other.
//
// Mounted ahead of the session middleware because the credential is the payload
// itself: HMAC-SHA256 over the exact bytes in X-Hub-Signature-256, keyed by
// [mirror] SYNC_SECRET. Fail-closed — an unset secret refuses everything.
//
// Only a push, a branch or tag created, and a branch or tag deleted change refs,
// so only those fetch; any other verified event is answered and ignored.
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

	head, valid, err := readVerified(secret, req.Header.Get("X-Hub-Signature-256"), http.MaxBytesReader(w, req.Body, maxSyncBody), keepSyncBody)
	if err != nil {
		if _, tooBig := errors.AsType[*http.MaxBytesError](err); tooBig {
			http.Error(w, "payload too large", http.StatusRequestEntityTooLarge)
			return
		}
		http.Error(w, "unreadable body", http.StatusBadRequest)
		return
	}
	if !valid {
		http.Error(w, "invalid signature", http.StatusUnauthorized)
		return
	}

	switch req.Header.Get("X-GitHub-Event") {
	case "push", "create", "delete":
	default:
		_, _ = w.Write([]byte("ok"))
		return
	}

	ownerName, repoName, ok := repoFromPayload(head)
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
	// mirror to the same repo on GitHub is the way back in. A repo with
	// neither must never be queued for a fetch.
	_, err = repo_model.GetMirrorByRepoID(req.Context(), repo.ID)
	switch {
	case err == nil:
		queuePull(repo.ID)
		log.Trace("sync: queued pull for %s", repo.FullName())
		_, _ = w.Write([]byte("queued"))
	case errors.Is(err, repo_model.ErrMirrorNotExist):
		if m := mirror_service.GitHubPushMirror(req.Context(), repo); m != nil {
			queueInbound(m.ID)
			log.Trace("sync: queued inbound for %s", repo.FullName())
			_, _ = w.Write([]byte("queued"))
			return
		}
		_, _ = w.Write([]byte("ok"))
	default:
		// Not "no mirror": say so, so GitHub shows the delivery red and can redeliver it.
		log.Error("sync: unable to read the mirror of %s: %v", repo.FullName(), err)
		http.Error(w, "try again", http.StatusInternalServerError)
	}
}

// The queue entry points, held in variables so a test can see what was queued
// without running a sync.
var (
	queuePull    = mirror_service.AddPullMirrorToEventQueue
	queueInbound = mirror_service.AddInboundMirrorToEventQueue
)

// repoFromPayload reads the repository a push event names, a field every
// platform whose events we accept spells the same way. It reads from the start
// of the payload and stops at the repository, so a payload cut short after it
// is still read: GitHub puts the repository ahead of the commits.
func repoFromPayload(head []byte) (ownerName, repoName string, ok bool) {
	dec := json.NewDecoder(bytes.NewReader(head))
	if tok, err := dec.Token(); err != nil || tok != json.Delim('{') {
		return "", "", false
	}
	for dec.More() {
		key, err := dec.Token()
		if err != nil {
			return "", "", false
		}
		if key != "repository" {
			var skip json.RawMessage
			if err := dec.Decode(&skip); err != nil {
				return "", "", false
			}
			continue
		}
		var repository struct {
			FullName string `json:"full_name"`
		}
		if err := dec.Decode(&repository); err != nil {
			return "", "", false
		}
		ownerName, repoName, ok = strings.Cut(repository.FullName, "/")
		return ownerName, repoName, ok && ownerName != "" && repoName != ""
	}
	return "", "", false
}

// readVerified reads all of body through the HMAC under secret, and reports
// whether sigHeader, "sha256=<hex>", is the signature of every byte of it. It
// returns the first keep bytes of the body, no more, whatever its size. A
// malformed header is invalid without reading anything.
func readVerified(secret, sigHeader string, body io.Reader, keep int) (head []byte, valid bool, err error) {
	hexDigest, ok := strings.CutPrefix(sigHeader, "sha256=")
	if !ok {
		return nil, false, nil
	}
	sent, err := hex.DecodeString(hexDigest)
	if err != nil {
		return nil, false, nil
	}

	mac := hmac.New(sha256.New, []byte(secret))
	var kept bytes.Buffer
	if _, err := io.Copy(io.MultiWriter(mac, &limitedWriter{buf: &kept, left: keep}), body); err != nil {
		return nil, false, err
	}
	return kept.Bytes(), hmac.Equal(sent, mac.Sum(nil)), nil
}

// limitedWriter keeps the first left bytes written to it and drops the rest, without failing.
type limitedWriter struct {
	buf  *bytes.Buffer
	left int
}

func (w *limitedWriter) Write(p []byte) (int, error) {
	if w.left > 0 {
		n := min(len(p), w.left)
		w.buf.Write(p[:n])
		w.left -= n
	}
	return len(p), nil
}
