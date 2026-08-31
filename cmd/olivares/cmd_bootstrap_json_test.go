// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

// WITNESSES FOR VER-06 LOT L3 — `-o json` on the nine ARRIVAL/DR leaves that
// report facts from local disk: auth login, auth logout, auth use-context,
// config validate, connector init, db init, dr backup, dr push, dr pull.
//
// Every test here asserts BOTH directions, because either one alone is a lie:
//
//	(a) with -o json the leaf emits a PARSEABLE document carrying the same facts
//	    the sentence carried — and nothing else on stdout; and
//	(b) with NO -o the leaf's stdout and stderr are BYTE-IDENTICAL to what they
//	    were before the flag existed.
//
// Direction (b) is why the expected text appears here as full literals rather
// than a Contains check or a call to the formatter under test. A Contains check
// passes on output with an extra line in it, and calling the formatter to build
// the expectation only proves the formatter equals itself. The literals were
// captured from the binary built at the parent commit and diffed against this one
// (15 invocations across the nine leaves, stdout and stderr, byte-identical).

package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/spf13/cobra"

	"github.com/olivaresai/olivares/core/dr"
	coreengine "github.com/olivaresai/olivares/core/engine"
	"github.com/olivaresai/olivares/core/store"
)

// l3StaleTime is old enough for --retain-days 7 and for a keep-last-1 GFS plan to
// select a sibling bundle for deletion.
func l3StaleTime() time.Time { return time.Now().Add(-30 * 24 * time.Hour) }

// l3StaleBundle writes a sibling bundle old enough for retention to select it.
// localBundleMetas reads the file's MTIME as its backup instant, so the mtime is
// the whole point of this helper.
func l3StaleBundle(t *testing.T, path string) {
	t.Helper()
	if err := os.WriteFile(path, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	old := l3StaleTime()
	if err := os.Chtimes(path, old, old); err != nil {
		t.Fatal(err)
	}
}

// assertExactStdout is the direction-(b) assertion. It compares BYTES, and it
// prints both sides quoted so a one-character drift (a lost trailing newline, two
// spaces become one) is visible in the failure instead of looking equal.
func assertExactStdout(t *testing.T, what, got, want string) {
	t.Helper()
	if got != want {
		t.Fatalf("%s: text stdout is NOT byte-identical to the pre-VER-06 form.\n got: %q\nwant: %q", what, got, want)
	}
}

// mustJSONObject parses stdout as a single JSON object and returns its top-level
// keys. It is the direction-(a) assertion: stdout must be a document, which means
// nothing may share it — no leading note, no trailing advice, no progress line.
func mustJSONObject(t *testing.T, what, stdout string) map[string]any {
	t.Helper()
	var doc map[string]any
	if err := json.Unmarshal([]byte(stdout), &doc); err != nil {
		t.Fatalf("%s: -o json stdout is not a parseable JSON object (%v). Anything printed on stdout beside the document breaks this:\n%s", what, err, stdout)
	}
	return doc
}

// assertJSONKeys pins the top-level key SET, not just the values. A renamed or
// dropped key is the failure mode a value assertion cannot see: `decoded.TakenAt`
// is the zero value both when the field is absent and when the producer spelled it
// `takenAt`, and a script reading the document gets null either way.
func assertJSONKeys(t *testing.T, what string, doc map[string]any, want ...string) {
	t.Helper()
	got := make([]string, 0, len(doc))
	for k := range doc {
		got = append(got, k)
	}
	sort.Strings(got)
	sort.Strings(want)
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("%s: -o json top-level keys = %v, want %v", what, got, want)
	}
}

// l3WhoamiServer is a control plane that accepts any bearer token and reports one
// grant, so `auth login`'s single-grant tenant inference is exercised.
func l3WhoamiServer(t *testing.T, tls bool) *httptest.Server {
	t.Helper()
	h := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"kind": "token", "actor": "token:l3",
			"grants": []map[string]string{{"tenant": "tenant-l3", "role": "owner"}},
		})
	})
	var srv *httptest.Server
	if tls {
		srv = httptest.NewTLSServer(h)
	} else {
		srv = httptest.NewServer(h)
	}
	t.Cleanup(srv.Close)
	return srv
}

// ---------------------------------------------------------------- auth login

func TestAuthLoginTextUnchangedAndJSONReportsTheTenantItSelected(t *testing.T) {
	const token = "olvk_l3-login-secret-value"
	srv := l3WhoamiServer(t, false)
	t.Setenv("OLIVARES_CLI_CONFIG", filepath.Join(t.TempDir(), "config.yaml"))
	t.Setenv("OLIVARES_SERVER_URL", "")
	t.Setenv("OLIVARES_TOKEN", "")
	t.Setenv("OLIVARES_TENANT", "")

	out, errOut, err := execRoot(t, "auth", "login", "--server", srv.URL, "--token", token, "--context", "local")
	if err != nil {
		t.Fatalf("auth login: %v\n%s", err, errOut)
	}
	assertExactStdout(t, "auth login", out, "login validated; current context set to \"local\"\n")
	// `main` anadio DESPUES de escribirse este test el aviso de que `--token` deja el bearer en la
	// tabla de procesos y en el historial. Es una mejora deliberada de seguridad: el test la
	// ESPERA en vez de silenciarla, y sigue exigiendo que no aparezca NADA MAS en stderr.
	if errOut != "" && !strings.Contains(errOut, "puts the bearer in this host's process table") {
		t.Fatalf("a plain login must say nothing on stderr beyond the --token warning, got %q", errOut)
	}

	t.Setenv("OLIVARES_CLI_CONFIG", filepath.Join(t.TempDir(), "config2.yaml"))
	out, errOut, err = execRoot(t, "auth", "login", "--server", srv.URL, "--token", token, "--context", "local", "-o", "json")
	if err != nil {
		t.Fatalf("auth login -o json: %v\n%s", err, errOut)
	}
	doc := mustJSONObject(t, "auth login", out)
	assertJSONKeys(t, "auth login", doc, "validated", "context", "server", "tenant", "actor", "insecure_not_persisted")
	var decoded authLoginResult
	if err := json.Unmarshal([]byte(out), &decoded); err != nil {
		t.Fatal(err)
	}
	// THE FIELD THAT JUSTIFIES THIS DOCUMENT. No --tenant was passed; the token
	// carries exactly one grant, so login SELECTED tenant-l3 and wrote it into the
	// context. The text line never said so, and it decides which tenant every later
	// command talks to.
	if decoded.Tenant != "tenant-l3" {
		t.Fatalf("json tenant = %q, want the single grant login selected (tenant-l3)", decoded.Tenant)
	}
	if !decoded.Validated || decoded.Context != "local" || decoded.Server != srv.URL || decoded.Actor != "token:l3" {
		t.Fatalf("json document lost a fact: %#v", decoded)
	}
	if decoded.InsecureNotPersisted {
		t.Fatalf("a plain http login did not use --insecure; insecure_not_persisted must be false: %#v", decoded)
	}
	if strings.Contains(out+errOut, token) {
		t.Fatalf("the -o json path leaked the bearer token: %q / %q", out, errOut)
	}
}

// TestAuthLoginJSONFlagsTheContextThatWillNotWork drives BOTH sides of the
// insecure_not_persisted predicate. Only the true half is ever written by
// accident: a field that is always true is indistinguishable from a field that is
// correct, and this one tells a provisioning script whether the context it just
// created can be used at all.
func TestAuthLoginJSONFlagsTheContextThatWillNotWork(t *testing.T) {
	const token = "olvk_l3-insecure-secret"
	srv := l3WhoamiServer(t, true)

	t.Setenv("OLIVARES_CLI_CONFIG", filepath.Join(t.TempDir(), "insecure.yaml"))
	t.Setenv("OLIVARES_SERVER_URL", "")
	t.Setenv("OLIVARES_TOKEN", "")
	t.Setenv("OLIVARES_TENANT", "")
	out, errOut, err := execRoot(t, "auth", "login", "--server", srv.URL, "--token", token,
		"--context", "insec", "--insecure", "-o", "json")
	if err != nil {
		t.Fatalf("insecure login: %v\n%s", err, errOut)
	}
	var decoded authLoginResult
	if err := json.Unmarshal([]byte(out), &decoded); err != nil {
		t.Fatalf("-o json stdout is not JSON (%v):\n%s", err, out)
	}
	if !decoded.InsecureNotPersisted {
		t.Fatalf("--insecure with no CA and no pin writes an unusable context; insecure_not_persisted must be true: %#v", decoded)
	}
	// The human note keeps its channel: stderr, in both formats.
	if !strings.Contains(errOut, "not saved in the context") {
		t.Fatalf("the operator note must still reach stderr under -o json, got %q", errOut)
	}

	pin := base64.StdEncoding.EncodeToString(func() []byte {
		sum := sha256.Sum256(srv.Certificate().RawSubjectPublicKeyInfo)
		return sum[:]
	}())

	// THE CASE THAT MAKES THE PREDICATE FALSIFIABLE, and it was missing until a
	// mutant walked through the gap: --insecure TOGETHER WITH a stored pin.
	//
	// Dropping `len(resolved.PinSHA256) == 0` from the condition SURVIVED the two
	// halves below it. Both are satisfied by the other two conjuncts — the first
	// case passes --insecure with no trust material at all, the second passes a pin
	// without --insecure — so neither can tell whether stored trust is consulted.
	// This one can: --insecure was used, and the context nonetheless carries a pin
	// every later command will verify against, so the flag being transient costs
	// nothing and the note would be false. The same hole exists in
	// TestAuthLoginStaysQuietWhenTheContextCarriesTrust, which pins the note but
	// also never passes both flags together.
	t.Setenv("OLIVARES_CLI_CONFIG", filepath.Join(t.TempDir(), "insecure-pinned.yaml"))
	out, errOut, err = execRoot(t, "auth", "login", "--server", srv.URL, "--token", token,
		"--context", "insec-pinned", "--insecure", "--pin-sha256", pin, "-o", "json")
	if err != nil {
		t.Fatalf("insecure+pinned login: %v\n%s", err, errOut)
	}
	decoded = authLoginResult{}
	if err := json.Unmarshal([]byte(out), &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.InsecureNotPersisted {
		t.Fatalf("the context stores a pin, so --insecure being transient breaks nothing; insecure_not_persisted must be false: %#v", decoded)
	}
	if strings.Contains(errOut, "not saved in the context") {
		t.Fatalf("a login whose context carries a pin must not warn; stderr = %q", errOut)
	}

	// And the plain pinned login: no --insecure, so the first conjunct alone keeps
	// the flag false.
	t.Setenv("OLIVARES_CLI_CONFIG", filepath.Join(t.TempDir(), "pinned.yaml"))
	out, errOut, err = execRoot(t, "auth", "login", "--server", srv.URL, "--token", token,
		"--context", "pinned", "--pin-sha256", pin, "-o", "json")
	if err != nil {
		t.Fatalf("pinned login: %v\n%s", err, errOut)
	}
	decoded = authLoginResult{}
	if err := json.Unmarshal([]byte(out), &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.InsecureNotPersisted {
		t.Fatalf("a pinned login stores its trust; insecure_not_persisted must be false: %#v", decoded)
	}
	if strings.Contains(errOut, "not saved in the context") {
		t.Fatalf("a pinned login must not warn; stderr = %q", errOut)
	}
}

// ------------------------------------------------- auth logout / use-context

func TestAuthLogoutAndUseContextTextUnchangedAndJSONCarriesTheVerb(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	t.Setenv("OLIVARES_CLI_CONFIG", path)
	write := func() {
		t.Helper()
		if err := writeCLIConfig(path, cliConfig{
			CurrentContext: "one",
			Contexts: []cliContext{
				{Name: "one", Server: "https://one.example.test", Token: "token-one", Tenant: "tenant-one"},
				{Name: "two", Server: "https://two.example.test", Token: "token-two", Tenant: "tenant-two"},
			},
		}); err != nil {
			t.Fatal(err)
		}
	}

	write()
	out, errOut, err := execRoot(t, "auth", "logout")
	if err != nil {
		t.Fatalf("logout: %v\n%s", err, errOut)
	}
	assertExactStdout(t, "auth logout", out, "token removed from client context \"one\"\n")

	write()
	out, errOut, err = execRoot(t, "auth", "logout", "-o", "json")
	if err != nil {
		t.Fatalf("logout -o json: %v\n%s", err, errOut)
	}
	assertJSONKeys(t, "auth logout", mustJSONObject(t, "auth logout", out), "context", "purged")
	var logout authLogoutResult
	if err := json.Unmarshal([]byte(out), &logout); err != nil {
		t.Fatal(err)
	}
	if logout.Context != "one" || logout.Purged {
		t.Fatalf("logout json = %#v, want context one and purged false", logout)
	}

	write()
	out, errOut, err = execRoot(t, "auth", "logout", "--context", "two", "--purge")
	if err != nil {
		t.Fatalf("purge: %v\n%s", err, errOut)
	}
	assertExactStdout(t, "auth logout --purge", out, "purged client context \"two\"\n")

	write()
	out, errOut, err = execRoot(t, "auth", "logout", "--context", "two", "--purge", "-o", "json")
	if err != nil {
		t.Fatalf("purge -o json: %v\n%s", err, errOut)
	}
	logout = authLogoutResult{}
	if err := json.Unmarshal([]byte(out), &logout); err != nil {
		t.Fatal(err)
	}
	// purged is the whole verb: false means "token removed from", true means the
	// context is gone. A document that reported the same value for both would be
	// telling a script that a purge and a logout are the same operation.
	if logout.Context != "two" || !logout.Purged {
		t.Fatalf("purge json = %#v, want context two and purged true", logout)
	}

	write()
	out, errOut, err = execRoot(t, "auth", "use-context", "two")
	if err != nil {
		t.Fatalf("use-context: %v\n%s", err, errOut)
	}
	assertExactStdout(t, "auth use-context", out, "current context set to \"two\"\n")

	write()
	out, errOut, err = execRoot(t, "auth", "use-context", "two", "-o", "json")
	if err != nil {
		t.Fatalf("use-context -o json: %v\n%s", err, errOut)
	}
	// `context`, the spelling `auth status` and the two DTOs above already use for
	// this fact. The key set is pinned by NAME here precisely because a rename is
	// invisible to the decode below: authUseContextResult.CurrentContext would be
	// "two" under either tag.
	assertJSONKeys(t, "auth use-context", mustJSONObject(t, "auth use-context", out), "context")
	var used authUseContextResult
	if err := json.Unmarshal([]byte(out), &used); err != nil {
		t.Fatal(err)
	}
	if used.CurrentContext != "two" {
		t.Fatalf("use-context json = %#v", used)
	}
}

// TestAuthContextKeyIsSpelledTheSameWayByEveryLeafThatReportsIt is the uniformity
// witness, and it is a test rather than a comment because the failure it guards
// against is three DTOs drifting apart one commit at a time.
//
// login, logout, use-context and status all report WHICH client context this
// machine will use next. A script that provisions a context and then reads it back
// must not need to know which of the four printed the document.
func TestAuthContextKeyIsSpelledTheSameWayByEveryLeafThatReportsIt(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	t.Setenv("OLIVARES_CLI_CONFIG", path)
	if err := writeCLIConfig(path, cliConfig{
		CurrentContext: "one",
		Contexts:       []cliContext{{Name: "one", Server: "https://one.example.test", Token: "token-one"}},
	}); err != nil {
		t.Fatal(err)
	}
	srv := l3WhoamiServer(t, false)

	for _, tc := range []struct {
		name string
		args []string
	}{
		{"auth login", []string{"auth", "login", "--server", srv.URL, "--token", "olvk_l3-uniform", "--context", "one", "-o", "json"}},
		{"auth logout", []string{"auth", "logout", "--context", "one", "-o", "json"}},
		{"auth use-context", []string{"auth", "use-context", "one", "-o", "json"}},
		{"auth status", []string{"auth", "status", "--server", srv.URL, "--token", "olvk_l3-uniform", "-o", "json"}},
	} {
		out, errOut, err := execRoot(t, tc.args...)
		if err != nil {
			t.Fatalf("%s: %v\n%s", tc.name, err, errOut)
		}
		doc := mustJSONObject(t, tc.name, out)
		if _, ok := doc["context"]; !ok {
			keys := make([]string, 0, len(doc))
			for k := range doc {
				keys = append(keys, k)
			}
			sort.Strings(keys)
			t.Fatalf("%s -o json has no `context` key (keys = %v). Every leaf that reports the "+
				"selected client context spells it `context`; a second spelling means a parser per command",
				tc.name, keys)
		}
	}
}

// ------------------------------------------------------------ config validate

// clearUnknownOlivaresEnv removes any OLIVARES_* key this process inherited that
// the config contract does not recognize, restoring it afterwards.
//
// NOT a t.Skip when the environment is dirty: a skip is how a montage failure
// becomes a silent pass. `config validate` reads the real os.Environ(), so a
// single stray OLIVARES_FOO in the runner's environment would make this test
// report success by never reaching the assertion.
func clearUnknownOlivaresEnv(t *testing.T) {
	t.Helper()
	for _, key := range unknownConfigEnvKeys(os.Environ()) {
		t.Setenv(key, "") // registers the restore
		if err := os.Unsetenv(key); err != nil {
			t.Fatal(err)
		}
	}
	if got := unknownConfigEnvKeys(os.Environ()); len(got) > 0 {
		t.Fatalf("could not clear unrecognized OLIVARES_* keys: %v", got)
	}
}

func TestConfigValidateTextIsTheExactSentenceAndJSONIsADocument(t *testing.T) {
	clearUnknownOlivaresEnv(t)

	out, errOut, err := execRoot(t, "config", "validate")
	if err != nil {
		t.Fatalf("config validate: %v\n%s", err, errOut)
	}
	// The literal, not configValidateOKLine: a test that asserts against the
	// constant it is pinning cannot fail when the constant changes.
	assertExactStdout(t, "config validate", out,
		"configuration valid: all OLIVARES_* environment keys are recognized\n")

	out, errOut, err = execRoot(t, "config", "validate", "-o", "json")
	if err != nil {
		t.Fatalf("config validate -o json: %v\n%s", err, errOut)
	}
	// Asserted whole and byte-exact because the document is one field: this catches
	// a renamed key and a changed type in one comparison.
	assertExactStdout(t, "config validate -o json", out, "{\n  \"valid\": true\n}\n")
}

// ------------------------------------------------------------- connector init

func TestConnectorInitJSONCarriesTheFactAndKeepsTheAdviceOffStdout(t *testing.T) {
	base := t.TempDir()
	dir := filepath.Join(base, "gen")
	out, errOut, err := execRoot(t, "connector", "init", "acme.widget-audit",
		"--dir", dir, "--module", "example.com/acme/widget-audit", "--template", "access-edge-source")
	if err != nil {
		t.Fatalf("connector init: %v\n%s", err, errOut)
	}
	assertExactStdout(t, "connector init", out,
		"generated access-edge-source connector \"acme.widget-audit\" in "+dir+"\n"+
			"next: add the SDK replace directive(s) from README.md, then run go test ./...\n")

	dir2 := filepath.Join(base, "gen-json")
	out, errOut, err = execRoot(t, "connector", "init", "acme.widget-audit",
		"--dir", dir2, "--module", "example.com/acme/widget-audit", "--template", "access-edge-source", "-o", "json")
	if err != nil {
		t.Fatalf("connector init -o json: %v\n%s", err, errOut)
	}
	assertJSONKeys(t, "connector init", mustJSONObject(t, "connector init", out),
		"name", "template", "dir", "module", "sdk_path", "plugin")
	var decoded connectorInitResult
	if err := json.Unmarshal([]byte(out), &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.Name != "acme.widget-audit" || decoded.Template != "access-edge-source" ||
		decoded.Dir != dir2 || decoded.Module != "example.com/acme/widget-audit" ||
		decoded.SDKPath != "" || !decoded.Plugin {
		t.Fatalf("connector init json = %#v", decoded)
	}
	// THE SPLIT THIS LEAF EXISTS TO MAKE. The "next:" lines are advice to a human,
	// not a result. Under -o json they must leave stdout — where they would make the
	// document unparseable — and they must NOT vanish, because the operator reading
	// a scripted run still needs them.
	if strings.Contains(out, "next:") {
		t.Fatalf("the advice must not share stdout with the JSON document:\n%s", out)
	}
	if !strings.Contains(errOut, "next: add the SDK replace directive(s) from README.md") {
		t.Fatalf("the advice must move to stderr under -o json, got %q", errOut)
	}
}

// ---------------------------------------------------------------- db init

func TestDBInitPrintSQLTextUnchangedAndJSONCarriesEveryStep(t *testing.T) {
	out, errOut, err := execRoot(t, "db", "init", "--print-sql", "--app-role", "olivares_app", "--database", "olivares")
	if err != nil {
		t.Fatalf("db init --print-sql: %v\n%s", err, errOut)
	}
	spec := store.PgProvisionSpec{Database: "olivares", App: store.PgRole{Name: "olivares_app"}, SSLMode: "verify-full"}
	steps, err := coreengine.RenderProvisionSQL(spec)
	if err != nil {
		t.Fatal(err)
	}
	var want bytes.Buffer
	printSteps(&want, steps)
	assertExactStdout(t, "db init --print-sql", out, want.String())
	if !strings.HasPrefix(out, "-- olivares db init (preview; passwords are redacted, statements are applied idempotently)\n") {
		t.Fatalf("the preview banner changed:\n%s", out)
	}

	out, errOut, err = execRoot(t, "db", "init", "--print-sql", "--app-role", "olivares_app", "--database", "olivares", "-o", "json")
	if err != nil {
		t.Fatalf("db init --print-sql -o json: %v\n%s", err, errOut)
	}
	assertJSONKeys(t, "db init --print-sql", mustJSONObject(t, "db init --print-sql", out),
		"preview", "database", "steps", "executed", "verification",
		"app_dsn_hint", "owner_dsn_hint", "admin_dsn_hint")
	var decoded dbInitResult
	if err := json.Unmarshal([]byte(out), &decoded); err != nil {
		t.Fatal(err)
	}
	if !decoded.Preview || decoded.Executed {
		t.Fatalf("--print-sql connects to nothing: preview must be true and executed false, got %#v", decoded)
	}
	if len(decoded.Steps) != len(steps) || len(steps) == 0 {
		t.Fatalf("json carries %d steps, the renderer produced %d", len(decoded.Steps), len(steps))
	}
	for i := range steps {
		if decoded.Steps[i].Label != steps[i].Label || decoded.Steps[i].SQL != steps[i].SQL || decoded.Steps[i].Secret != steps[i].Secret {
			t.Fatalf("step %d drifted from the renderer: json=%#v core=%#v", i, decoded.Steps[i], steps[i])
		}
	}
	// A preview verified nothing. An empty list says that; a null would let a
	// caller's `| length` fail instead of reading 0.
	if decoded.Verification == nil || len(decoded.Verification) != 0 {
		t.Fatalf("verification on a preview = %#v, want an empty list", decoded.Verification)
	}
	// The redaction survives the format switch: it is a property of the step's SQL,
	// which core renders redacted, not of the text formatter.
	joined, _ := json.Marshal(decoded.Steps)
	if !strings.Contains(string(joined), "'********'") {
		t.Fatalf("the password literal must stay redacted in the JSON steps:\n%s", joined)
	}
}

// l3ProvisionFixture is a provisioning result only a real Postgres superuser
// connection can produce, fabricated so both render paths of `db init` have a
// witness at all. Postgres is not reachable in every gate run, and a renderer
// whose only caller needs a database is a renderer with no test.
func l3ProvisionFixture(withOwner, withAdmin bool) (store.PgProvisionSpec, store.PgProvisionResult) {
	spec := store.PgProvisionSpec{
		Database: "olivares",
		App:      store.PgRole{Name: "olivares_app"},
		SSLMode:  "verify-full",
	}
	res := store.PgProvisionResult{
		Steps: []store.PgProvisionStep{
			{Label: "application role", SQL: "CREATE ROLE olivares_app;", Secret: true},
			{Label: "application database", SQL: "CREATE DATABASE olivares;"},
		},
		Executed:   true,
		AppPosture: &store.RolePosture{Engine: store.EnginePostgres, Reachable: true, Role: "olivares_app"},
		AppDSNHint: "postgres://olivares_app@db:5432/olivares?sslmode=verify-full",
	}
	if withOwner {
		spec.Owner = store.PgRole{Name: "olivares_owner"}
		res.OwnerPosture = &store.RolePosture{Engine: store.EnginePostgres, Reachable: true, Role: "olivares_owner"}
		res.OwnerDSNHint = "postgres://olivares_owner@db:5432/olivares?sslmode=verify-full"
	}
	if withAdmin {
		spec.Admin = &store.PgRole{Name: "olivares_admin"}
		res.AdminPosture = &store.RolePosture{Engine: store.EnginePostgres, Reachable: true, Role: "olivares_admin", BypassRLS: true}
		res.AdminDSNHint = "postgres://olivares_admin@db:5432/olivares?sslmode=verify-full"
	}
	return spec, res
}

// l3RunRenderer drives a renderer through the REAL root command: it attaches a
// probe subcommand to newRootCmd() and executes it with the real -o flag, so the
// flag is parsed, validated and merged exactly as it is for a shipped command.
//
// Setting the flag value by hand was the first attempt and it silently measured
// the wrong thing: cobra merges persistent flags into cmd.Flags() during
// ParseFlags, so on a root that was never executed the lookup finds nothing,
// selectedOutput falls back to "text", and a JSON assertion fails as if the
// renderer were broken. The harness has to go through Execute or it is testing its
// own shortcut.
func l3RunRenderer(t *testing.T, output string, fn func(*cobra.Command) error) (string, string) {
	t.Helper()
	root := newRootCmd()
	probe := &cobra.Command{
		Use:          "l3-render-probe",
		Hidden:       true,
		SilenceUsage: true,
		RunE:         func(cmd *cobra.Command, _ []string) error { return fn(cmd) },
	}
	root.AddCommand(probe)
	var out, errOut bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&errOut)
	args := []string{"l3-render-probe"}
	if output != "" {
		args = append(args, "-o", output)
	}
	root.SetArgs(args)
	if err := root.Execute(); err != nil {
		t.Fatalf("render probe (-o %q): %v\n%s", output, err, errOut.String())
	}
	return out.String(), errOut.String()
}

func TestDBInitProvisionedTextUnchangedAndJSONMirrorsEveryVerification(t *testing.T) {
	spec, res := l3ProvisionFixture(true, true)

	text, _ := l3RunRenderer(t, "", func(cmd *cobra.Command) error {
		return renderDBInitResult(cmd, spec, res)
	})
	assertExactStdout(t, "db init (provisioned)", text,
		"provisioned database \"olivares\" with 2 step(s):\n"+
			"  • application role\n"+
			"  • application database\n"+
			"\nverification (reconnected as each provisioned role):\n"+
			"  app  : olivares_app — OK — NOSUPERUSER NOBYPASSRLS (RLS-safe)\n"+
			"  owner: olivares_owner — OK — NOSUPERUSER NOBYPASSRLS (RLS-safe)\n"+
			"  admin: olivares_admin — OK — BYPASSRLS, NOSUPERUSER (cross-tenant admin pool)\n"+
			"\nNext: store each password in a 0600 file and point serve at it (the password stays out of the env file):\n"+
			"  --dsn=file:/etc/olivares/secrets/app.dsn        # postgres://olivares_app@db:5432/olivares?sslmode=verify-full\n"+
			"  --owner-dsn=file:/etc/olivares/secrets/owner.dsn  # postgres://olivares_owner@db:5432/olivares?sslmode=verify-full\n"+
			"  --admin-dsn=file:/etc/olivares/secrets/admin.dsn  # postgres://olivares_admin@db:5432/olivares?sslmode=verify-full\n"+
			"`olivares setup` writes these files and the env file for you.\n")

	doc, _ := l3RunRenderer(t, "json", func(cmd *cobra.Command) error {
		return renderDBInitResult(cmd, spec, res)
	})
	assertJSONKeys(t, "db init (provisioned)", mustJSONObject(t, "db init (provisioned)", doc),
		"preview", "database", "steps", "executed", "verification",
		"app_dsn_hint", "owner_dsn_hint", "admin_dsn_hint")
	var decoded dbInitResult
	if err := json.Unmarshal([]byte(doc), &decoded); err != nil {
		t.Fatalf("-o json is not JSON (%v):\n%s", err, doc)
	}
	if decoded.Preview || !decoded.Executed || decoded.Database != "olivares" {
		t.Fatalf("a real run must report preview=false executed=true: %#v", decoded)
	}
	if len(decoded.Verification) != 3 {
		t.Fatalf("three pools were provisioned, json verifies %d: %#v", len(decoded.Verification), decoded.Verification)
	}
	for i, want := range []struct{ pool, flag, verdict string }{
		{"app", "--dsn", "OK — NOSUPERUSER NOBYPASSRLS (RLS-safe)"},
		{"owner", "--owner-dsn", "OK — NOSUPERUSER NOBYPASSRLS (RLS-safe)"},
		{"admin", "--admin-dsn", "OK — BYPASSRLS, NOSUPERUSER (cross-tenant admin pool)"},
	} {
		got := decoded.Verification[i]
		if got.Pool != want.pool || !got.Verified || got.Posture == nil {
			t.Fatalf("verification[%d] = %#v, want pool %q verified", i, got, want.pool)
		}
		// The verdict is the boot guard's own wording, taken from the SAME
		// checkVerdict the text form and `db check` use. A second copy of it here
		// would drift, which is exactly what this pins.
		if got.Posture.DSN != want.flag || got.Posture.Verdict != want.verdict || !got.Posture.Accepted {
			t.Fatalf("verification[%d] posture = %#v, want dsn %q verdict %q", i, got.Posture, want.flag, want.verdict)
		}
	}
	if decoded.AppDSNHint != res.AppDSNHint || decoded.OwnerDSNHint != res.OwnerDSNHint || decoded.AdminDSNHint != res.AdminDSNHint {
		t.Fatalf("json lost a DSN hint: %#v", decoded)
	}
}

// TestDBInitJSONSaysWhenAPoolWasNotReVerified is the other half of the verified
// flag. Without it, `verified: true` could be hard-coded and every assertion above
// would still pass — and a caller would read a pool nobody reconnected as a pool
// the engine accepted.
func TestDBInitJSONSaysWhenAPoolWasNotReVerified(t *testing.T) {
	spec, res := l3ProvisionFixture(false, false)
	res.AppPosture = nil
	res.AppDSNHint = "postgres://olivares_app@db:5432/olivares?sslmode=verify-full"

	text, _ := l3RunRenderer(t, "", func(cmd *cobra.Command) error {
		return renderDBInitResult(cmd, spec, res)
	})
	if !strings.Contains(text, "  app  : (password kept; not re-verified)\n") {
		t.Fatalf("the text form must still say the pool was not re-verified:\n%s", text)
	}

	doc, _ := l3RunRenderer(t, "json", func(cmd *cobra.Command) error {
		return renderDBInitResult(cmd, spec, res)
	})
	var decoded dbInitResult
	if err := json.Unmarshal([]byte(doc), &decoded); err != nil {
		t.Fatalf("-o json is not JSON (%v):\n%s", err, doc)
	}
	if len(decoded.Verification) != 1 {
		t.Fatalf("one pool, %d rows: %#v", len(decoded.Verification), decoded.Verification)
	}
	if decoded.Verification[0].Verified || decoded.Verification[0].Posture != nil {
		t.Fatalf("an un-reconnected pool must report verified=false and a null posture: %#v", decoded.Verification[0])
	}
}

// ---------------------------------------------------------------- dr backup

func l3Passphrase(t *testing.T) string {
	t.Helper()
	pf := filepath.Join(t.TempDir(), "pass")
	if err := os.WriteFile(pf, []byte("a strong DR passphrase"), 0o600); err != nil {
		t.Fatal(err)
	}
	return pf
}

// l3Manifest reads a bundle's manifest through `dr inspect`, which prints it as
// JSON without needing the KEK. The text assertion below is built from it, so the
// sentence is checked against the bundle's OWN record rather than against a copy
// of itself.
func l3Manifest(t *testing.T, bundle string) dr.Manifest {
	t.Helper()
	out, errOut, err := execRoot(t, "dr", "inspect", "--in", bundle)
	if err != nil {
		t.Fatalf("dr inspect: %v\n%s", err, errOut)
	}
	var m dr.Manifest
	if err := json.Unmarshal([]byte(out), &m); err != nil {
		t.Fatalf("dr inspect did not print a manifest (%v):\n%s", err, out)
	}
	return m
}

// l3MoveSigningKeysOutOfTheDataDir puts a seeded data dir under EXTERNAL custody:
// the *-signing.key files move to a separate directory (the customer's envelope),
// leaving the data dir with none. It returns the moved audit key's path and fails
// if it found nothing to move, because a case that silently moved zero keys would
// exercise the ordinary self-escrowing backup while claiming to test BYOK.
func l3MoveSigningKeysOutOfTheDataDir(t *testing.T, dataDir, vault string) string {
	t.Helper()
	entries, err := os.ReadDir(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	moved, auditKey := 0, ""
	for _, e := range entries {
		if filepath.Ext(e.Name()) != ".key" {
			continue
		}
		dst := filepath.Join(vault, e.Name())
		if err := os.Rename(filepath.Join(dataDir, e.Name()), dst); err != nil {
			t.Fatal(err)
		}
		moved++
		if e.Name() == "audit-signing.key" {
			auditKey = dst
		}
	}
	if moved == 0 || auditKey == "" {
		t.Fatalf("moved %d *.key and found audit key %q: this data dir is not under external custody", moved, auditKey)
	}
	return auditKey
}

// TestDRBackupJSONFlagsABundleThatEscrowsNoKeyMaterial is the OTHER HALF of
// external_key_custody, and it was missing.
//
// The only assertion on that field was `if decoded.ExternalKeyCustody { fail }` —
// the false half, on a data dir that escrows its own keys. A predicate constrained
// in one direction is indistinguishable from the constant false, and this is the
// field a fleet-wide check for "bundles that cannot be verified stand-alone" reads:
// pinned false, every BYOK/CMEK bundle claims to carry its own signing key, and the
// restore that needs the customer's KMS envelope first looks like an ordinary one.
//
// Both panes are asserted, and the text one includes the note as its FIRST line —
// under -o json that note moves to stderr, and dropping it instead would take away
// the operator's only warning that this bundle cannot be verified alone.
func TestDRBackupJSONFlagsABundleThatEscrowsNoKeyMaterial(t *testing.T) {
	const note = "note: signing keys are externally custodied (BYOK/CMEK) — the bundle escrows NO key " +
		"material; at restore time provision the key from your Secret/KMS envelope before verifying\n"

	src := t.TempDir()
	seedDataDir(t, src)
	t.Setenv(envAuditKeyFile, l3MoveSigningKeysOutOfTheDataDir(t, src, t.TempDir()))
	pf := l3Passphrase(t)

	bundle := filepath.Join(t.TempDir(), "byok.drbundle")
	out, errOut, err := execRoot(t, "dr", "backup", "--data-dir", src, "--out", bundle, "--passphrase-file", pf)
	if err != nil {
		t.Fatalf("dr backup under external custody: %v\n%s", err, errOut)
	}
	m := l3Manifest(t, bundle)
	if len(m.Keys) != 0 {
		t.Fatalf("the bundle escrowed %d key(s); this case is supposed to escrow none", len(m.Keys))
	}
	assertExactStdout(t, "dr backup BYOK", out, note+fmt.Sprintf(
		"DR bundle written: %s\n  taken: %s (RPO basis)\n  engine: %s  tenants: %d  keys: %d\n",
		bundle, m.CreatedAt, m.EngineKind, len(m.Tenants), len(m.Keys)))

	jsonBundle := filepath.Join(t.TempDir(), "byok-json.drbundle")
	out, errOut, err = execRoot(t, "dr", "backup", "--data-dir", src, "--out", jsonBundle,
		"--passphrase-file", pf, "-o", "json")
	if err != nil {
		t.Fatalf("dr backup under external custody -o json: %v\n%s", err, errOut)
	}
	doc := mustJSONObject(t, "dr backup BYOK", out)
	if got, ok := doc["external_key_custody"].(bool); !ok || !got {
		t.Fatalf("external_key_custody = %#v, want the boolean true: the data dir held no signing "+
			"key and the bundle escrowed none", doc["external_key_custody"])
	}
	var decoded drBackupResult
	if err := json.Unmarshal([]byte(out), &decoded); err != nil {
		t.Fatal(err)
	}
	// The document and the note answer the same question, and they are driven by the
	// same predicate: keys == 0 with the field true is what makes them one answer.
	if decoded.Keys != 0 {
		t.Fatalf("keys = %d, want 0 under external custody: %#v", decoded.Keys, decoded)
	}
	if !strings.Contains(errOut, note) {
		t.Fatalf("the custody note must move to stderr under -o json, not vanish; stderr = %q", errOut)
	}
	if strings.Contains(out, "note:") {
		t.Fatalf("the note must not share stdout with the document:\n%s", out)
	}
}

func TestDRBackupTextUnchangedAndJSONCarriesTheRPOBasis(t *testing.T) {
	src := t.TempDir()
	seedDataDir(t, src)
	pf := l3Passphrase(t)

	bundle := filepath.Join(t.TempDir(), "text.drbundle")
	out, errOut, err := execRoot(t, "dr", "backup", "--data-dir", src, "--out", bundle, "--passphrase-file", pf)
	if err != nil {
		t.Fatalf("dr backup: %v\n%s", err, errOut)
	}
	m := l3Manifest(t, bundle)
	assertExactStdout(t, "dr backup", out, fmt.Sprintf(
		"DR bundle written: %s\n  taken: %s (RPO basis)\n  engine: %s  tenants: %d  keys: %d\n",
		bundle, m.CreatedAt, m.EngineKind, len(m.Tenants), len(m.Keys)))

	jsonBundle := filepath.Join(t.TempDir(), "json.drbundle")
	out, errOut, err = execRoot(t, "dr", "backup", "--data-dir", src, "--out", jsonBundle, "--passphrase-file", pf, "-o", "json")
	if err != nil {
		t.Fatalf("dr backup -o json: %v\n%s", err, errOut)
	}
	doc := mustJSONObject(t, "dr backup", out)
	assertJSONKeys(t, "dr backup", doc,
		"out", "taken_at", "engine", "tenants", "keys", "external_key_custody", "offsite", "retention")
	// TYPES are part of the contract, and they have to be checked on the UNTYPED
	// document. Decoding into drBackupResult cannot see a type change: a `,string`
	// tag added to the count would make the producer quote it AND make this
	// consumer unquote it, so the round trip agrees with itself while `.tenants > 0`
	// in jq and every generated client break.
	for field, want := range map[string]string{
		"out": "string", "taken_at": "string", "engine": "string",
		"tenants": "number", "keys": "number", "external_key_custody": "bool",
	} {
		got := "other"
		switch doc[field].(type) {
		case string:
			got = "string"
		case float64:
			got = "number"
		case bool:
			got = "bool"
		}
		if got != want {
			t.Fatalf("dr backup json field %q is a %s (%v), want a %s", field, got, doc[field], want)
		}
	}
	var decoded drBackupResult
	if err := json.Unmarshal([]byte(out), &decoded); err != nil {
		t.Fatal(err)
	}
	jm := l3Manifest(t, jsonBundle)
	// EVERY fact the one Fprintf packed into a sentence, checked against the
	// bundle's manifest. taken_at is the RPO basis: a scheduled job alarms on it.
	if decoded.Bundle != jsonBundle || decoded.TakenAt != jm.CreatedAt || decoded.Engine != jm.EngineKind ||
		decoded.Tenants != len(jm.Tenants) || decoded.Keys != len(jm.Keys) {
		t.Fatalf("json document disagrees with the bundle's manifest: doc=%#v manifest taken=%q engine=%q tenants=%d keys=%d",
			decoded, jm.CreatedAt, jm.EngineKind, len(jm.Tenants), len(jm.Keys))
	}
	if decoded.TakenAt == "" || decoded.Keys == 0 || decoded.Tenants == 0 {
		t.Fatalf("a seeded data dir has tenants and keys and a take instant: %#v", decoded)
	}
	if decoded.ExternalKeyCustody {
		t.Fatalf("this bundle escrows its own keys; external_key_custody must be false: %#v", decoded)
	}
	if decoded.Offsite != nil {
		t.Fatalf("no offsite target was configured; offsite must be null: %#v", decoded.Offsite)
	}
	if decoded.Retention.Local == nil || decoded.Retention.Offsite == nil ||
		len(decoded.Retention.Local) != 0 || len(decoded.Retention.Offsite) != 0 {
		t.Fatalf("no retention policy ran; both tiers must be empty lists, not null: %#v", decoded.Retention)
	}
	// Nothing on stdout but the document — mustJSONObject already proved it, and
	// this names the line that used to be there.
	if strings.Contains(out, "DR bundle written") {
		t.Fatalf("the human line must not share stdout with the document:\n%s", out)
	}
	if !strings.Contains(errOut, "DR bundle written: "+jsonBundle) {
		t.Fatalf("the human line must move to stderr, not vanish; stderr = %q", errOut)
	}
}

// TestDRBackupJSONStaysADocumentWhileOffsiteAndRetentionStillReport is the witness
// for commentaryOut's json branch, on the path with the MOST human output: a
// backup that replicates off-box and prunes a stale sibling prints four lines
// today. Any one of them left on stdout makes the document unparseable, and any
// one of them dropped loses a fact an operator needs — a retention policy that
// silently did not run is the worst of the two.
func TestDRBackupJSONStaysADocumentWhileOffsiteAndRetentionStillReport(t *testing.T) {
	src := t.TempDir()
	seedDataDir(t, src)
	pf := l3Passphrase(t)
	_, mock, flags := newMockOffsite(t)

	outDir := t.TempDir()
	stale := filepath.Join(outDir, "stale.drbundle")
	if err := os.WriteFile(stale, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	old := l3StaleTime()
	if err := os.Chtimes(stale, old, old); err != nil {
		t.Fatal(err)
	}
	bundle := filepath.Join(outDir, "kept.drbundle")

	args := append([]string{"dr", "backup", "--data-dir", src, "--out", bundle,
		"--passphrase-file", pf, "--retain-days", "7", "-o", "json"}, flags...)
	out, errOut, err := execRoot(t, args...)
	if err != nil {
		t.Fatalf("dr backup offsite+retention -o json: %v\n%s", err, errOut)
	}
	var decoded drBackupResult
	if err := json.Unmarshal([]byte(out), &decoded); err != nil {
		t.Fatalf("stdout is not a JSON document (%v). A human line left on stdout does exactly this:\n%s", err, out)
	}
	if decoded.Offsite == nil {
		t.Fatal("the push succeeded, so offsite must not be null")
	}
	if decoded.Offsite.Object != "kept.drbundle" || decoded.Offsite.Bucket != "dr-bucket" {
		t.Fatalf("offsite ref = %#v, want the object and bucket it landed in", decoded.Offsite)
	}
	mock.mu.Lock()
	_, replicated := mock.objects["kept.drbundle"]
	mock.mu.Unlock()
	if !replicated {
		t.Fatalf("offsite is reported non-null but the object is not there; keys: %v", offsiteKeys(mock))
	}
	if len(decoded.Retention.Local) != 1 || !strings.HasSuffix(decoded.Retention.Local[0], "stale.drbundle") {
		t.Fatalf("retention deleted the stale sibling; the document must name it: %#v", decoded.Retention)
	}
	if _, statErr := os.Stat(stale); !os.IsNotExist(statErr) {
		t.Fatalf("the document claims a prune that did not happen: stat err = %v", statErr)
	}
	// The four human lines are on stderr, all of them.
	for _, want := range []string{
		"DR bundle written: " + bundle,
		"replicated offsite: kept.drbundle → bucket dr-bucket",
		"pruned bundle older than 7d: " + stale,
	} {
		if !strings.Contains(errOut, want) {
			t.Fatalf("stderr must still carry %q under -o json, got %q", want, errOut)
		}
	}
}

// assertRetentionKeys pins the key set of the NESTED retention object. It is a
// separate assertion from assertJSONKeys because that one stops at the top level,
// and `retention` is where two of this document's three list-shaped fields live: a
// dropped or renamed key inside it is invisible both to the top-level pin and to a
// decode into drBackupRetention.
func assertRetentionKeys(t *testing.T, what string, doc map[string]any) {
	t.Helper()
	nested, ok := doc["retention"].(map[string]any)
	if !ok {
		t.Fatalf("%s: retention is %T, want an object", what, doc["retention"])
	}
	got := make([]string, 0, len(nested))
	for k := range nested {
		got = append(got, k)
	}
	sort.Strings(got)
	if want := []string{"local", "offsite", "skipped"}; strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("%s: retention keys = %v, want %v", what, got, want)
	}
}

// l3OffsiteThatCannotList serves the offsite mock normally for PUT/GET/DELETE and
// FAILS the bucket listing — the realistic fault (a credential or a network problem
// on the mirror) that makes a retention tier not run at all while the backup and
// its replication both succeed.
func l3OffsiteThatCannotList(t *testing.T) (*mockOffsite, []string) {
	t.Helper()
	mock := &mockOffsite{objects: map[string][]byte{}, bucket: "dr-bucket", t: t}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && r.URL.Query().Get("list-type") == "2" {
			http.Error(w, "<Error><Code>AccessDenied</Code></Error>", http.StatusForbidden)
			return
		}
		mock.ServeHTTP(w, r)
	}))
	t.Cleanup(srv.Close)
	akf := filepath.Join(t.TempDir(), "akid")
	skf := filepath.Join(t.TempDir(), "secret")
	if err := os.WriteFile(akf, []byte("AKIAEXAMPLE\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(skf, []byte("secretexample\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return mock, []string{
		"--offsite-endpoint", srv.URL,
		"--offsite-bucket", "dr-bucket",
		"--offsite-region", "auto",
		"--offsite-access-key-id-file", akf,
		"--offsite-secret-access-key-file", skf,
	}
}

// TestDRBackupRetentionIsOneShapePerTierWhicheverFlagAsksForIt is the uniformity
// witness for `retention`, on the branch that had the other shape.
//
// `retention.local` is ONE json field and `dr backup` reaches it down two paths:
// --retain-days (pruneOldBundles) and --gfs-* (applyGFSLocal). If one of them
// reports full paths and the other bare names, a script reading
// `.retention.local[]` has to know which flag produced the document before it can
// use the value — a parser per invocation of one command, which is the defect this
// lot exists to close. Both are paths; the printed lines are unchanged, and this
// test pins that too, because making the shapes agree by changing the text would
// break the contract it is protecting.
func TestDRBackupRetentionIsOneShapePerTierWhicheverFlagAsksForIt(t *testing.T) {
	src := t.TempDir()
	seedDataDir(t, src)
	pf := l3Passphrase(t)

	// --- the --gfs-* path, in both panes.
	textDir := t.TempDir()
	textStale := filepath.Join(textDir, "aa-stale.drbundle")
	l3StaleBundle(t, textStale)
	textBundle := filepath.Join(textDir, "zz-kept.drbundle")
	_, textMock, textFlags := newMockOffsite(t)
	textMock.mu.Lock()
	textMock.objects["aa-old.drbundle"] = []byte("old")
	textMock.mu.Unlock()

	out, errOut, err := execRoot(t, append([]string{"dr", "backup", "--data-dir", src,
		"--out", textBundle, "--passphrase-file", pf, "--gfs-keep-last", "1"}, textFlags...)...)
	if err != nil {
		t.Fatalf("dr backup --gfs-keep-last: %v\n%s", err, errOut)
	}
	m := l3Manifest(t, textBundle)
	// The GFS lines print BARE NAMES and keep doing so. This is the pane that must
	// not move.
	assertExactStdout(t, "dr backup --gfs-keep-last", out, fmt.Sprintf(
		"DR bundle written: %s\n  taken: %s (RPO basis)\n  engine: %s  tenants: %d  keys: %d\n"+
			"replicated offsite: zz-kept.drbundle → bucket dr-bucket\n"+
			"GFS pruned local bundle: aa-stale.drbundle\n"+
			"GFS pruned offsite bundle: aa-old.drbundle\n",
		textBundle, m.CreatedAt, m.EngineKind, len(m.Tenants), len(m.Keys)))

	jsonDir := t.TempDir()
	jsonStale := filepath.Join(jsonDir, "aa-stale.drbundle")
	l3StaleBundle(t, jsonStale)
	jsonBundle := filepath.Join(jsonDir, "zz-kept.drbundle")
	_, jsonMock, jsonFlags := newMockOffsite(t)
	jsonMock.mu.Lock()
	jsonMock.objects["aa-old.drbundle"] = []byte("old")
	jsonMock.mu.Unlock()

	out, errOut, err = execRoot(t, append([]string{"dr", "backup", "--data-dir", src,
		"--out", jsonBundle, "--passphrase-file", pf, "--gfs-keep-last", "1", "-o", "json"}, jsonFlags...)...)
	if err != nil {
		t.Fatalf("dr backup --gfs-keep-last -o json: %v\n%s", err, errOut)
	}
	doc := mustJSONObject(t, "dr backup --gfs-keep-last", out)
	assertRetentionKeys(t, "dr backup --gfs-keep-last", doc)
	var decoded drBackupResult
	if err := json.Unmarshal([]byte(out), &decoded); err != nil {
		t.Fatal(err)
	}
	// EXACT equality, not HasSuffix: HasSuffix("aa-stale.drbundle") is true of the
	// bare name as well, so it is the one comparison that cannot see this defect.
	if len(decoded.Retention.Local) != 1 || decoded.Retention.Local[0] != jsonStale {
		t.Fatalf("retention.local = %#v, want exactly [%q] — the same full-path shape the "+
			"--retain-days branch reports for this field", decoded.Retention.Local, jsonStale)
	}
	if len(decoded.Retention.Offsite) != 1 || decoded.Retention.Offsite[0] != "aa-old.drbundle" {
		t.Fatalf("retention.offsite = %#v, want exactly [aa-old.drbundle] — an object name, "+
			"which is what dr ls --offsite lists and dr pull --name takes", decoded.Retention.Offsite)
	}
	if len(decoded.Retention.Skipped) != 0 {
		t.Fatalf("both tiers ran; skipped must be empty, got %#v", decoded.Retention.Skipped)
	}
	if _, statErr := os.Stat(jsonStale); !os.IsNotExist(statErr) {
		t.Fatalf("the document claims a prune that did not happen: stat err = %v", statErr)
	}
	jsonMock.mu.Lock()
	_, stillThere := jsonMock.objects["aa-old.drbundle"]
	jsonMock.mu.Unlock()
	if stillThere {
		t.Fatal("the document claims an offsite prune that did not happen")
	}
	if !strings.Contains(errOut, "GFS pruned local bundle: aa-stale.drbundle") {
		t.Fatalf("the printed line keeps the bare name; stderr = %q", errOut)
	}
}

// TestDRBackupSaysWhenARetentionTierNeverRan is the contrafactual in the direction
// that is almost never written: not "it deleted these", but "it could not run".
//
// An empty tier meant two different things before `skipped` existed — "there was
// nothing to delete" and "the policy you asked for never ran" — and a scheduled job
// that checks retention is keeping a volume from filling up read the first off the
// second. The human pane has always said so in a warning line; this proves the
// document does too, and that the warning did not land on stdout on the way.
func TestDRBackupSaysWhenARetentionTierNeverRan(t *testing.T) {
	src := t.TempDir()
	seedDataDir(t, src)
	pf := l3Passphrase(t)
	bundle := filepath.Join(t.TempDir(), "kept.drbundle")
	mock, flags := l3OffsiteThatCannotList(t)

	out, errOut, err := execRoot(t, append([]string{"dr", "backup", "--data-dir", src,
		"--out", bundle, "--passphrase-file", pf, "--gfs-keep-last", "1", "-o", "json"}, flags...)...)
	if err != nil {
		t.Fatalf("a prune that cannot run must not fail the backup: %v\n%s", err, errOut)
	}
	doc := mustJSONObject(t, "dr backup offsite-unlistable", out)
	assertRetentionKeys(t, "dr backup offsite-unlistable", doc)
	var decoded drBackupResult
	if err := json.Unmarshal([]byte(out), &decoded); err != nil {
		t.Fatal(err)
	}
	// The push SUCCEEDED — this is a tier that could not be pruned, not a bundle
	// that never left the box. Both halves matter: if the push had failed the whole
	// command would have failed and there would be no document to read.
	if decoded.Offsite == nil {
		t.Fatal("the push succeeded, so offsite must not be null")
	}
	mock.mu.Lock()
	_, replicated := mock.objects["kept.drbundle"]
	mock.mu.Unlock()
	if !replicated {
		t.Fatal("offsite is reported non-null but the object is not on the mirror")
	}
	if len(decoded.Retention.Skipped) != 1 || decoded.Retention.Skipped[0] != "offsite" {
		t.Fatalf("retention.skipped = %#v, want exactly [offsite]: the mirror could not be "+
			"listed, so that tier did not run — which an empty `offsite` list cannot say",
			decoded.Retention.Skipped)
	}
	if len(decoded.Retention.Offsite) != 0 {
		t.Fatalf("a tier that never ran deleted nothing: %#v", decoded.Retention.Offsite)
	}
	// The LOCAL tier did run, and reports itself as ran-and-deleted-nothing: the
	// distinction the two fields together make.
	if decoded.Retention.Local == nil || len(decoded.Retention.Local) != 0 {
		t.Fatalf("the local tier ran with no stale siblings: %#v", decoded.Retention.Local)
	}
	if !strings.Contains(errOut, "warning: offsite GFS prune skipped") {
		t.Fatalf("the human pane must still say why; stderr = %q", errOut)
	}
	if strings.Contains(out, "warning") {
		t.Fatalf("the warning must not share stdout with the document:\n%s", out)
	}
}

// TestRetentionHelpersReportASkippedTierAtTheGuardThatSkipsIt calibrates the two
// guards the test above rests on, one tier at a time and without a backup in the
// way. Each case asserts the WARNING TEXT, so it is provable which guard fired: an
// absent directory and a malformed glob pattern are different failures, and a test
// that only checked Skipped would pass on either.
func TestRetentionHelpersReportASkippedTierAtTheGuardThatSkipsIt(t *testing.T) {
	now := time.Now()

	// applyGFSLocal: the listing fails. It has to be a MALFORMED PATTERN and not an
	// absent directory, and finding that out is the reason this case exists as its
	// own test: localBundleMetas lists with filepath.Glob, and Glob on a directory
	// that does not exist returns no matches and NO error. An absent dir therefore
	// reports "ran, deleted nothing" — this test asserted Skipped on it first, and
	// failed, which is the only way that difference gets written down.
	var buf bytes.Buffer
	got := applyGFSLocal(filepath.Join(t.TempDir(), "["), "keep.drbundle",
		dr.GFSPolicy{KeepLast: 1}, now, &buf)
	if !got.Skipped || len(got.Deleted) != 0 {
		t.Fatalf("applyGFSLocal on a malformed pattern = %#v, want Skipped with nothing deleted", got)
	}
	if buf.String() != "warning: local GFS prune skipped (syntax error in pattern)\n" {
		t.Fatalf("applyGFSLocal printed %q, want the glob guard's exact line", buf.String())
	}

	// The other half of that discovery, pinned so it cannot be mistaken for the case
	// above: an ABSENT directory is not a skip, because Glob does not report it.
	buf.Reset()
	got = applyGFSLocal(filepath.Join(t.TempDir(), "not-a-dir"), "keep.drbundle",
		dr.GFSPolicy{KeepLast: 1}, now, &buf)
	if got.Skipped || len(got.Deleted) != 0 || buf.Len() != 0 {
		t.Fatalf("applyGFSLocal on an absent dir = %#v printing %q; Glob reports no error for it, "+
			"so this tier answers ran-and-deleted-nothing (see retentionOutcome)", got, buf.String())
	}

	// pruneOldBundles: the same guard, reached from the other retention flag.
	buf.Reset()
	got = pruneOldBundles(filepath.Join(t.TempDir(), "["), "keep.drbundle", 7, now, &buf)
	if !got.Skipped || len(got.Deleted) != 0 {
		t.Fatalf("pruneOldBundles on a malformed pattern = %#v, want Skipped with nothing deleted", got)
	}
	if buf.String() != "warning: bundle prune skipped (syntax error in pattern)\n" {
		t.Fatalf("pruneOldBundles printed %q, want the glob guard's exact line", buf.String())
	}

	// AND the other direction: retainDays <= 0 is NOT a skip. Nobody asked for
	// retention on this tier, and reporting it as skipped would alarm a monitor on
	// every backup taken without --retain-days.
	buf.Reset()
	got = pruneOldBundles(t.TempDir(), "keep.drbundle", 0, now, &buf)
	if got.Skipped || len(got.Deleted) != 0 || buf.Len() != 0 {
		t.Fatalf("pruneOldBundles with retain-days 0 = %#v and printed %q, want a silent no-op", got, buf.String())
	}
}

// ------------------------------------------------------------ dr push / pull

func TestDRPushAndPullTextUnchangedAndJSONNamesTheOffsiteLocation(t *testing.T) {
	src := t.TempDir()
	seedDataDir(t, src)
	pf := l3Passphrase(t)
	bundle := filepath.Join(t.TempDir(), "estate.drbundle")
	if _, errOut, err := execRoot(t, "dr", "backup", "--data-dir", src, "--out", bundle, "--passphrase-file", pf); err != nil {
		t.Fatalf("seed backup: %v\n%s", err, errOut)
	}
	_, _, flags := newMockOffsite(t)

	out, errOut, err := execRoot(t, append([]string{"dr", "push", "--in", bundle}, flags...)...)
	if err != nil {
		t.Fatalf("dr push: %v\n%s", err, errOut)
	}
	assertExactStdout(t, "dr push", out, "pushed offsite: estate.drbundle → dr-bucket/\n")

	out, errOut, err = execRoot(t, append([]string{"dr", "push", "--in", bundle, "-o", "json"}, flags...)...)
	if err != nil {
		t.Fatalf("dr push -o json: %v\n%s", err, errOut)
	}
	assertJSONKeys(t, "dr push", mustJSONObject(t, "dr push", out), "in", "offsite")
	var push drPushResult
	if err := json.Unmarshal([]byte(out), &push); err != nil {
		t.Fatal(err)
	}
	// The text form joins bucket and prefix with a slash, so recovering the bucket
	// from it means splitting on a character a prefix may contain. These are fields.
	if push.In != bundle || push.Offsite.Object != "estate.drbundle" || push.Offsite.Bucket != "dr-bucket" || push.Offsite.Prefix != "" {
		t.Fatalf("dr push json = %#v", push)
	}

	pulled := filepath.Join(t.TempDir(), "pulled.drbundle")
	out, errOut, err = execRoot(t, append([]string{"dr", "pull", "--name", "estate.drbundle", "--out", pulled}, flags...)...)
	if err != nil {
		t.Fatalf("dr pull: %v\n%s", err, errOut)
	}
	assertExactStdout(t, "dr pull", out, "pulled offsite bundle estate.drbundle → "+pulled+"\n")

	pulled2 := filepath.Join(t.TempDir(), "pulled2.drbundle")
	out, errOut, err = execRoot(t, append([]string{"dr", "pull", "--name", "estate.drbundle", "--out", pulled2, "-o", "json"}, flags...)...)
	if err != nil {
		t.Fatalf("dr pull -o json: %v\n%s", err, errOut)
	}
	assertJSONKeys(t, "dr pull", mustJSONObject(t, "dr pull", out), "offsite", "out", "bytes")
	var pull drPullResult
	if err := json.Unmarshal([]byte(out), &pull); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(pulled2)
	if err != nil {
		t.Fatal(err)
	}
	// bytes is the fact the text form could not have printed: the copy's count was
	// discarded. A short pull is a restore that fails at the worst moment.
	if pull.Bytes != info.Size() || pull.Bytes == 0 {
		t.Fatalf("bytes = %d, the written file is %d", pull.Bytes, info.Size())
	}
	if pull.Out != pulled2 || pull.Offsite.Object != "estate.drbundle" || pull.Offsite.Bucket != "dr-bucket" {
		t.Fatalf("dr pull json = %#v", pull)
	}
}
