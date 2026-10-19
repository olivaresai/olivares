// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/cmd/olivares/exitcode"
)

const (
	vaultCLIPassword = "pw-vault-cli-fixture-2387"
	// vaultCLISeed is the RFC 6238 SHA-1 test seed "12345678901234567890" in base32.
	vaultCLISeed = "GEZDGNBVGY3TQOJQGEZDGNBVGY3TQOJQ"
)

// vaultRunner runs `olivares vault <verb> ...` as the first person of a real engine.
func vaultRunner(t *testing.T) (run func(stdin string, verb string, args ...string) (string, string, error), userID string) {
	t.Helper()
	creds, userID, _ := vaultCLISession(t)
	run = func(stdin string, verb string, args ...string) (string, string, error) {
		t.Helper()
		all := append(append([]string{"vault", verb}, creds...), args...)
		return execSessionCLI(t, strings.NewReader(stdin), all...)
	}
	return run, userID
}

func writeVaultFile(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "value")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func decodeObject(t *testing.T, raw string) map[string]any {
	t.Helper()
	var got map[string]any
	if err := json.Unmarshal([]byte(raw), &got); err != nil {
		t.Fatalf("not a JSON object: %v\n%s", err, raw)
	}
	return got
}

// A person keeps a login in four commands and gets every part of it back: put reads
// the secrets from files or stdin (never argv), ls shows no value, get prints the
// part asked for, totp prints the current code and rm needs an explicit yes.
func TestVaultCLIKeepsALoginEndToEnd(t *testing.T) {
	run, userID := vaultRunner(t)
	full := "vault/" + userID + "/bank"
	seedFile := writeVaultFile(t, vaultCLISeed+"\n")

	out, errb, err := run(vaultCLIPassword+"\n", "put", "bank", "--username", "ann", "--url", "https://bank.example",
		"--password-file", "-", "--totp-file", seedFile, "--description", "Main account", "-o", "json")
	stored := decodeObject(t, out)
	if err != nil || stored["name"] != full || stored["type"] != "login" || stored["totp"] != true ||
		strings.Contains(out+errb, vaultCLIPassword) || strings.Contains(out+errb, vaultCLISeed) {
		t.Fatalf("put: err=%v out=%q stderr=%q", err, out, errb)
	}

	out, errb, err = run("", "ls")
	if err != nil || !strings.Contains(out, "bank") || !strings.Contains(out, "login") || !strings.Contains(out, "Main account") ||
		strings.Contains(out+errb, vaultCLIPassword) || strings.Contains(out+errb, vaultCLISeed) {
		t.Fatalf("ls: err=%v out=%q", err, out)
	}
	out, _, err = run("", "ls", "-o", "json")
	var rows []map[string]any
	if err != nil || json.Unmarshal([]byte(out), &rows) != nil || len(rows) != 1 ||
		rows[0]["name"] != full || rows[0]["label"] != "bank" || rows[0]["type"] != "login" || rows[0]["totp"] != true ||
		rows[0]["description"] != "Main account" || strings.Contains(out, vaultCLIPassword) {
		t.Fatalf("ls -o json: err=%v out=%q", err, out)
	}

	for field, want := range map[string]string{"password": vaultCLIPassword, "username": "ann", "url": "https://bank.example", "totp": vaultCLISeed} {
		out, _, err = run("", "get", "bank", "--field", field)
		if err != nil || out != want+"\n" {
			t.Fatalf("get --field %s: err=%v out=%q, want %q", field, err, out, want)
		}
	}
	out, _, err = run("", "get", "bank")
	if err != nil || out != vaultCLIPassword+"\n" {
		t.Fatalf("get without a field: err=%v out=%q, want the password only", err, out)
	}
	out, _, err = run("", "get", "bank", "-o", "json")
	got := decodeObject(t, out)
	if err != nil || got["name"] != full || got["type"] != "login" || got["value"] != vaultCLIPassword ||
		got["username"] != "ann" || got["password"] != vaultCLIPassword || got["url"] != "https://bank.example" || got["totp"] != vaultCLISeed {
		t.Fatalf("get -o json: err=%v out=%q", err, out)
	}
	if _, _, err = run("", "get", "bank", "--field", "cvv"); exitcode.From(err) != exitcode.Usage {
		t.Fatalf("get --field cvv: %v, want exit %d", err, exitcode.Usage)
	}

	out, errb, err = run("", "totp", "bank")
	if err != nil || !regexp.MustCompile(`^\d{6}\n$`).MatchString(out) || strings.Contains(out+errb, vaultCLISeed) {
		t.Fatalf("totp: err=%v out=%q stderr=%q, want the six-digit code alone on stdout", err, out, errb)
	}
	out, _, err = run("", "totp", "bank", "-o", "json")
	code := decodeObject(t, out)
	if left, _ := code["seconds_left"].(float64); err != nil || !regexp.MustCompile(`^\d{6}$`).MatchString(code["code"].(string)) ||
		left < 1 || left > 30 || code["period"] != float64(30) || code["digits"] != float64(6) || strings.Contains(out, vaultCLISeed) {
		t.Fatalf("totp -o json: err=%v out=%q", err, out)
	}

	// Removing an item needs the explicit yes when nobody can be asked.
	if _, _, err = run("", "rm", "bank"); exitcode.From(err) != exitcode.Usage {
		t.Fatalf("rm without --yes: %v, want exit %d", err, exitcode.Usage)
	}
	if _, _, err = run("", "get", "bank"); err != nil {
		t.Fatalf("the refused rm removed the item: %v", err)
	}
	out, _, err = run("", "rm", "bank", "--yes", "-o", "json")
	if removed := decodeObject(t, out); err != nil || removed["name"] != full || removed["deleted"] != true {
		t.Fatalf("rm --yes: err=%v out=%q", err, out)
	}
	if _, _, err = run("", "get", "bank"); exitcode.From(err) != exitcode.NotFound {
		t.Fatalf("get after rm: %v, want exit %d", err, exitcode.NotFound)
	}
	if _, _, err = run("", "totp", "bank"); exitcode.From(err) != exitcode.NotFound {
		t.Fatalf("totp after rm: %v, want exit %d", err, exitcode.NotFound)
	}
	if out, _, _ = run("", "ls"); !strings.Contains(out, "olivares vault put") {
		t.Fatalf("an empty vault says how to add an item, got %q", out)
	}
}

// A note and a standalone authenticator are items too, each read back as itself.
func TestVaultCLIKeepsNotesAndAuthenticators(t *testing.T) {
	run, _ := vaultRunner(t)
	if _, _, err := run("the safe code is 4411\n", "put", "safe", "--note-file", "-"); err != nil {
		t.Fatalf("put note: %v", err)
	}
	if out, _, err := run("", "get", "safe"); err != nil || out != "the safe code is 4411\n" {
		t.Fatalf("get note: err=%v out=%q", err, out)
	}
	if _, _, err := run(vaultCLISeed+"\n", "put", "github-2fa", "--totp-file", "-"); err != nil {
		t.Fatalf("put totp: %v", err)
	}
	if out, _, err := run("", "totp", "github-2fa"); err != nil || !regexp.MustCompile(`^\d{6}\n$`).MatchString(out) {
		t.Fatalf("totp of an authenticator item: err=%v out=%q", err, out)
	}
	if out, _, err := run("", "ls"); err != nil || !strings.Contains(out, "note") || !strings.Contains(out, "totp") {
		t.Fatalf("ls shows the kinds: err=%v out=%q", err, out)
	}
	// A note has no authenticator: that is a conflict with the item, not a missing item.
	if _, _, err := run("", "totp", "safe"); exitcode.From(err) != exitcode.Conflict {
		t.Fatalf("totp of a note: %v, want exit %d", err, exitcode.Conflict)
	}
}

// The generator runs in the CLI itself (no server needed) and put --generate stores
// a generated password without printing it.
func TestVaultCLIGeneratesPasswords(t *testing.T) {
	out, _, err := execSessionCLI(t, nil, "vault", "generate")
	if err != nil || len(strings.TrimSpace(out)) != 20 || !strings.HasSuffix(out, "\n") {
		t.Fatalf("generate: err=%v out=%q, want 20 characters and a newline", err, out)
	}
	out, _, err = execSessionCLI(t, nil, "vault", "generate", "--length", "12", "--no-symbols", "--no-upper", "-o", "json")
	pw, _ := decodeObject(t, out)["password"].(string)
	if err != nil || len(pw) != 12 || strings.Trim(pw, "abcdefghijklmnopqrstuvwxyz0123456789") != "" {
		t.Fatalf("generate --length 12 --no-symbols --no-upper -o json: err=%v out=%q", err, out)
	}
	for _, args := range [][]string{
		{"--length", "3"},
		{"--length", "500"},
		{"--no-lower", "--no-upper", "--no-digits", "--no-symbols"},
	} {
		if _, _, err := execSessionCLI(t, nil, append([]string{"vault", "generate"}, args...)...); exitcode.From(err) != exitcode.Usage {
			t.Fatalf("generate %v: %v, want exit %d", args, err, exitcode.Usage)
		}
	}

	run, _ := vaultRunner(t)
	out, errb, err := run("", "put", "forum", "--username", "ann", "--generate", "--length", "24")
	if err != nil {
		t.Fatalf("put --generate: %v", err)
	}
	got, _, err := run("", "get", "forum")
	kept := strings.TrimSpace(got)
	if err != nil || len(kept) != 24 || strings.Contains(out+errb, kept) {
		t.Fatalf("put --generate stored %q (err %v) and printed %q / %q", kept, err, out, errb)
	}
}

// What put refuses, it refuses before anything is stored or printed.
func TestVaultCLIPutRefusesBadInvocations(t *testing.T) {
	run, _ := vaultRunner(t)
	for what, c := range map[string]struct {
		stdin string
		args  []string
	}{
		"nothing to store":           {"", []string{"bank"}},
		"two values on stdin":        {"x\n", []string{"bank", "--password-file", "-", "--totp-file", "-"}},
		"generate and a file":        {"x\n", []string{"bank", "--generate", "--password-file", "-"}},
		"a password for a note":      {"x\n", []string{"bank", "--type", "note", "--password-file", "-"}},
		"an unknown type":            {"x\n", []string{"bank", "--type", "card", "--password-file", "-"}},
		"an empty password file":     {"", []string{"bank", "--password-file", "-"}},
		"a name that is not a label": {"x\n", []string{"bank account", "--password-file", "-"}},
	} {
		out, errb, err := run(c.stdin, "put", c.args...)
		if exitcode.From(err) != exitcode.Usage || out != "" {
			t.Fatalf("%s: err=%v (exit %d) out=%q stderr=%q, want exit %d and no output", what, err, exitcode.From(err), out, errb, exitcode.Usage)
		}
	}
	if out, _, err := run("", "ls"); err != nil || strings.Contains(out, "bank") {
		t.Fatalf("a refused put stored something: err=%v out=%q", err, out)
	}
	// A seed the engine refuses is refused without being echoed.
	_, errb, err := run("not-a-seed-"+vaultCLIPassword+"\n", "put", "bank", "--totp-file", "-")
	if err == nil || strings.Contains(err.Error()+errb, vaultCLIPassword) {
		t.Fatalf("put with a bad seed: err=%v stderr=%q, want a refusal that repeats nothing", err, errb)
	}
}

// Every vault command is scriptable and documents how it exits.
func TestVaultCommandsDocumentJSONAndExitCodes(t *testing.T) {
	root := newRootCmd()
	for _, verb := range []string{"ls", "put", "get", "rm", "totp", "generate"} {
		cmd, _, err := root.Find([]string{"vault", verb})
		if err != nil || cmd.Name() != verb {
			t.Fatalf("vault %s is not a command: %v", verb, err)
		}
		if cmd.Flags().Lookup("output") == nil && cmd.InheritedFlags().Lookup("output") == nil {
			t.Errorf("vault %s has no -o json", verb)
		}
		if !strings.Contains(cmd.Long, "Exit codes") {
			t.Errorf("vault %s does not document its exit codes:\n%s", verb, cmd.Long)
		}
		// Every code a command that asks the engine can exit with is listed: an engine that is
		// down or answers badly is exit 6, and a script branches on it. generate runs locally.
		for _, code := range []string{"0", "1", "2", "3", "4", "5", "6"} {
			if verb == "generate" {
				break
			}
			if !regexp.MustCompile(`Exit codes:[^\n]*\b` + code + ` `).MatchString(cmd.Long) {
				t.Errorf("vault %s does not document exit %s:\n%s", verb, code, cmd.Long)
			}
		}
		if cmd.Example == "" {
			t.Errorf("vault %s has no example", verb)
		}
	}
}

// A login with only a username has no password to print: text mode says so (and never prints
// the username as if it were one), while -o json prints the item as it is. A missing
// authenticator key is exit 5 on every command that asks for it.
func TestVaultCLIGetOfALoginWithoutAPassword(t *testing.T) {
	run, _ := vaultRunner(t)
	if _, _, err := run("", "put", "who", "--username", "ann"); err != nil {
		t.Fatalf("put: %v", err)
	}
	if out, _, err := run("", "get", "who"); exitcode.From(err) != exitcode.NotFound || strings.Contains(out, "ann") {
		t.Fatalf("get: out=%q err=%v, want exit %d and no username", out, err, exitcode.NotFound)
	}
	out, _, err := run("", "get", "who", "-o", "json")
	got := decodeObject(t, out)
	if err != nil || got["username"] != "ann" || got["value"] != "" {
		t.Fatalf("get -o json: err=%v out=%q, want the item with an empty value", err, out)
	}
	if _, _, err = run("", "get", "who", "--field", "url"); exitcode.From(err) != exitcode.NotFound {
		t.Fatalf("get --field url: %v, want exit %d", err, exitcode.NotFound)
	}
	for _, args := range [][]string{{"get", "who", "--field", "totp"}, {"totp", "who"}} {
		if _, _, err = run("", args[0], args[1:]...); exitcode.From(err) != exitcode.Conflict {
			t.Fatalf("%v: %v, want exit %d", args, err, exitcode.Conflict)
		}
	}
	if out, _, err = run("", "get", "who", "--field", "totp", "-o", "json"); err != nil || decodeObject(t, out)["value"] != "" {
		t.Fatalf("get --field totp -o json: err=%v out=%q", err, out)
	}
}
