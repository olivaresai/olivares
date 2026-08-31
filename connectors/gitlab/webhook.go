// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package gitlab

import (
	"crypto/subtle"
	"encoding/json"
	"io"
	"net/http"

	"github.com/olivaresai/olivares/sdk"
)

type pushHook struct {
	Ref       string      `json:"ref"`
	Project   projectRef  `json:"project"`
	UserName  string      `json:"user_name"`
	UserLogin string      `json:"user_username"`
	Commits   []commitRef `json:"commits"`
}

type mergeRequestHook struct {
	ObjectKind       string       `json:"object_kind"`
	ObjectAttributes mrAttributes `json:"object_attributes"`
	Project          projectRef   `json:"project"`
	User             glUserRef    `json:"user"`
}

type projectRef struct {
	PathWithNamespace string `json:"path_with_namespace"`
	WebURL            string `json:"web_url"`
}

type commitRef struct {
	ID      string `json:"id"`
	Message string `json:"message"`
	Author  struct {
		Name  string `json:"name"`
		Email string `json:"email"`
	} `json:"author"`
}

type glUserRef struct {
	Username string `json:"username"`
	Name     string `json:"name"`
}

type mrAttributes struct {
	Action       string `json:"action"`
	State        string `json:"state"`
	TargetBranch string `json:"target_branch"`
	SourceBranch string `json:"source_branch"`
}

// handleWebhook returns an HTTP handler that verifies the X-Gitlab-Token header,
// dispatches by X-Gitlab-Event, and emits edges to the sink.
func (s *Source) handleWebhook(sink sdk.Sink) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}

		if !verifyToken(r.Header.Get("X-Gitlab-Token"), s.webhookSecret) {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}

		body, err := io.ReadAll(io.LimitReader(r.Body, 10<<20))
		if err != nil {
			http.Error(w, "read error", http.StatusBadRequest)
			return
		}

		eventType := r.Header.Get("X-Gitlab-Event")
		switch eventType {
		case "Push Hook", "Tag Push Hook":
			var ev pushHook
			if err := json.Unmarshal(body, &ev); err != nil {
				http.Error(w, "bad payload", http.StatusBadRequest)
				return
			}
			for _, edge := range s.buildPushEdges(ev) {
				if err := sink.Emit(r.Context(), edge); err != nil {
					http.Error(w, "emit error", http.StatusInternalServerError)
					return
				}
			}

		case "Merge Request Hook":
			var ev mergeRequestHook
			if err := json.Unmarshal(body, &ev); err != nil {
				http.Error(w, "bad payload", http.StatusBadRequest)
				return
			}
			edges := s.buildMREdges(ev)
			for _, edge := range edges {
				if err := sink.Emit(r.Context(), edge); err != nil {
					http.Error(w, "emit error", http.StatusInternalServerError)
					return
				}
			}

		default:
			// Unknown events are accepted but ignored.
		}

		w.WriteHeader(http.StatusOK)
	}
}

// verifyToken compares the received token against the expected secret using
// constant-time comparison to prevent timing attacks.
func verifyToken(got, expected string) bool {
	return subtle.ConstantTimeCompare([]byte(got), []byte(expected)) == 1
}
