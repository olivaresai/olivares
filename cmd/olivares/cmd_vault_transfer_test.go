// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/olivaresai/olivares/cmd/olivares/exitcode"
	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// vaultFixtureValues are the secrets of every import fixture: none may reach a terminal.
var vaultFixtureValues = regexp.MustCompile(`pw-[a-z]{2}-[a-z0-9]+-2388|4411-bw-2388|A1B2-C3D4-op-2388|pp-2388|GEZDGNBVGY3TQOJQGEZDGNBVGY3TQOJQ|not-base32`)

func vaultFixturePath(source string) string {
	return filepath.Join("testdata", "vault-import", source+".csv")
}

func decodeVaultImportResult(t *testing.T, raw string) vaultImportResult {
	t.Helper()
	var got vaultImportResult
	if err := json.Unmarshal([]byte(raw), &got); err != nil {
		t.Fatalf("not an import result: %v\n%s", err, raw)
	}
	return got
}

// Every layout imports through the same Put the console and `vault put` use, with the item
// count its rows mean, and no secret of the file reaches stdout or stderr.
func TestVaultImportEachSourceOnARealEngine(t *testing.T) {
	for _, c := range []struct {
		source, file string
		items, bad   int
		types        map[string]int
	}{
		{"bitwarden", "bitwarden", 4, 1, map[string]int{"login": 3, "note": 1}},
		{"keepass", "keepass", 3, 1, map[string]int{"login": 2, "totp": 1}},
		{"1password", "1password", 3, 0, map[string]int{"login": 2, "note": 1}},
		{"chrome", "chrome", 2, 1, map[string]int{"login": 2}},
		{"proton", "proton", 3, 0, map[string]int{"login": 2, "note": 1}},
	} {
		t.Run(c.source, func(t *testing.T) {
			run, _ := vaultRunner(t)
			out, errb, err := run("", "import", vaultFixturePath(c.file), "--from", c.source, "-o", "json")
			if c.bad == 0 && err != nil {
				t.Fatalf("import: %v\n%s", err, errb)
			}
			// A row the vault cannot hold is exit 1, after every valid row is stored.
			if c.bad > 0 && exitcode.From(err) != exitcode.Err {
				t.Fatalf("import with %d bad rows: err=%v (exit %d), want exit %d", c.bad, err, exitcode.From(err), exitcode.Err)
			}
			if vaultFixtureValues.MatchString(out+errb) || (err != nil && vaultFixtureValues.MatchString(err.Error())) {
				t.Fatalf("a value reached the output:\nstdout %q\nstderr %q\nerr %v", out, errb, err)
			}
			got := decodeVaultImportResult(t, out)
			if got.Items != c.items || got.Written != c.items || len(got.Failed) != c.bad || got.DryRun {
				t.Fatalf("result = %+v, want %d items written and %d failed", got, c.items, c.bad)
			}
			for kind, n := range c.types {
				if got.Types[kind] != n {
					t.Errorf("types = %v, want %v", got.Types, c.types)
				}
			}
			out, _, err = run("", "ls", "-o", "json")
			var rows []map[string]any
			if err != nil || json.Unmarshal([]byte(out), &rows) != nil || len(rows) != c.items {
				t.Fatalf("ls after import: err=%v out=%q, want %d items", err, out, c.items)
			}
		})
	}
}

// What an import stores is what the file said, read back by the owner.
func TestVaultImportStoresTheFileContent(t *testing.T) {
	run, _ := vaultRunner(t)
	if _, _, err := run("", "import", vaultFixturePath("bitwarden"), "--from", "bitwarden"); exitcode.From(err) != exitcode.Err {
		t.Fatalf("import: %v", err)
	}
	out, _, err := run("", "get", "Bank", "-o", "json")
	got := decodeObject(t, out)
	if err != nil || got["username"] != "ann" || got["password"] != "pw-bw-bank-2388" || got["url"] != "https://bank.example" ||
		got["note"] != "Main account\nsecond line" || !strings.HasPrefix(got["totp"].(string), "otpauth://totp/Bank:ann?secret=GEZD") {
		t.Fatalf("get Bank: err=%v out=%q", err, out)
	}
	if out, _, err = run("", "totp", "Bank"); err != nil || !regexp.MustCompile(`^\d{6}\n$`).MatchString(out) {
		t.Fatalf("an imported authenticator makes codes: err=%v out=%q", err, out)
	}
	// A title that is not a valid name is kept as the description of its label.
	out, _, err = run("", "ls", "-o", "json")
	var rows []map[string]any
	if err != nil || json.Unmarshal([]byte(out), &rows) != nil {
		t.Fatalf("ls: %v %q", err, out)
	}
	desc := map[string]any{}
	for _, r := range rows {
		desc[r["label"].(string)] = r["description"]
	}
	if desc["Forum-old"] != "Forum (old)" || desc["Forum-old-2"] != "Forum (old)" || desc["Bank"] != "" {
		t.Fatalf("descriptions = %v", desc)
	}
}

// A dry run reads and checks the file, prints counts only, and writes nothing.
func TestVaultImportDryRunWritesNothingAndNamesNothing(t *testing.T) {
	run, _ := vaultRunner(t)
	out, errb, err := run("", "import", vaultFixturePath("bitwarden"), "--from", "bitwarden", "--dry-run", "-o", "json")
	if exitcode.From(err) != exitcode.Err {
		t.Fatalf("dry run: %v, want exit %d for the row that cannot be stored", err, exitcode.Err)
	}
	got := decodeVaultImportResult(t, out)
	if !got.DryRun || got.Items != 4 || got.Written != 0 || got.Rows != 6 || len(got.Failed) != 1 || got.Unsupported["card"] != 1 {
		t.Fatalf("dry run result = %+v", got)
	}
	// Counts only: no label, no title, no value, in either rendering.
	text, errb2, _ := run("", "import", vaultFixturePath("bitwarden"), "--from", "bitwarden", "--dry-run")
	for _, leak := range []string{"Bank", "Forum", "Safe", "Visa", "ann", "bank.example"} {
		if strings.Contains(out+errb+text+errb2, leak) {
			t.Errorf("a dry run printed %q:\n%s\n%s", leak, out, text)
		}
	}
	if vaultFixtureValues.MatchString(out + errb + text + errb2) {
		t.Fatalf("a value reached the output of a dry run")
	}
	if ls, _, err := run("", "ls", "-o", "json"); err != nil || strings.TrimSpace(ls) != "[]" {
		t.Fatalf("a dry run stored something: err=%v ls=%q", err, ls)
	}
}

// An item already in the vault is never overwritten by accident; --replace says so.
func TestVaultImportKeepsExistingItemsUnlessReplace(t *testing.T) {
	run, _ := vaultRunner(t)
	if _, _, err := run("mine-not-theirs\n", "put", "Bank", "--username", "me", "--password-file", "-"); err != nil {
		t.Fatal(err)
	}
	out, _, _ := run("", "import", vaultFixturePath("bitwarden"), "--from", "bitwarden", "-o", "json")
	got := decodeVaultImportResult(t, out)
	if got.Items != 4 || got.SkippedExisting != 1 || got.Written != 3 {
		t.Fatalf("result = %+v, want 3 written and Bank skipped", got)
	}
	if pw, _, err := run("", "get", "Bank"); err != nil || pw != "mine-not-theirs\n" {
		t.Fatalf("the import overwrote Bank: %q %v", pw, err)
	}
	out, _, _ = run("", "import", vaultFixturePath("bitwarden"), "--from", "bitwarden", "--replace", "-o", "json")
	got = decodeVaultImportResult(t, out)
	if got.SkippedExisting != 0 || got.Written != 4 {
		t.Fatalf("with --replace: %+v, want 4 written", got)
	}
	if pw, _, err := run("", "get", "Bank"); err != nil || pw != "pw-bw-bank-2388\n" {
		t.Fatalf("--replace did not replace Bank: %q %v", pw, err)
	}
}

// A vault holds 500 items; an import that cannot fit is refused whole, before it writes.
func TestVaultImportBeyondTheLimitWritesNothing(t *testing.T) {
	run, _ := vaultRunner(t)
	var csv strings.Builder
	csv.WriteString("name,url,username,password\n")
	for i := 0; i < auth.MaxVaultItemsPerOwner+1; i++ {
		csv.WriteString("site" + strings.Repeat("x", i%7) + string(rune('a'+i%26)) + strings.Repeat("0", i/26) + ",https://x.example,ann,pw-big-2388\n")
	}
	file := writeVaultFile(t, csv.String())
	_, errb, err := run("", "import", file, "--from", "chrome")
	if exitcode.From(err) != exitcode.Err || !strings.Contains(err.Error(), "500") {
		t.Fatalf("import of 501 items: err=%v stderr=%q, want exit %d naming the limit", err, errb, exitcode.Err)
	}
	if ls, _, _ := run("", "ls", "-o", "json"); strings.TrimSpace(ls) != "[]" {
		t.Fatalf("a refused import stored items: %q", ls)
	}
}

// What import refuses as a wrong invocation, it refuses before it stores anything.
func TestVaultImportAndExportRefuseBadInvocations(t *testing.T) {
	run, _ := vaultRunner(t)
	chrome := vaultFixturePath("chrome")
	for what, c := range map[string]struct {
		verb string
		args []string
		code int
	}{
		"import without a source":     {"import", []string{chrome}, exitcode.Usage},
		"import of an unknown source": {"import", []string{chrome, "--from", "lastpass"}, exitcode.Usage},
		"import of the wrong layout":  {"import", []string{vaultFixturePath("keepass"), "--from", "chrome"}, exitcode.Usage},
		"import of a missing file":    {"import", []string{"testdata/vault-import/nope.csv", "--from", "chrome"}, exitcode.Err},
		"import without a file":       {"import", []string{"--from", "chrome"}, exitcode.Usage},
		"export without a file":       {"export", nil, exitcode.Usage},
	} {
		out, errb, err := run("", c.verb, c.args...)
		if exitcode.From(err) != c.code || out != "" {
			t.Errorf("%s: err=%v (exit %d) out=%q stderr=%q, want exit %d and no output", what, err, exitcode.From(err), out, errb, c.code)
		}
	}
	if ls, _, _ := run("", "ls", "-o", "json"); strings.TrimSpace(ls) != "[]" {
		t.Fatalf("a refused import stored items: %q", ls)
	}
}

// Export reads every item back through reveal, so each one is a ledger entry, and the file
// is the owner's alone.
func TestVaultExportRevealsEveryItemAndWritesAPrivateFile(t *testing.T) {
	eng, creds, _, _ := vaultCLIEngine(t)
	run := func(stdin, verb string, args ...string) (string, string, error) {
		t.Helper()
		return execSessionCLI(t, strings.NewReader(stdin), append(append([]string{"vault", verb}, creds...), args...)...)
	}
	if _, _, err := run("", "import", vaultFixturePath("1password"), "--from", "1password"); err != nil {
		t.Fatal(err)
	}
	reveals := func() int { return vaultRevealEntries(t, eng) }
	if n := reveals(); n != 0 {
		t.Fatalf("an import revealed %d items", n)
	}

	file := filepath.Join(t.TempDir(), "vault.json")
	out, errb, err := run("", "export", "--file", file, "-o", "json")
	if err != nil || vaultFixtureValues.MatchString(out+errb) {
		t.Fatalf("export: err=%v out=%q stderr=%q", err, out, errb)
	}
	if got := decodeObject(t, out); got["items"] != float64(3) || got["file"] != file {
		t.Fatalf("export result = %v", got)
	}
	if n := reveals(); n != 3 {
		t.Fatalf("secret.reveal entries = %d, want 3 (one per exported item)", n)
	}
	info, err := os.Stat(file)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("export file: %v mode %v, want 0600", err, info)
	}
	// Exporting again never overwrites a file.
	before, _ := os.ReadFile(file)
	if _, _, err := run("", "export", "--file", file); exitcode.From(err) != exitcode.Usage {
		t.Fatalf("export over an existing file: %v, want exit %d", err, exitcode.Usage)
	}
	if after, _ := os.ReadFile(file); string(after) != string(before) {
		t.Fatal("a refused export changed the file")
	}
	// `--file -` is the explicit way to stdout.
	out, _, err = run("", "export", "--file", "-")
	var doc map[string]any
	if err != nil || json.Unmarshal([]byte(out), &doc) != nil || doc["format"] != "olivares-vault" || len(doc["items"].([]any)) != 3 {
		t.Fatalf("export --file -: err=%v out=%q", err, out)
	}
}

// Export then import returns every login, note and authenticator byte for byte, with its
// description, into a vault that no longer has them.
func TestVaultExportThenImportRoundTrips(t *testing.T) {
	run, _ := vaultRunner(t)
	tricky := "line one\r\nline \"two\", with <b>&amp;</b>\ttab, unicode ünï 映画, and a trailing space "
	put := func(stdin string, args ...string) {
		t.Helper()
		if _, _, err := run(stdin, "put", args...); err != nil {
			t.Fatalf("put %v: %v", args, err)
		}
	}
	put("pw-round-login-2388\n", "bank", "--username", "ann", "--url", "https://bank.example", "--password-file", "-", "--description", "Main account, \"personal\"")
	put(vaultCLISeed+"\n", "bank-2fa", "--totp-file", "-")
	put(tricky+"\n", "safe", "--note-file", "-", "--description", "Where it is")
	put("otpauth://totp/Mail:ann?secret="+vaultCLISeed+"&digits=8&period=60&algorithm=SHA256\n", "mail-2fa", "--totp-file", "-")

	snapshot := func() map[string]string {
		t.Helper()
		out, _, err := run("", "ls", "-o", "json")
		var rows []map[string]any
		if err != nil || json.Unmarshal([]byte(out), &rows) != nil {
			t.Fatalf("ls: %v %q", err, out)
		}
		snap := map[string]string{}
		for _, r := range rows {
			label := r["label"].(string)
			item, _, err := run("", "get", label, "-o", "json")
			if err != nil {
				t.Fatalf("get %s: %v", label, err)
			}
			item = regexp.MustCompile(`"name": "[^"]*",?`).ReplaceAllString(item, "")
			snap[label] = item + "|" + r["type"].(string) + "|" + r["description"].(string)
		}
		return snap
	}
	before := snapshot()
	if len(before) != 4 {
		t.Fatalf("setup: %d items", len(before))
	}

	file := filepath.Join(t.TempDir(), "vault.json")
	if _, _, err := run("", "export", "--file", file); err != nil {
		t.Fatalf("export: %v", err)
	}
	for label := range before {
		if _, _, err := run("", "rm", label, "--yes"); err != nil {
			t.Fatalf("rm %s: %v", label, err)
		}
	}
	if ls, _, _ := run("", "ls", "-o", "json"); strings.TrimSpace(ls) != "[]" {
		t.Fatalf("vault not empty: %s", ls)
	}
	out, _, err := run("", "import", file, "--from", "olivares", "-o", "json")
	if got := decodeVaultImportResult(t, out); err != nil || got.Written != 4 || len(got.Failed) != 0 {
		t.Fatalf("import of the export: err=%v %+v", err, got)
	}
	after := snapshot()
	var labels []string
	for label := range before {
		labels = append(labels, label)
	}
	sort.Strings(labels)
	for _, label := range labels {
		if before[label] != after[label] {
			t.Errorf("%s changed in the round trip:\nbefore %s\nafter  %s", label, before[label], after[label])
		}
	}
	if len(after) != len(before) {
		t.Errorf("round trip has %d items, want %d", len(after), len(before))
	}
}

// ---- an engine that fails the way a real one cannot be made to on demand ------------------

type vaultStubEntry struct{ value, description, hint string }

// vaultStub is the part of an engine a vault CLI verb talks to: the person, the list, the
// put, and reveal. It keeps what it is given, can fail the Nth put, and can rate limit.
type vaultStub struct {
	mu          sync.Mutex
	items       map[string]vaultStubEntry
	puts        int
	failPutAt   int // the Nth put answers 500 and stores nothing
	limitPuts   int // this many puts answer 429 first
	limitReveal int // this many reveals answer 429 first
	failReveal  string
	badPuts     []string // bodies the real engine would have refused as not an item
}

func newVaultStub(t *testing.T) (*vaultStub, []string) {
	t.Helper()
	s := &vaultStub{items: map[string]vaultStubEntry{}}
	srv := httptest.NewServer(http.HandlerFunc(s.serve))
	t.Cleanup(srv.Close)
	return s, []string{"--server", srv.URL, "--token", "stub-token", "--tenant", "tenant-a"}
}

func (s *vaultStub) serve(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var in struct{ Name, Value, Description string }
	_ = json.NewDecoder(r.Body).Decode(&in)
	switch {
	case r.URL.Path == "/v1/auth/whoami":
		_, _ = w.Write([]byte(`{"kind":"user","user_id":"u-1"}`))
	case r.URL.Path == "/v1/console/secrets" && r.Method == http.MethodGet:
		var rows []map[string]string
		for name, e := range s.items {
			rows = append(rows, map[string]string{"name": name, "hint": e.hint, "description": e.description, "updated_at": "2026-10-18T00:00:00Z"})
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"secrets": rows})
	case r.URL.Path == "/v1/console/secrets" && r.Method == http.MethodPut:
		if s.limitPuts > 0 {
			s.limitPuts--
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = w.Write([]byte(`{"error":{"code":"rate_limited","message":"rate limited"}}`))
			return
		}
		s.puts++
		if s.puts == s.failPutAt {
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte(`{"error":{"code":"internal","message":"store unavailable ` + in.Value + `"}}`))
			return
		}
		item, err := auth.ParseVaultItem(in.Value)
		if err != nil {
			s.badPuts = append(s.badPuts, in.Name)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		s.items[in.Name] = vaultStubEntry{value: in.Value, description: in.Description, hint: item.Hint()}
		_, _ = w.Write([]byte(`{"name":"` + in.Name + `"}`))
	case r.URL.Path == "/v1/console/secrets/reveal" && r.Method == http.MethodPost:
		if s.limitReveal > 0 {
			s.limitReveal--
			w.Header().Set("Retry-After", "1")
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = w.Write([]byte(`{"error":{"code":"rate_limited","message":"rate limited"}}`))
			return
		}
		if in.Name == s.failReveal {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		e, ok := s.items[in.Name]
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]string{"name": in.Name, "value": e.value})
	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

func (s *vaultStub) names() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	var names []string
	for n := range s.items {
		names = append(names, strings.TrimPrefix(n, "vault/u-1/"))
	}
	sort.Strings(names)
	return names
}

// An engine that fails in the middle of an import leaves whole items and nothing else: each
// stored item is a valid, complete document, the failure is reported with the count, no value
// is echoed from the engine's answer, and running the same import again finishes the rest.
func TestVaultImportPartialFailureLeavesNoHalfWrittenItem(t *testing.T) {
	stub, creds := newVaultStub(t)
	run := func(args ...string) (string, string, error) {
		t.Helper()
		return execSessionCLI(t, nil, append(append([]string{"vault", "import"}, creds...), args...)...)
	}
	stub.failPutAt = 3
	out, errb, err := run(vaultFixturePath("bitwarden"), "--from", "bitwarden", "-o", "json")
	if exitcode.From(err) != exitcode.Server {
		t.Fatalf("import with a failing engine: err=%v (exit %d), want exit %d", err, exitcode.From(err), exitcode.Server)
	}
	if vaultFixtureValues.MatchString(out+errb) || vaultFixtureValues.MatchString(err.Error()) {
		t.Fatalf("a value reached the output:\n%s\n%s\n%v", out, errb, err)
	}
	if got := decodeVaultImportResult(t, out); got.Items != 4 || got.Written != 2 {
		t.Fatalf("result = %+v, want 4 items of which 2 written", got)
	}
	if names := stub.names(); strings.Join(names, ",") != "Bank,Forum-old" {
		t.Fatalf("stored = %v, want the two items before the failure and nothing of the third", names)
	}
	if len(stub.badPuts) != 0 {
		t.Fatalf("the CLI sent %v, which the engine would refuse as not an item", stub.badPuts)
	}
	for name, e := range stub.items {
		if _, err := auth.ParseVaultItem(e.value); err != nil {
			t.Errorf("%s is not a complete item: %v", name, err)
		}
	}
	// Whole is not enough: the stored document is the one the file's row meant, field for field.
	bank, _ := json.Marshal(auth.VaultItem{Type: "login", Username: "ann", Password: "pw-bw-bank-2388", URL: "https://bank.example",
		Note: "Main account\nsecond line", TOTP: "otpauth://totp/Bank:ann?secret=GEZDGNBVGY3TQOJQGEZDGNBVGY3TQOJQ&issuer=Bank"})
	if stub.items["vault/u-1/Bank"].value != string(bank) {
		t.Error("Bank was stored as something other than the row of the file")
	}
	if !strings.Contains(err.Error(), "2 of 4") || !strings.Contains(err.Error(), "run the same command again") {
		t.Errorf("the stop does not say how far it got and how to finish: %v", err)
	}

	// Running it again stores the rest, skips what is there, and writes no item twice.
	stub.failPutAt = 0
	putsBefore := stub.puts
	out, _, err = run(vaultFixturePath("bitwarden"), "--from", "bitwarden", "-o", "json")
	got := decodeVaultImportResult(t, out)
	if exitcode.From(err) != exitcode.Err || got.SkippedExisting != 2 || got.Written != 2 || stub.puts-putsBefore != 2 {
		t.Fatalf("second run: err=%v %+v puts=%d, want 2 skipped, 2 written", err, got, stub.puts-putsBefore)
	}
	if names := stub.names(); strings.Join(names, ",") != "Bank,Forum-old,Forum-old-2,Safe-code" {
		t.Fatalf("after the second run: %v", names)
	}
}

// A rate-limited engine makes a bulk verb wait and carry on, not fail half way.
func TestVaultBulkVerbsWaitOutTheRateLimit(t *testing.T) {
	old := vaultRateWait
	vaultRateWait = time.Millisecond
	t.Cleanup(func() { vaultRateWait = old })

	stub, creds := newVaultStub(t)
	stub.limitPuts = 2
	run := func(verb string, args ...string) (string, string, error) {
		t.Helper()
		return execSessionCLI(t, nil, append(append([]string{"vault", verb}, creds...), args...)...)
	}
	if _, errb, err := run("import", vaultFixturePath("1password"), "--from", "1password"); err != nil {
		t.Fatalf("import against a limited engine: %v\n%s", err, errb)
	}
	if strings.Join(stub.names(), ",") != "Recovery-codes,Router,Shop" {
		t.Fatalf("stored %v after the limit cleared", stub.names())
	}
	stub.limitReveal = 3
	file := filepath.Join(t.TempDir(), "out.json")
	out, errb, err := run("export", "--file", file, "-o", "json")
	if err != nil || decodeObject(t, out)["items"] != float64(3) {
		t.Fatalf("export against a limited engine: err=%v out=%q", err, out)
	}
	if !strings.Contains(errb, "rate limit") || vaultFixtureValues.MatchString(errb) {
		t.Fatalf("the wait is announced on stderr without a value: %q", errb)
	}

	// A limit that never clears is an error, not a hang.
	stub.limitReveal = 1 << 20
	if _, _, err := run("export", "--file", filepath.Join(t.TempDir(), "never.json")); err == nil {
		t.Fatal("an engine that never stops limiting must fail the export")
	}
}

// An export that fails writes no file: the secrets of a half export are not left on disk.
func TestVaultExportFailureLeavesNoFile(t *testing.T) {
	stub, creds := newVaultStub(t)
	run := func(verb string, args ...string) (string, string, error) {
		t.Helper()
		return execSessionCLI(t, nil, append(append([]string{"vault", verb}, creds...), args...)...)
	}
	if _, _, err := run("import", vaultFixturePath("1password"), "--from", "1password"); err != nil {
		t.Fatal(err)
	}
	stub.failReveal = "vault/u-1/Router"
	file := filepath.Join(t.TempDir(), "partial.json")
	out, errb, err := run("export", "--file", file)
	if exitcode.From(err) != exitcode.Server {
		t.Fatalf("export with a failing reveal: err=%v (exit %d), want exit %d", err, exitcode.From(err), exitcode.Server)
	}
	if _, statErr := os.Stat(file); !os.IsNotExist(statErr) {
		t.Fatalf("a failed export left %s behind (%v)", file, statErr)
	}
	if out != "" || vaultFixtureValues.MatchString(out+errb) {
		t.Fatalf("a failed export printed: %q %q", out, errb)
	}
}

// An entry stored before items had a shape cannot be exported as an item: the export says
// which ones it left out, by name, and exports the rest.
func TestVaultExportReportsEntriesThatAreNotItems(t *testing.T) {
	stub, creds := newVaultStub(t)
	stub.items["vault/u-1/old-key"] = vaultStubEntry{value: "a-raw-value-2388", hint: "sha256:ab12"}
	stub.items["vault/u-1/bank"] = vaultStubEntry{value: `{"type":"login","username":"ann","password":"pw-old-2388"}`, hint: "login"}
	file := filepath.Join(t.TempDir(), "out.json")
	out, errb, err := execSessionCLI(t, nil, append([]string{"vault", "export", "--file", file, "-o", "json"}, creds...)...)
	if err != nil {
		t.Fatalf("export: %v\n%s", err, errb)
	}
	got := decodeObject(t, out)
	skipped, _ := got["skipped"].([]any)
	if got["items"] != float64(1) || len(skipped) != 1 || skipped[0] != "old-key" || strings.Contains(out+errb, "a-raw-value-2388") {
		t.Fatalf("export result = %v (stderr %q)", got, errb)
	}
}

// vaultRevealEntries counts the secret.reveal entries of the engine's audit ledger, failing
// the test if any entry of it holds a fixture value.
func vaultRevealEntries(t *testing.T, eng *engine) int {
	t.Helper()
	n := 0
	err := eng.store.AuthView(context.Background(), func(as store.AuthScope) error {
		walker, ok := as.Audit().(store.CanonicalWalker)
		if !ok {
			t.Fatal("the ledger has no canonical reader")
		}
		return walker.WalkCanonical(context.Background(), 1, func(ev model.AuditEvent, canonical string, _ []byte) error {
			if vaultFixtureValues.MatchString(canonical) {
				t.Fatalf("the ledger holds a value: %s", canonical)
			}
			if ev.Action == "secret.reveal" {
				n++
			}
			return nil
		})
	})
	if err != nil {
		t.Fatal(err)
	}
	return n
}

// An export that cannot be written fails before it reads anything: every item it would have
// opened is an audit entry and a minute of the engine's read budget.
func TestVaultExportClaimsItsFileBeforeTheFirstReveal(t *testing.T) {
	eng, creds, _, _ := vaultCLIEngine(t)
	run := func(verb string, args ...string) (string, string, error) {
		t.Helper()
		return execSessionCLI(t, nil, append(append([]string{"vault", verb}, creds...), args...)...)
	}
	if _, _, err := run("import", vaultFixturePath("1password"), "--from", "1password"); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	for what, target := range map[string]string{"a missing directory": filepath.Join(dir, "missing", "v.json"), "a directory": dir} {
		if _, _, err := run("export", "--file", target); err == nil {
			t.Errorf("export to %s succeeded", what)
		}
	}
	if n := vaultRevealEntries(t, eng); n != 0 {
		t.Fatalf("refused exports revealed %d items, want 0", n)
	}
	existing := writeVaultFile(t, "mine")
	if _, _, err := run("export", "--file", existing); exitcode.From(err) != exitcode.Usage {
		t.Fatalf("export over a file: %v, want exit %d", err, exitcode.Usage)
	}
	if n := vaultRevealEntries(t, eng); n != 0 {
		t.Fatalf("an export over a file revealed %d items, want 0", n)
	}
	if data, _ := os.ReadFile(existing); string(data) != "mine" {
		t.Fatalf("a refused export changed the file: %q", data)
	}
}

// Every name `vault put` accepts survives export then import, and an import of the same file
// into the vault that still holds them stores nothing twice.
func TestVaultExportThenImportKeepsEveryLabel(t *testing.T) {
	run, _ := vaultRunner(t)
	labels := []string{"work/bank", ".env", "_priv", "a-b", "env", strings.Repeat("l", 200)}
	for _, label := range labels {
		if _, _, err := run("pw-"+label[:1]+"-lbl-2388\n", "put", label, "--password-file", "-", "--description", "for "+label[:1]); err != nil {
			t.Fatalf("put %q: %v", label, err)
		}
	}
	names := func() string {
		out, _, err := run("", "ls", "-o", "json")
		var rows []map[string]any
		if err != nil || json.Unmarshal([]byte(out), &rows) != nil {
			t.Fatalf("ls: %v %q", err, out)
		}
		var got []string
		for _, r := range rows {
			got = append(got, r["label"].(string)+"|"+r["description"].(string))
		}
		sort.Strings(got)
		return strings.Join(got, "\n")
	}
	before := names()
	file := filepath.Join(t.TempDir(), "v.json")
	if _, _, err := run("", "export", "--file", file); err != nil {
		t.Fatal(err)
	}
	out, _, err := run("", "import", file, "--from", "olivares", "-o", "json")
	if got := decodeVaultImportResult(t, out); err != nil || got.SkippedExisting != len(labels) || got.Written != 0 {
		t.Fatalf("import into the vault that holds them: err=%v %+v, want all skipped", err, got)
	}
	for _, label := range labels {
		if _, _, err := run("", "rm", label, "--yes"); err != nil {
			t.Fatal(err)
		}
	}
	out, _, err = run("", "import", file, "--from", "olivares", "-o", "json")
	if got := decodeVaultImportResult(t, out); err != nil || got.Written != len(labels) || len(got.Failed) != 0 {
		t.Fatalf("import into an empty vault: err=%v %+v", err, got)
	}
	if after := names(); after != before {
		t.Fatalf("labels and descriptions changed:\nbefore %s\nafter  %s", before, after)
	}
}

// A replacement keeps the line the list shows unless the file gives one, as `put` does.
func TestVaultImportReplaceKeepsTheDescription(t *testing.T) {
	run, _ := vaultRunner(t)
	if _, _, err := run("old-pw\n", "put", "Bank", "--username", "me", "--password-file", "-", "--description", "joint account"); err != nil {
		t.Fatal(err)
	}
	file := writeVaultFile(t, "name,url,username,password\nBank,https://bank.example,ann,pw-new-bank-2388\n")
	out, _, err := run("", "import", file, "--from", "chrome", "--replace", "-o", "json")
	if got := decodeVaultImportResult(t, out); err != nil || got.Written != 1 {
		t.Fatalf("replace: err=%v %+v", err, got)
	}
	if pw, _, err := run("", "get", "Bank"); err != nil || pw != "pw-new-bank-2388\n" {
		t.Fatalf("Bank = %q %v, want the new password", pw, err)
	}
	out, _, _ = run("", "ls", "-o", "json")
	var rows []map[string]any
	if json.Unmarshal([]byte(out), &rows) != nil || len(rows) != 1 || rows[0]["description"] != "joint account" {
		t.Fatalf("ls after replace = %s, want the description kept", out)
	}
}

// A file comes in on stdin and the document goes out on stdout, so a vault moves through a pipe
// without a file on disk.
func TestVaultImportAndExportThroughPipes(t *testing.T) {
	run, _ := vaultRunner(t)
	csv := "name,url,username,password\nsite,https://s.example,ann,pw-pipe-site-2388\nother,https://o.example,bob,pw-pipe-other-2388\n"
	out, _, err := run(csv, "import", "-", "--from", "chrome", "-o", "json")
	if got := decodeVaultImportResult(t, out); err != nil || got.Written != 2 {
		t.Fatalf("import from stdin: err=%v %+v", err, got)
	}
	doc, errb, err := run("", "export", "--file", "-")
	if err != nil || !strings.HasPrefix(doc, "{") || !strings.Contains(errb, "Exported 2 items to stdout") || strings.Contains(errb, "pw-pipe") {
		t.Fatalf("export to stdout: err=%v stderr=%q", err, errb)
	}
	for _, label := range []string{"site", "other"} {
		if _, _, err := run("", "rm", label, "--yes"); err != nil {
			t.Fatal(err)
		}
	}
	out, _, err = run(doc, "import", "-", "--from", "olivares", "-o", "json")
	if got := decodeVaultImportResult(t, out); err != nil || got.Written != 2 {
		t.Fatalf("import of the piped export: err=%v %+v", err, got)
	}
	if pw, _, err := run("", "get", "other"); err != nil || pw != "pw-pipe-other-2388\n" {
		t.Fatalf("other = %q %v", pw, err)
	}
	if out, _, err := run("", "import", "-", "--from", "chrome"); exitcode.From(err) != exitcode.Usage || out != "" {
		t.Fatalf("an empty stdin: err=%v out=%q, want exit %d", err, out, exitcode.Usage)
	}
}

func vaultSiteRows(from, to int) string {
	var csv strings.Builder
	csv.WriteString("name,url,username,password\n")
	for i := from; i < to; i++ {
		fmt.Fprintf(&csv, "site%d,https://x.example,ann,pw-cap-2388\n", i)
	}
	return csv.String()
}

// The limit counts what an import adds, not what it names: re-importing a file the vault already
// holds is fine, a vault filled to exactly 500 is fine, and one more is refused before a write.
func TestVaultImportCapCountsOnlyNewItems(t *testing.T) {
	stub, creds := newVaultStub(t)
	for i := 0; i < 300; i++ {
		stub.items[fmt.Sprintf("vault/u-1/site%d", i)] = vaultStubEntry{value: `{"type":"login","username":"ann","password":"pw-cap-2388"}`, hint: "login"}
	}
	run := func(csv string) (string, error) {
		t.Helper()
		out, _, err := execSessionCLI(t, nil, append([]string{"vault", "import", writeVaultFile(t, csv), "--from", "chrome", "-o", "json"}, creds...)...)
		return out, err
	}
	out, err := run(vaultSiteRows(0, 500))
	if got := decodeVaultImportResult(t, out); err != nil || got.SkippedExisting != 300 || got.Written != 200 || stub.puts != 200 {
		t.Fatalf("300 held, 500 named: err=%v %+v puts=%d, want 300 skipped and 200 written", err, got, stub.puts)
	}
	if _, err := run(vaultSiteRows(500, 501)); exitcode.From(err) != exitcode.Err || !strings.Contains(err.Error(), "500") || stub.puts != 200 {
		t.Fatalf("501st item: err=%v puts=%d, want a refusal before any write", err, stub.puts)
	}
}

// Only the caller's own vault is read or written: another person's entries and the
// organization's other secrets are neither opened by an export nor counted by an import.
func TestVaultBulkVerbsLeaveOtherEntriesAlone(t *testing.T) {
	stub, creds := newVaultStub(t)
	login := `{"type":"login","username":"ann","password":"pw-own-2388"}`
	stub.items["vault/u-1/mine"] = vaultStubEntry{value: login, hint: "login"}
	stub.items["vault/u-2/other"] = vaultStubEntry{value: login, hint: "login"}
	stub.items["mcp/key"] = vaultStubEntry{value: "a-key", hint: "sha256:ab12"}
	stub.failReveal = "vault/u-2/other" // an attempt to open it is a failed export
	// An administrator's list also holds the organization's own secrets; none counts against a
	// person's 500 items.
	for i := 0; i < auth.MaxVaultItemsPerOwner; i++ {
		stub.items[fmt.Sprintf("mcp/key%d", i)] = vaultStubEntry{value: "a-key", hint: "sha256:ab12"}
	}
	file := filepath.Join(t.TempDir(), "v.json")
	out, errb, err := execSessionCLI(t, nil, append([]string{"vault", "export", "--file", file, "-o", "json"}, creds...)...)
	if got := decodeObject(t, out); err != nil || got["items"] != float64(1) || len(got["skipped"].([]any)) != 0 {
		t.Fatalf("export: err=%v out=%q stderr=%q, want only the caller's own item", err, out, errb)
	}
	csv := writeVaultFile(t, "name,url,username,password\nother,https://x.example,bob,pw-new-2388\n")
	out, _, err = execSessionCLI(t, nil, append([]string{"vault", "import", csv, "--from", "chrome", "-o", "json"}, creds...)...)
	if got := decodeVaultImportResult(t, out); err != nil || got.Written != 1 || got.SkippedExisting != 0 {
		t.Fatalf("import of a title another person's item has: err=%v %+v, want it stored", err, got)
	}
	if _, kept := stub.items["vault/u-2/other"]; !kept || stub.items["vault/u-2/other"].value != login {
		t.Fatal("an import changed another person's item")
	}
}

// An engine that never stops rate limiting makes an import fail with a word about it, and
// leaves nothing stored.
func TestVaultImportFailsWhenTheRateLimitNeverClears(t *testing.T) {
	old := vaultRateWait
	vaultRateWait = time.Millisecond
	t.Cleanup(func() { vaultRateWait = old })
	stub, creds := newVaultStub(t)
	stub.limitPuts = 1 << 20
	out, _, err := execSessionCLI(t, nil, append([]string{"vault", "import", vaultFixturePath("1password"), "--from", "1password", "-o", "json"}, creds...)...)
	if got := decodeVaultImportResult(t, out); exitcode.From(err) != exitcode.Err || !strings.Contains(err.Error(), "rate limiting") || got.Written != 0 || len(stub.names()) != 0 {
		t.Fatalf("err=%v (exit %d) %+v stored=%v", err, exitcode.From(err), got, stub.names())
	}
}
