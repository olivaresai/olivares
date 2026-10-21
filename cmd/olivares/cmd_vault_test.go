// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/cmd/olivares/exitcode"
)

// vaultCLIValue is the password inside vaultCLIItem, the document a login is stored as.
const (
	vaultCLIValue = "vault-cli-fixture-value-2386"
	vaultCLIItem  = `{"type":"login","username":"ann","password":"` + vaultCLIValue + `"}`
)

// vaultCLISession boots a real engine and returns the CLI credentials of its first
// person, that person's user id, and a function that stores a vault item for them
// through the API (writing items is not part of this command).
func vaultCLISession(t *testing.T) (creds []string, userID string, store func(name, value string)) {
	t.Helper()
	_, creds, userID, store = vaultCLIEngine(t)
	return creds, userID, store
}

// vaultCLIEngine is vaultCLISession that also hands back the engine, for a test that reads
// its ledger.
func vaultCLIEngine(t *testing.T) (eng *engine, creds []string, userID string, store func(name, value string)) {
	t.Helper()
	eng, creds = mcpSecretEngine(t)
	token, err := os.ReadFile(creds[3])
	if err != nil {
		t.Fatal(err)
	}
	tenant := creds[5]
	code, who, raw := doDemoViewJSON(t, eng.api.Handler(), "GET", "/v1/auth/whoami", string(token), tenant, nil)
	userID, _ = who["user_id"].(string)
	if code != http.StatusOK || userID == "" {
		t.Fatalf("whoami = %d %s", code, raw)
	}
	store = func(name, value string) {
		t.Helper()
		code, _, raw := doDemoViewJSON(t, eng.api.Handler(), "PUT", "/v1/console/secrets?scope=tenant", string(token), tenant,
			map[string]any{"name": name, "value": value})
		if code != http.StatusOK {
			t.Fatalf("store %s = %d %s", name, code, raw)
		}
	}
	return eng, creds, userID, store
}

// `olivares vault get` prints the value of the caller's own item and nothing else on
// stdout, so it pipes; -o json names the item; the value never reaches stderr.
func TestVaultGetPrintsOnlyTheCallersOwnItem(t *testing.T) {
	creds, userID, store := vaultCLISession(t)
	store("vault/"+userID+"/bank", vaultCLIItem)
	run := func(args ...string) (string, string, error) {
		t.Helper()
		return execSessionCLI(t, nil, append(append([]string{"vault", "get"}, creds...), args...)...)
	}

	out, errb, err := run("bank")
	if err != nil || out != vaultCLIValue+"\n" || strings.Contains(errb, vaultCLIValue) {
		t.Fatalf("vault get bank: err=%v out=%q stderr=%q", err, out, errb)
	}
	out, _, err = run("vault/"+userID+"/bank", "-o", "json")
	var got map[string]string
	if err != nil || json.Unmarshal([]byte(out), &got) != nil ||
		got["name"] != "vault/"+userID+"/bank" || got["value"] != vaultCLIValue {
		t.Fatalf("vault get -o json: err=%v out=%q", err, out)
	}

	_, errb, err = run("missing")
	if exitcode.From(err) != exitcode.NotFound || strings.Contains(err.Error()+errb, vaultCLIValue) {
		t.Fatalf("vault get missing: err=%v, want exit %d", err, exitcode.NotFound)
	}
	// Someone else's namespace is refused, whatever the spelling.
	_, errb, err = run("vault/00000000-0000-0000-0000-000000000000/bank")
	if exitcode.From(err) != exitcode.Auth || strings.Contains(err.Error()+errb, vaultCLIValue) {
		t.Fatalf("vault get of another namespace: err=%v, want exit %d", err, exitcode.Auth)
	}
}

// An API token has no vault: the command says so without asking the engine to reveal,
// and an engine reply with no value is an error, never an empty line and exit 0.
func TestVaultGetRefusesATokenAndAnEmptyReply(t *testing.T) {
	creds, userID, store := vaultCLISession(t)
	store("vault/"+userID+"/bank", vaultCLIItem)
	tokenFile := creds[3]
	session, err := os.ReadFile(tokenFile)
	if err != nil {
		t.Fatal(err)
	}
	// Mint an API token from the person's session through the engine under test.
	server := creds[1]
	req, _ := http.NewRequest("POST", server+"/v1/tokens", strings.NewReader(`{"name":"ci","tenant":"`+creds[5]+`","role":"admin"}`))
	req.Header.Set("Authorization", "Bearer "+string(session))
	req.Header.Set("Content-Type", "application/json")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = res.Body.Close() }()
	var issued map[string]any
	if err := json.NewDecoder(res.Body).Decode(&issued); err != nil || issued["token"] == nil {
		t.Fatalf("issue token: status %d, %v", res.StatusCode, err)
	}
	_, errb, err := execSessionCLI(t, nil, "vault", "get", "--server", server, "--token", issued["token"].(string), "--tenant", creds[5], "bank")
	if exitcode.From(err) != exitcode.Auth || strings.Contains(err.Error()+errb, vaultCLIValue) {
		t.Fatalf("vault get with an API token: err=%v, want exit %d", err, exitcode.Auth)
	}

	empty := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/auth/whoami" {
			_, _ = w.Write([]byte(`{"kind":"user","user_id":"u-1"}`))
			return
		}
		_, _ = w.Write([]byte(`{"name":"vault/u-1/bank"}`))
	}))
	t.Cleanup(empty.Close)
	out, _, err := execSessionCLI(t, nil, "vault", "get", "--server", empty.URL, "--token", "stub-token", "--tenant", "tenant-a", "bank")
	if exitcode.From(err) != exitcode.Server || out != "" {
		t.Fatalf("vault get with an empty reply: out=%q err=%v, want exit %d and no output", out, err, exitcode.Server)
	}
}
