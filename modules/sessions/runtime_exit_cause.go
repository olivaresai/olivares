// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"bytes"
	"encoding/json"
	"regexp"
	"strings"
	"unicode/utf8"
)

// exitCauseMax bounds the cause recorded on a run: one line an operator reads.
const exitCauseMax = 240

// credentialShaped masks anything that looks like a key or token before a
// child's own words are recorded and shown: provider key prefixes, bearer
// values, and long unbroken runs of key alphabet.
var credentialShaped = regexp.MustCompile(`(?i)(bearer\s+\S+|sk-[a-z0-9_\-]{6,}|olv[a-z]*_[a-z0-9_\-]{6,}|[a-z0-9_\-+/=]{32,})`)

// exitCause is what a child said last before it exited on its own: the error of
// a stream-json result frame (Claude Code reports a failed start there), else the
// last non-empty line it wrote to stderr. One line, bounded, with anything shaped
// like a credential masked, because it becomes the run's reason and is shown to
// the operator (HU-06: a refused or failed launch says why). Empty when the child
// said nothing.
func exitCause(r *outputRing) string {
	if r == nil {
		return ""
	}
	r.mu.Lock()
	frames := append([]seqFrame(nil), r.frames...)
	r.mu.Unlock()
	for i := len(frames) - 1; i >= 0; i-- {
		if frames[i].Stream != streamStdout {
			continue
		}
		var res struct {
			Type    string `json:"type"`
			IsError bool   `json:"is_error"`
			Result  string `json:"result"`
		}
		if json.Unmarshal(bytes.TrimSpace(frames[i].Data), &res) == nil &&
			res.Type == "result" && res.IsError && strings.TrimSpace(res.Result) != "" {
			return clipCause(res.Result)
		}
	}
	for i := len(frames) - 1; i >= 0; i-- {
		if frames[i].Stream != streamStderr {
			continue
		}
		lines := strings.Split(string(frames[i].Data), "\n")
		for j := len(lines) - 1; j >= 0; j-- {
			if line := strings.TrimSpace(lines[j]); line != "" {
				return clipCause(line)
			}
		}
	}
	return ""
}

func clipCause(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	s = credentialShaped.ReplaceAllString(s, "…")
	if len(s) > exitCauseMax {
		s = s[:exitCauseMax]
		for !utf8.ValidString(s) {
			s = s[:len(s)-1]
		}
		s += "…"
	}
	return s
}

// signInFailures are the words each coding tool exits with when it has no login it
// can use. The tool then names its OWN step ("Please run /login", "codex login"),
// which in Olivares is the tool's sign-in under AI tools or a key in Providers.
var signInFailures = map[string][]string{
	providerDriverClaude:   {"not logged in", "please run /login", "invalid api key", "oauth token has expired", "authentication_error"},
	providerDriverCodex:    {"not logged in", "codex login", "please log in", "unauthorized"},
	"grok":                 {"not logged in", "grok login", "unauthenticated", "unauthorized"},
	providerDriverOpenCode: {"no provider", "api key", "unauthorized", "not authenticated"},
}

var signInToolNames = map[string]string{
	providerDriverClaude: "Claude Code", providerDriverCodex: "Codex", "grok": "Grok Build", providerDriverOpenCode: "OpenCode",
}

// productCause turns a tool's own "not signed in" exit into the step that fixes it
// in Olivares (CLX on 08b: the run said "Please run /login", a step the product does
// not have). The vendor's words stay as the detail, already clipped and redacted by
// clipCause. Any other cause is returned as it is.
func productCause(driver, cause string) string {
	name, known := signInToolNames[driver]
	if !known || cause == "" {
		return cause
	}
	lower := strings.ToLower(cause)
	hit := false
	for _, w := range signInFailures[driver] {
		if strings.Contains(lower, w) {
			hit = true
			break
		}
	}
	if !hit {
		return cause
	}
	if driver == providerDriverOpenCode {
		return name + " has no credential it can use: add a key or a local model (Ollama) in Providers and use it for this profile. " + name + " said: " + cause
	}
	return name + " is not signed in for this session: sign it in under AI tools (olivares tool login " + driver + "), or use an API key from Providers. " + name + " said: " + cause
}
