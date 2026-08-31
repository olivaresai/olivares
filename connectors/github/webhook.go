// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package github

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"strings"

	"github.com/olivaresai/olivares/sdk"
)

// pushEvent is the GitHub push webhook payload (subset).
type pushEvent struct {
	Ref        string      `json:"ref"`
	Repository repoRef     `json:"repository"`
	Pusher     userRef     `json:"pusher"`
	Sender     userRef     `json:"sender"`
	Commits    []commitRef `json:"commits"`
}

// pullRequestEvent is the GitHub pull_request webhook payload (subset).
type pullRequestEvent struct {
	Action      string  `json:"action"`
	PullRequest prRef   `json:"pull_request"`
	Repository  repoRef `json:"repository"`
	Sender      userRef `json:"sender"`
}

// repoRef identifies a repository in webhook payloads.
type repoRef struct {
	FullName string `json:"full_name"`
	Private  bool   `json:"private"`
}

// userRef identifies a user in webhook payloads. Login is the opaque
// username; Name and Email are used only for commit-author correlation
// and are never emitted.
type userRef struct {
	Login string `json:"login"`
	Name  string `json:"name"`
	Email string `json:"email"`
}

// commitRef is a single commit in a push event.
type commitRef struct {
	ID      string  `json:"id"`
	Message string  `json:"message"`
	Author  userRef `json:"author"`
}

// prRef is the pull_request object in a PR event.
type prRef struct {
	Merged bool      `json:"merged"`
	Base   branchRef `json:"base"`
	Head   branchRef `json:"head"`
}

// branchRef identifies a branch.
type branchRef struct {
	Ref string `json:"ref"`
}

const maxWebhookBody = 10 << 20 // 10 MiB

// handleWebhook returns an http.HandlerFunc that processes GitHub webhook
// deliveries, verifies the HMAC-SHA256 signature, and emits edges.
func (s *Source) handleWebhook(sink sdk.Sink) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}

		body, err := io.ReadAll(io.LimitReader(r.Body, maxWebhookBody))
		if err != nil {
			http.Error(w, "read error", http.StatusBadRequest)
			return
		}

		sig := r.Header.Get("X-Hub-Signature-256")
		if !verifySignature(body, sig, s.webhookSecret) {
			http.Error(w, "invalid signature", http.StatusForbidden)
			return
		}

		event := r.Header.Get("X-GitHub-Event")
		switch event {
		case "push":
			s.handlePush(r, w, body, sink)
		case "pull_request":
			s.handlePullRequest(r, w, body, sink)
		default:
			// Accept and ignore unhandled event types.
			w.WriteHeader(http.StatusOK)
		}
	}
}

// handlePush processes a push webhook event.
func (s *Source) handlePush(r *http.Request, w http.ResponseWriter, body []byte, sink sdk.Sink) {
	var ev pushEvent
	if err := json.Unmarshal(body, &ev); err != nil {
		http.Error(w, "bad payload", http.StatusBadRequest)
		return
	}
	edges := s.buildPushEdges(ev)
	for _, e := range edges {
		if err := sink.Emit(r.Context(), e); err != nil {
			http.Error(w, "emit error", http.StatusInternalServerError)
			return
		}
	}
	w.WriteHeader(http.StatusOK)
}

// handlePullRequest processes a pull_request webhook event.
func (s *Source) handlePullRequest(r *http.Request, w http.ResponseWriter, body []byte, sink sdk.Sink) {
	var ev pullRequestEvent
	if err := json.Unmarshal(body, &ev); err != nil {
		http.Error(w, "bad payload", http.StatusBadRequest)
		return
	}
	edges := s.buildPREdges(ev)
	for _, e := range edges {
		if err := sink.Emit(r.Context(), e); err != nil {
			http.Error(w, "emit error", http.StatusInternalServerError)
			return
		}
	}
	w.WriteHeader(http.StatusOK)
}

// verifySignature checks the HMAC-SHA256 signature of a webhook payload.
func verifySignature(payload []byte, signature, secret string) bool {
	if !strings.HasPrefix(signature, "sha256=") {
		return false
	}
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(payload)
	expected := "sha256=" + hex.EncodeToString(mac.Sum(nil))
	return hmac.Equal([]byte(expected), []byte(signature))
}
