// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package gitlab

import (
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/olivaresai/olivares/connectors/internal/replay"
	"github.com/olivaresai/olivares/sdk"
)

// signatureTolerance bounds how far webhook-timestamp may be from now, the
// Standard Webhooks default GitLab follows.
const signatureTolerance = 5 * time.Minute

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

// handleWebhook returns an HTTP handler that verifies the X-Gitlab-Token header
// and, when a signing token is set, the webhook-signature; drops a repeated
// delivery; dispatches by X-Gitlab-Event; and emits edges to the sink.
func (s *Source) handleWebhook(sink sdk.Sink) http.HandlerFunc {
	seen := replay.New()
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

		// The signed webhook-id alone is the replay key when signatures are on:
		// the unsigned Idempotency-Key (the same value from GitLab) and
		// X-Gitlab-Event could be changed.
		id, event := r.Header.Get("webhook-id"), r.Header.Get("X-Gitlab-Event")
		if s.signingKey != nil {
			if !verifySigned(r.Header, body, s.signingKey, time.Now()) {
				http.Error(w, "forbidden", http.StatusForbidden)
				return
			}
			event = ""
		} else if k := r.Header.Get("Idempotency-Key"); k != "" {
			id = k
		}
		seen.Once(w, event, id, func(w http.ResponseWriter) { s.serveEvent(w, r, body, sink) })
	}
}

// serveEvent dispatches one authenticated delivery by X-Gitlab-Event.
func (s *Source) serveEvent(w http.ResponseWriter, r *http.Request, body []byte, sink sdk.Sink) {
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

	case "Pipeline Hook", "Job Hook":
		sample, ok, err := parseGitLabEvidence(eventType, body)
		if err != nil {
			http.Error(w, "bad payload", http.StatusBadRequest)
			return
		}
		if ok {
			if err := sink.Emit(r.Context(), sample); err != nil {
				http.Error(w, "emit error", http.StatusInternalServerError)
				return
			}
		}

	default:
		// Unknown events are accepted but ignored.
	}

	w.WriteHeader(http.StatusOK)
}

// verifySigned checks a GitLab 19.x signed delivery: one of the space-separated
// webhook-signature values (in one header line or several) is "v1," + base64(HMAC-SHA256(key,
// "<webhook-id>.<webhook-timestamp>.<body>")), and the timestamp is within
// signatureTolerance of now.
func verifySigned(h http.Header, body, key []byte, now time.Time) bool {
	id, ts := h.Get("webhook-id"), h.Get("webhook-timestamp")
	sec, err := strconv.ParseInt(ts, 10, 64)
	if id == "" || err != nil {
		return false
	}
	if d := now.Sub(time.Unix(sec, 0)); d > signatureTolerance || d < -signatureTolerance {
		return false
	}
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte(id + "." + ts + "."))
	mac.Write(body)
	want := []byte("v1," + base64.StdEncoding.EncodeToString(mac.Sum(nil)))
	for _, sig := range strings.Fields(strings.Join(h.Values("webhook-signature"), " ")) {
		if hmac.Equal([]byte(sig), want) {
			return true
		}
	}
	return false
}

// verifyToken compares the received token against the expected secret using
// constant-time comparison to prevent timing attacks.
func verifyToken(got, expected string) bool {
	return subtle.ConstantTimeCompare([]byte(got), []byte(expected)) == 1
}
