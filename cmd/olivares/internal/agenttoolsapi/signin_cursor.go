// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package agenttoolsapi

import (
	"encoding/json"
	"net/url"
	"strings"
)

// cursorLoginURL reports whether raw is the link `agent login` prints for the browser
// (https://cursor.com/loginDeepControl?challenge=..., measured on 2026.10.22-27dcf5f). The
// tool prints other links too (a changelog, a help page); only this one is offered as the
// sign-in.
func cursorLoginURL(raw string) bool {
	u, err := url.Parse(raw)
	return err == nil && u.Scheme == "https" && u.User == nil && u.Host == "cursor.com" && u.Path == "/loginDeepControl"
}

// cursorSignInStatus reads `agent status --format json` (Cursor CLI 2026.10.22-27dcf5f). Its
// document names the status ("authenticated", "partially-authenticated" or
// "unauthenticated") and isAuthenticated; a signed-in account also carries userInfo. Signed
// out, the tool was also seen to answer the line `Not logged in` and the line
// `Error: Authentication required. Please run 'agent login' first...`, and those are signed
// out too. Anything else is unrecognized, which is neither signed in nor signed out: a status
// value nobody has seen, a signed-in status without isAuthenticated, a proxy refusal or a
// stack trace that merely mentions authentication. A partial login (no refresh token) cannot
// run a session. The caller has already refused a command that failed.
func cursorSignInStatus(out SignInStatus, raw []byte) SignInStatus {
	lines := strings.Split(ansiEscape.ReplaceAllString(string(raw), ""), "\n")
	for i, line := range lines {
		text := strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(text, "{"):
			return cursorStatusDocument(out, strings.Join(lines[i:], "\n"))
		case text == "Not logged in", strings.HasPrefix(text, "Error: Authentication required. Please run"):
			return out
		}
	}
	out.unrecognized = true
	return out
}

func cursorStatusDocument(out SignInStatus, text string) SignInStatus {
	var doc struct {
		Status          string `json:"status"`
		IsAuthenticated *bool  `json:"isAuthenticated"`
		HasAccessToken  *bool  `json:"hasAccessToken"`
		HasRefreshToken *bool  `json:"hasRefreshToken"`
		UserInfo        *struct {
			Email string `json:"email"`
		} `json:"userInfo"`
	}
	if err := json.Unmarshal([]byte(text), &doc); err != nil {
		out.unrecognized = true
		return out
	}
	switch doc.Status {
	case "unauthenticated", "partially-authenticated":
		if doc.IsAuthenticated == nil || *doc.IsAuthenticated {
			out.unrecognized = true
		}
		return out
	case "authenticated":
		if doc.IsAuthenticated == nil || !*doc.IsAuthenticated ||
			doc.HasAccessToken == nil || !*doc.HasAccessToken ||
			doc.HasRefreshToken == nil || !*doc.HasRefreshToken {
			out.unrecognized = true
			return out
		}
		out.SignedIn, out.Method = true, "Cursor account"
		if doc.UserInfo != nil {
			out.Account = doc.UserInfo.Email
		}
		return out
	}
	out.unrecognized = true
	return out
}
