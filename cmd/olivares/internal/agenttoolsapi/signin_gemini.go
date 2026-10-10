// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package agenttoolsapi

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/url"
	"os"
	"path/filepath"

	"github.com/olivaresai/olivares/core/driverfacts"
)

// Gemini CLI 0.62.0 has no login subcommand. Its official ACP authenticate
// method starts Google's native manual OAuth flow with NO_BROWSER=true, writes
// its own credentials and selectedType, and returns an empty result on success.
// This relay opens no conversation and never submits a model prompt.
const geminiSignInInitialize = "{\"jsonrpc\":\"2.0\",\"id\":1,\"method\":\"initialize\",\"params\":{\"protocolVersion\":1,\"clientCapabilities\":{}}}\n"
const geminiSignInAuthenticate = "{\"jsonrpc\":\"2.0\",\"id\":2,\"method\":\"authenticate\",\"params\":{\"methodId\":\"oauth-personal\"}}\n"

func pasteSignIn(driver string) bool {
	facts, _ := driverfacts.Lookup(driver)
	return facts.SignIn == driverfacts.SignInPaste
}

func geminiAuthorizationURL(raw string) bool {
	u, err := url.Parse(raw)
	return err == nil && u.Scheme == "https" && u.Host == "accounts.google.com" && u.User == nil
}

// Native readline prompts have no final newline. Preserve the usual bounded
// scanner, but emit this one complete prompt so a rejected code can be retried.
func geminiSignInSplit(data []byte, atEOF bool) (int, []byte, error) {
	const prompt = "Enter the authorization code: "
	if i := bytes.Index(data, []byte(prompt)); i >= 0 {
		end := i + len(prompt)
		if n := bytes.IndexByte(data[:end], '\n'); n >= 0 {
			return n + 1, data[:n], nil
		}
		return end, data[:end], nil
	}
	return bufio.ScanLines(data, atEOF)
}

// A native file is a readiness hint, never proof that Google's refresh token is
// still valid. Its contents are exclusively the CLI's. An unsafe/unreadable file
// is an error; another profile's home cannot qualify this selection.
func geminiSignInStatus(out SignInStatus, configDir string) (SignInStatus, error) {
	method, err := geminiSavedAuthType(filepath.Join(configDir, "settings.json"))
	if err != nil {
		return out, err
	}
	if method != "oauth-personal" {
		return out, nil
	}
	info, err := os.Lstat(filepath.Join(configDir, "oauth_creds.json"))
	if errors.Is(err, os.ErrNotExist) {
		return out, nil
	}
	if err != nil || !info.Mode().IsRegular() || info.Size() == 0 {
		return out, errSignInStatus
	}
	out.SignedIn, out.Method = true, "Google account"
	return out, nil
}

func (m *Module) geminiSignInResponse(s *SignIn, line string) bool {
	var response struct {
		JSONRPC string          `json:"jsonrpc"`
		ID      int             `json:"id"`
		Result  json.RawMessage `json:"result"`
		Error   json.RawMessage `json:"error"`
	}
	if json.Unmarshal([]byte(line), &response) != nil || response.JSONRPC != "2.0" || (response.ID != 1 && response.ID != 2) {
		return false
	}
	fail := func() {
		m.finishSignIn(s.ID, signInFailed, "Gemini CLI could not complete its Google sign-in. Start it again.")
		s.cancel()
	}
	if response.ID != s.geminiStage || len(response.Error) != 0 || len(response.Result) == 0 || response.Result[0] != '{' {
		fail()
		return true
	}
	if response.ID == 1 {
		var init struct {
			ProtocolVersion int `json:"protocolVersion"`
			AuthMethods     []struct {
				ID string `json:"id"`
			} `json:"authMethods"`
		}
		if json.Unmarshal(response.Result, &init) != nil || init.ProtocolVersion != 1 {
			fail()
			return true
		}
		for _, method := range init.AuthMethods {
			if method.ID == "oauth-personal" {
				s.geminiStage = 2
				if _, err := io.WriteString(s.stdin, geminiSignInAuthenticate); err != nil {
					fail()
				}
				return true
			}
		}
		fail()
		return true
	}
	status, err := geminiSignInStatus(SignInStatus{}, s.configDir)
	if err != nil || !status.SignedIn {
		fail()
		return true
	}
	m.finishSignIn(s.ID, signInDone, "")
	s.cancel() // Authentication is complete; the ACP server otherwise keeps running.
	return true
}

// With NO_BROWSER, 0.62 starts a saved Google OAuth login before its ACP
// server. Sending initialize then would become an authorization code. Do not
// guess startup timing or rewrite the CLI's native settings to bypass it.
func geminiSignInStartRefusal(configDir, userHome string) error {
	paths := []string{"/etc/gemini-cli/system-defaults.json", "/etc/gemini-cli/settings.json", filepath.Join(configDir, "settings.json"), filepath.Join(userHome, ".gemini", "settings.json")}
	for _, path := range paths {
		method, err := geminiSavedAuthType(path)
		if err != nil {
			return err
		}
		if method == "oauth-personal" {
			return errGeminiSavedGoogleLogin
		}
	}
	return nil
}

func geminiSavedAuthType(path string) (string, error) {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return "", nil
	}
	if err != nil || !info.Mode().IsRegular() || info.Size() > 1<<20 {
		return "", errSignInStatus
	}
	f, err := os.Open(path)
	if err != nil {
		return "", errSignInStatus
	}
	raw, err := io.ReadAll(io.LimitReader(f, (1<<20)+1))
	closeErr := f.Close()
	if err != nil || closeErr != nil || len(raw) > 1<<20 {
		return "", errSignInStatus
	}
	// Native JSON keys are exact-case, with the last duplicate winning.
	for _, key := range []string{"security", "auth", "selectedType"} {
		var object map[string]json.RawMessage
		if json.Unmarshal(raw, &object) != nil || object == nil {
			return "", errSignInStatus
		}
		raw = object[key]
		if len(raw) == 0 {
			return "", nil
		}
	}
	var method string
	if json.Unmarshal(raw, &method) != nil {
		return "", errSignInStatus
	}
	return method, nil
}

var errGeminiSavedGoogleLogin = errors.New("Gemini CLI already selects Google sign-in in this home; use its existing login, or create a new Google profile to sign in again")
