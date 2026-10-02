// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package api

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"net/http"
	"strings"
	"time"

	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

const browserSessionCookie = "__Host-olivares-session"

type browserSessionContextKey struct{}

func browserCredential(r *http.Request) string {
	token, _ := r.Context().Value(browserSessionContextKey{}).(string)
	return token
}

func wantsBrowserSession(r *http.Request) bool {
	return r.Header.Get("X-Olivares-Session") == "cookie" || browserCredential(r) != ""
}

// CSRF tokens are bound to the random credential, but cannot authenticate alone.
// Deriving them avoids a second session store and survives restart and HA routing.
func browserCSRF(token string) string {
	sum := sha256.Sum256([]byte("olivares-browser-csrf-v1\x00" + token))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

// BrowserSameOrigin checks the engine's shared origin boundary before a browser
// sign-in changes session state. Module sign-in handlers use this same check.
func BrowserSameOrigin(r *http.Request) bool {
	if site := r.Header.Get("Sec-Fetch-Site"); site != "" && site != "same-origin" && site != "none" {
		return false
	}
	origin := r.Header.Get("Origin")
	return origin == "" || origin == schemeHost(r)
}

func browserSafeMethod(method string) bool {
	return method == http.MethodGet || method == http.MethodHead || method == http.MethodOptions
}

func clearBrowserCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name: browserSessionCookie, Path: "/", HttpOnly: true, Secure: true,
		SameSite: http.SameSiteStrictMode, MaxAge: -1, Expires: time.Unix(1, 0).UTC(),
	})
}

// SessionEnvelope sets the protected cookie and returns only session metadata
// and a CSRF token when the request selects X-Olivares-Session: cookie. CLI callers
// retain the bearer envelope. Callers must check BrowserSameOrigin before issuing
// a browser session. Expires mirrors the database; there is no separate TTL.
func SessionEnvelope(w http.ResponseWriter, r *http.Request, token string, sess model.AuthSession) map[string]any {
	w.Header().Set("Cache-Control", "no-store")
	out := map[string]any{"session_id": sess.ID.String(), "expires_at": sess.ExpiresAt.String()}
	if wantsBrowserSession(r) {
		http.SetCookie(w, &http.Cookie{
			Name: browserSessionCookie, Value: token, Path: "/", HttpOnly: true,
			Secure: true, SameSite: http.SameSiteStrictMode, Expires: sess.ExpiresAt.Time(),
		})
		out["csrf_token"] = browserCSRF(token)
	} else {
		out["token"] = token
	}
	return out
}

// handleBrowserSession restores metadata after reload, or migrates a legacy
// bearer once. Migration rotates the secret and preserves the exact expiry.
func (s *Server) handleBrowserSession(w http.ResponseWriter, r *http.Request) {
	p, ok := principalFrom(r.Context())
	if !ok || p.Kind != auth.KindUser {
		s.writeError(w, r, auth.ErrUnauthenticated)
		return
	}
	if !BrowserSameOrigin(r) {
		s.writeError(w, r, errForbidden)
		return
	}
	if r.Method == http.MethodPost {
		if browserCredential(r) != "" || r.Header.Get("X-Olivares-Session") != "cookie" {
			s.badRequest(w, r, "migration requires a bearer session and cookie transport")
			return
		}
		token, sess, err := s.authr.MigrateBrowserSession(r.Context(), p)
		if err != nil {
			s.writeError(w, r, err)
			return
		}
		writeJSON(w, http.StatusOK, SessionEnvelope(w, r, token, sess))
		return
	}
	token := browserCredential(r)
	if token == "" {
		s.writeError(w, r, auth.ErrUnauthenticated)
		return
	}
	var sess model.AuthSession
	err := s.st.AuthView(r.Context(), func(as store.AuthScope) error {
		var err error
		sess, err = as.Sessions().Get(r.Context(), p.CredID)
		return err
	})
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, map[string]any{
		"session_id": sess.ID.String(), "expires_at": sess.ExpiresAt.String(), "csrf_token": browserCSRF(token),
	})
}

// Ignore browser cookies on public login/bootstrap paths so an expired cookie
// cannot prevent recovery. Protocol MCP requests keep their independent auth.
func browserCookieApplies(r *http.Request) bool {
	if !strings.HasPrefix(r.URL.Path, "/v1/") {
		return false
	}
	switch r.URL.Path {
	case "/v1/auth/login", "/v1/setup", "/v1/server-info", "/v1/auth/totp/challenge", "/v1/invites/accept":
		return false
	}
	if r.Header.Get("X-Olivares-Session") == "cookie" &&
		(r.URL.Path == "/v1/auth/totp/enrol" || r.URL.Path == "/v1/auth/totp/activate") {
		return false // pending-login completion authenticates its mfa_token body
	}
	return !strings.HasPrefix(r.URL.Path, "/v1/auth/federation/")
}

func withBrowserCredential(r *http.Request, token string) *http.Request {
	return r.WithContext(context.WithValue(r.Context(), browserSessionContextKey{}, token))
}

func validBrowserCSRF(r *http.Request, token string) bool {
	return BrowserSameOrigin(r) && (browserSafeMethod(r.Method) ||
		subtle.ConstantTimeCompare([]byte(r.Header.Get("X-CSRF-Token")), []byte(browserCSRF(token))) == 1)
}
