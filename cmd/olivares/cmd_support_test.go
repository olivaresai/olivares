// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/dr"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/secret"
)

const supportSeededSecret = "SEEDEDSECRETsupportbundleXYZ"

const supportSeededPrivateKey = "-----BEGIN PRIVATE KEY-----\n" + supportSeededSecret + "\n-----END PRIVATE KEY-----"

func TestSupportBundleSeededSecretNeverLeaks(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "olivares.env")
	logsPath := filepath.Join(dir, "engine.log")
	verifyPath := filepath.Join(dir, "audit-verify.json")
	drPath := filepath.Join(dir, "recovery.drbundle")
	outPath := filepath.Join(dir, "support.tar.gz")
	dataDir := filepath.Join(dir, "data")
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		t.Fatal(err)
	}

	writeSupportTestFile(t, configPath, "DATABASE_PASSWORD="+supportSeededSecret+
		"\nSIGNING_KEY="+supportSeededSecret+"\nPRIVATE_KEY="+supportSeededSecret+
		"\nexport\tSIGNING_KEY="+supportSeededSecret+
		"\nOLIVARES_VECTOR_DSN=db://svc:"+supportSeededSecret+"@host/db"+
		"\nPASSWORD=store: "+supportSeededSecret+
		"\nADMIN=env:FOO sk-ant-api03-"+supportSeededSecret+
		"\nKMS_MASTER_KEY="+supportSeededSecret+
		"\nROOT_KEY="+supportSeededSecret+
		"\nOLIVARES_WEBHOOK_HMAC="+supportSeededSecret+
		"\nSESSION_COOKIE="+supportSeededSecret+
		"\nHMAC_SIGNING_MATERIAL="+supportSeededSecret+
		"\nSHARED_SALT="+supportSeededSecret+
		"\nLOG_LEVEL=info\nOLIVARES_HTTP_PORT=8443"+
		"\nOLIVARES_SERVER_URL=db://:"+supportSeededSecret+"@host"+
		"\nAPI_KEY=store:foo\nDB=store:mykey\n")
	writeSupportTestFile(t, logsPath, "level=error token="+supportSeededSecret+
		" cookie="+supportSeededSecret+" hmac="+supportSeededSecret+"\n"+supportSeededPrivateKey+"\n")
	writeSupportTestFile(t, verifyPath, `{"ok":false,"token":"`+supportSeededSecret+`","pem":"`+
		supportSeededPrivateKey+`"}`+"\n")
	writeSupportTestDRBundle(t, drPath, "token="+supportSeededSecret)
	// These are deliberately present in the selected data-dir. The support writer
	// has no path that can copy them into its closed archive allowlist.
	writeSupportTestFile(t, filepath.Join(dataDir, "secret-store.key"), supportSeededSecret)
	writeSupportTestFile(t, filepath.Join(dataDir, "audit-signing.key"), supportSeededSecret)
	writeSupportTestFile(t, filepath.Join(dataDir, "server-tls.key"), supportSeededSecret)

	statusServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/status" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		statusPEM := strings.ReplaceAll(supportSeededPrivateKey, "\n", `\n`)
		_, _ = io.WriteString(w, `{"status":"ok","components":[],"timestamp":"2026-07-15T00:00:00Z","token":"`+
			supportSeededSecret+`","pem":"`+statusPEM+`"}`)
	}))
	defer statusServer.Close()

	cmd := supportBundleCmd()
	var stdout strings.Builder
	cmd.SetOut(&stdout)
	cmd.SetErr(&stdout)
	cmd.SetArgs([]string{
		"--out", outPath,
		"--data-dir", dataDir,
		"--server", statusServer.URL,
		"--config", configPath,
		"--logs", logsPath,
		"--verify-report", verifyPath,
		"--dr-bundle", drPath,
		"--include", "config,status,logs,manifests,verify",
	})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("support bundle: %v\n%s", err, stdout.String())
	}

	info, err := os.Stat(outPath)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Fatalf("bundle mode = %o, want 600", got)
	}

	entries := readSupportTestBundle(t, outPath)
	for name, contents := range entries {
		if strings.Contains(string(contents), supportSeededSecret) {
			t.Errorf("seeded secret leaked in %s", name)
		}
		if strings.HasSuffix(strings.ToLower(name), ".key") {
			t.Errorf("key file entered the support bundle: %s", name)
		}
	}

	config := string(entries["config/effective.txt"])
	if !strings.Contains(config, "DATABASE_PASSWORD=[REDACTED]") {
		t.Fatalf("config lacks structural redaction marker:\n%s", config)
	}
	if !strings.Contains(config, "SIGNING_KEY=[REDACTED]") {
		t.Fatalf("config leaked or omitted SIGNING_KEY structural redaction:\n%s", config)
	}
	if !strings.Contains(config, "PRIVATE_KEY=[REDACTED]") {
		t.Fatalf("config leaked or omitted PRIVATE_KEY structural redaction:\n%s", config)
	}
	if !strings.Contains(config, "export\tSIGNING_KEY=[REDACTED]") {
		t.Fatalf("config leaked or omitted tab-export SIGNING_KEY structural redaction:\n%s", config)
	}
	if !strings.Contains(config, "OLIVARES_VECTOR_DSN=[REDACTED]") {
		t.Fatalf("config leaked or omitted inline db credential structural redaction:\n%s", config)
	}
	if !strings.Contains(config, "PASSWORD=[REDACTED]") {
		t.Fatalf("config treated a non-canonical store reference as safe:\n%s", config)
	}
	if !strings.Contains(config, "ADMIN=[REDACTED]") {
		t.Fatalf("config treated a non-canonical env reference as safe:\n%s", config)
	}
	if !strings.Contains(config, "KMS_MASTER_KEY=[REDACTED]") ||
		!strings.Contains(config, "ROOT_KEY=[REDACTED]") {
		t.Fatalf("config leaked or omitted generic key material redaction:\n%s", config)
	}
	for _, key := range []string{
		"OLIVARES_WEBHOOK_HMAC", "SESSION_COOKIE", "HMAC_SIGNING_MATERIAL", "SHARED_SALT",
		"OLIVARES_SERVER_URL",
	} {
		if !strings.Contains(config, key+"=[REDACTED]") {
			t.Errorf("config lacks fail-closed redaction for %s:\n%s", key, config)
		}
	}
	for _, public := range []string{"LOG_LEVEL=info", "OLIVARES_HTTP_PORT=8443"} {
		if !strings.Contains(config, public) {
			t.Errorf("config changed public setting %q:\n%s", public, config)
		}
	}
	if !strings.Contains(config, "API_KEY=store:foo") {
		t.Fatalf("store reference was changed or resolved:\n%s", config)
	}
	if !strings.Contains(config, "DB=store:mykey") {
		t.Fatalf("canonical store reference was changed or resolved:\n%s", config)
	}
	for _, name := range []string{
		"status/status.json", "logs/engine.log", "verify/report-001.json", "manifests/dr-001.json",
	} {
		if got := string(entries[name]); !strings.Contains(got, "[redacted") {
			t.Errorf("%s lacks canonical redaction marker: %s", name, got)
		}
	}

	manifestBytes, ok := entries["manifest.json"]
	if !ok {
		t.Fatal("manifest.json is absent")
	}
	var manifest supportBundleManifest
	if err := json.Unmarshal(manifestBytes, &manifest); err != nil {
		t.Fatalf("decode manifest: %v", err)
	}
	redactions := make(map[string]int, len(manifest.Sections))
	for _, section := range manifest.Sections {
		redactions[section.Path] = section.Redactions
	}
	if redactions["config/effective.txt"] == 0 {
		t.Error("manifest records no config redactions")
	}
	if redactions["logs/engine.log"] == 0 {
		t.Error("manifest records no log redactions")
	}
	if manifest.Redaction.TotalRedactions < 10 || manifest.Redaction.SectionsScrubbed < 5 {
		t.Errorf("redaction summary does not cover every seeded channel: %+v", manifest.Redaction)
	}
	manifestSum := sha256.Sum256(manifestBytes)
	if digest := hex.EncodeToString(manifestSum[:]); !strings.Contains(stdout.String(), "manifest.json sha256:"+digest) {
		t.Errorf("stdout does not emit the stored manifest digest: %s", stdout.String())
	}
}

func TestSupportPublicConfigAllowlistExcludesCredentialKeys(t *testing.T) {
	for key := range supportPublicConfigKeys {
		if secret.IsCredentialBearingConfigKey(key) {
			t.Errorf("public support-config allowlist contains credential-bearing key %q", key)
		}
	}
}

func TestRedactEffectiveConfigIsDenyByDefault(t *testing.T) {
	in := "OLIVARES_WEBHOOK_HMAC=" + supportSeededSecret +
		"\nSESSION_COOKIE=" + supportSeededSecret +
		"\nHMAC_SIGNING_MATERIAL=" + supportSeededSecret +
		"\nSHARED_SALT=" + supportSeededSecret +
		"\nLOG_LEVEL=info" +
		"\nOLIVARES_HTTP_PORT=8443" +
		"\nOLIVARES_SERVER_URL=db://:" + supportSeededSecret + "@host" +
		"\nAKIA0000000000000000=opaque-value" +
		"\nAPI_KEY=store:foo\n"

	out, redactions := redactEffectiveConfig(in)
	if strings.Contains(out, supportSeededSecret) {
		t.Fatalf("effective config leaked an unknown or inline secret:\n%s", out)
	}
	for _, key := range []string{
		"OLIVARES_WEBHOOK_HMAC", "SESSION_COOKIE", "HMAC_SIGNING_MATERIAL", "SHARED_SALT",
		"OLIVARES_SERVER_URL", "AKIA0000000000000000",
	} {
		if !strings.Contains(out, key+"=[REDACTED]") {
			t.Errorf("effective config lacks fail-closed redaction for %s:\n%s", key, out)
		}
	}
	for _, public := range []string{"LOG_LEVEL=info", "OLIVARES_HTTP_PORT=8443", "API_KEY=store:foo"} {
		if !strings.Contains(out, public) {
			t.Errorf("effective config changed allowed value %q:\n%s", public, out)
		}
	}
	if redactions < 6 {
		t.Fatalf("effective config redactions = %d, want at least 6", redactions)
	}
}

func TestSupportBundleRedactsNonOKStatusBody(t *testing.T) {
	statusServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = io.WriteString(w, "token="+supportSeededSecret)
	}))
	defer statusServer.Close()

	outPath := filepath.Join(t.TempDir(), "status-error.tar.gz")
	cmd := supportBundleCmd()
	var output strings.Builder
	cmd.SetOut(&output)
	cmd.SetErr(&output)
	cmd.SetArgs([]string{"--include", "status", "--server", statusServer.URL, "--out", outPath})
	err := cmd.Execute()
	if err == nil {
		t.Fatal("support bundle accepted a non-OK status response")
	}
	if strings.Contains(err.Error(), supportSeededSecret) || strings.Contains(output.String(), supportSeededSecret) {
		t.Fatalf("status error leaked the response body credential: err=%q output=%q", err, output.String())
	}
	if !strings.Contains(err.Error(), "collect status: HTTP 503: token=[redacted") {
		t.Fatalf("status error = %q, want redacted HTTP 503 body", err)
	}
	if _, statErr := os.Stat(outPath); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("failed status collection left an output bundle: %v", statErr)
	}
}

func TestSupportBundleRefusesKeyMaterialInput(t *testing.T) {
	dir := t.TempDir()
	keyPath := filepath.Join(dir, "audit-signing.key")
	outPath := filepath.Join(dir, "refused.tar.gz")
	writeSupportTestFile(t, keyPath, strings.Repeat("QUJD", 22)+"\n")

	cmd := supportBundleCmd()
	cmd.SilenceErrors = true
	cmd.SilenceUsage = true
	cmd.SetArgs([]string{
		"--offline", "--include", "verify", "--verify-report", keyPath, "--out", outPath,
	})
	err := cmd.Execute()
	if err == nil {
		t.Fatal("support bundle accepted a key file as a verify report")
	}
	want := "support bundle: refusing to read " + keyPath + ": key material or data-dir content is never ingested"
	if !strings.Contains(err.Error(), want) {
		t.Fatalf("key-material error = %q, want %q", err, want)
	}
	if _, statErr := os.Stat(outPath); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("refused key input left an output bundle: %v", statErr)
	}
}

func TestRedactEffectiveConfigRemovesMultiLinePrivateKey(t *testing.T) {
	in := "TLS_KEY=" + supportSeededPrivateKey + "\nAPI_KEY=store:foo\n"
	out, redactions := redactEffectiveConfig(in)
	if strings.Contains(out, supportSeededSecret) || strings.Contains(out, "PRIVATE KEY-----") {
		t.Fatalf("effective config leaked PEM content:\n%s", out)
	}
	if !strings.Contains(out, "TLS_KEY=[REDACTED]") {
		t.Fatalf("effective config lacks structural PEM redaction:\n%s", out)
	}
	if !strings.Contains(out, "API_KEY=store:foo") {
		t.Fatalf("effective config changed an exact secret reference:\n%s", out)
	}
	if redactions == 0 {
		t.Fatal("effective config recorded no PEM redaction")
	}
}

func TestRedactEffectiveConfigNormalizesExportWhitespace(t *testing.T) {
	in := "export\tSIGNING_KEY=x\nexport  PRIVATE_KEY=x\nexport\tOLIVARES_SECRET_STORE_KEY=x\n"
	out, redactions := redactEffectiveConfig(in)
	for _, want := range []string{
		"export\tSIGNING_KEY=[REDACTED]",
		"export  PRIVATE_KEY=[REDACTED]",
		"export\tOLIVARES_SECRET_STORE_KEY=[REDACTED]",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("effective config lacks %q:\n%s", want, out)
		}
	}
	if redactions != 3 {
		t.Fatalf("effective config redactions = %d, want 3", redactions)
	}
}

func TestInlineDBCredentialIsRedactedWithoutBlindingGuard(t *testing.T) {
	raw := "OLIVARES_VECTOR_DSN=db://svc:" + supportSeededSecret + "@host/db\n"
	guarded := supportBundleGuardText(supportBundleEntry{
		path: "config/effective.txt",
		data: []byte(raw),
	})
	if guarded != raw {
		t.Fatalf("support-bundle guard treated inline db credentials as a safe reference: %q", guarded)
	}

	out, redactions := redactEffectiveConfig(raw)
	if strings.Contains(out, supportSeededSecret) || out != "OLIVARES_VECTOR_DSN=[REDACTED]\n" {
		t.Fatalf("effective config did not redact inline db credentials:\n%s", out)
	}
	if redactions != 1 {
		t.Fatalf("effective config redactions = %d, want 1", redactions)
	}
}

func TestSupportBundleExactSecretReferenceGrammar(t *testing.T) {
	for _, value := range []string{
		"store:mykey", "db:mykey", "env:FOO_1", "file:/run/secrets/key", "'store:mykey'", `"env:FOO"`,
	} {
		if !isExactSecretReference(value) {
			t.Errorf("isExactSecretReference(%q) = false, want true", value)
		}
	}
	for _, value := range []string{
		"store: secret", "store:secret value", " store:secret", "store:secret ",
		"env:9FOO", "env:FOO-BAR", "env:FOO secret", "file:/run/secrets/private key",
	} {
		if isExactSecretReference(value) {
			t.Errorf("isExactSecretReference(%q) = true, want false", value)
		}
	}

	raw := "PASSWORD=store: " + supportSeededSecret +
		"\nADMIN=env:FOO sk-ant-api03-" + supportSeededSecret +
		"\nDB=store:mykey\n"
	guarded := supportBundleGuardText(supportBundleEntry{path: "config/effective.txt", data: []byte(raw)})
	if !strings.Contains(guarded, "PASSWORD=store: "+supportSeededSecret) ||
		!strings.Contains(guarded, "ADMIN=env:FOO sk-ant-api03-"+supportSeededSecret) {
		t.Fatalf("support-bundle guard blinded a non-canonical reference: %q", guarded)
	}
	if !strings.Contains(guarded, "DB=x") {
		t.Fatalf("support-bundle guard did not blind a canonical reference: %q", guarded)
	}
}

// THE FIXTURE IS BUILT BY THE ENGINE'S OWN BOOT, and that is the whole point
// (2026-08-05). It used to be seeded with `coreengine.Open(..., nil)` — the exact
// call the collector made — so the test and the product shared one defect and
// agreed with each other. It passed for as long as it existed while `support
// bundle` failed on EVERY install a real engine had created: opening with a nil
// schema-registration callback yields a different guard-unit manifest, hence a
// different rollout_id, and the append-only receipt reconciliation refuses.
//
// A control contaminated by the same defect as the experiment proves nothing. The
// fixture now comes from boot(), which is what `serve` runs.
func TestSupportBundleSecretInventoryListsMetadataWithoutOpeningValue(t *testing.T) {
	dataDir := t.TempDir()
	eng, err := boot(context.Background(), bootConfig{
		DataDir: dataDir, Engine: "sqlite", Version: "test", Logger: slog.Default(),
	})
	if err != nil {
		t.Fatal(err)
	}
	secretStore := auth.NewSecretStore(eng.store, supportInventoryTestSealer{})
	_, err = secretStore.Put(context.Background(), mustTestOperator("support-test"), auth.GlobalSecretScope, "support/api-token", supportSeededSecret, "support test credential 4111111111111111")
	if err != nil {
		_ = eng.Close()
		t.Fatal(err)
	}
	if err := eng.Close(); err != nil {
		t.Fatal(err)
	}

	outPath := filepath.Join(t.TempDir(), "inventory.tar.gz")
	cmd := supportBundleCmd()
	cmd.SetArgs([]string{"--offline", "--include", "secrets", "--data-dir", dataDir, "--out", outPath})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	entries := readSupportTestBundle(t, outPath)
	inventory := string(entries["secrets/inventory.txt"])
	if !strings.Contains(inventory, "support/api-token") || !strings.Contains(inventory, "support test credential") {
		t.Fatalf("inventory lacks non-secret metadata:\n%s", inventory)
	}
	if strings.Contains(inventory, supportSeededSecret) {
		t.Fatalf("inventory opened and leaked the stored value:\n%s", inventory)
	}
	for name, contents := range entries {
		if strings.Contains(string(contents), "4111111111111111") {
			t.Fatalf("inventory credit card leaked in %s", name)
		}
	}
	if !strings.Contains(inventory, "[redacted:credit-card]") {
		t.Fatalf("inventory lacks credit-card redaction marker:\n%s", inventory)
	}
	var manifest supportBundleManifest
	if err := json.Unmarshal(entries["manifest.json"], &manifest); err != nil {
		t.Fatal(err)
	}
	foundRedaction := false
	for _, section := range manifest.Sections {
		if section.Path == "secrets/inventory.txt" && section.Redactions > 0 {
			foundRedaction = true
		}
	}
	if !foundRedaction {
		t.Fatal("manifest records no secret-inventory redactions")
	}
}

type supportInventoryTestSealer struct{}

func (supportInventoryTestSealer) Seal(_ context.Context, _ model.TenantID, plaintext []byte) (string, error) {
	sum := sha256.Sum256(plaintext)
	return "sealed-test:" + hex.EncodeToString(sum[:]), nil
}

func (supportInventoryTestSealer) Open(context.Context, model.TenantID, string) ([]byte, error) {
	return nil, errors.New("support inventory must not open secret values")
}

func TestSupportBundleWriterIsDeterministicAndRejectsNonAllowlistedPaths(t *testing.T) {
	assembler := newSupportBundleAssembler()
	if err := assembler.add("logs/engine.log", "test", []byte("ordinary log\n"), 0); err != nil {
		t.Fatal(err)
	}
	if err := assembler.add("secret-store.key", "forbidden", []byte("secret"), 0); err == nil {
		t.Fatal("assembler accepted a key path outside the allowlist")
	}
	if err := assembler.add("../status/status.json", "forbidden", []byte("{}"), 0); err == nil {
		t.Fatal("assembler accepted a traversal path")
	}

	createdAt := time.Date(2026, 7, 15, 1, 2, 3, 0, time.UTC)
	a := filepath.Join(t.TempDir(), "a.tar.gz")
	b := filepath.Join(t.TempDir(), "b.tar.gz")
	if _, err := writeSupportBundle(a, "test", createdAt, assembler); err != nil {
		t.Fatal(err)
	}
	if _, err := writeSupportBundle(b, "test", createdAt, assembler); err != nil {
		t.Fatal(err)
	}
	ab, err := os.ReadFile(a)
	if err != nil {
		t.Fatal(err)
	}
	bb, err := os.ReadFile(b)
	if err != nil {
		t.Fatal(err)
	}
	if string(ab) != string(bb) {
		t.Fatal("same support-bundle inputs produced different tar.gz bytes")
	}
}

func TestSupportBundleWriterRefusesUnredactedContent(t *testing.T) {
	for name, content := range map[string]string{
		"key-value":   "token=" + supportSeededSecret + "\n",
		"private-key": supportSeededPrivateKey + "\n",
		"credit-card": "4111111111111111\n",
	} {
		t.Run(name, func(t *testing.T) {
			assembler := newSupportBundleAssembler()
			if err := assembler.add("logs/engine.log", "test", []byte(content), 0); err != nil {
				t.Fatal(err)
			}
			outPath := filepath.Join(t.TempDir(), "refused.tar.gz")
			_, err := writeSupportBundle(outPath, "test", time.Unix(0, 0), assembler)
			if err == nil {
				t.Fatal("support-bundle writer accepted unredacted content")
			}
			want := "support bundle: refusing to emit logs/engine.log: unredacted secret/PII detected"
			if !strings.Contains(err.Error(), want) {
				t.Fatalf("writer error = %q, want %q", err, want)
			}
			if _, statErr := os.Stat(outPath); !errors.Is(statErr, os.ErrNotExist) {
				t.Fatalf("refused bundle left an output file: %v", statErr)
			}
		})
	}
}

// TestSupportBundleFailClosedConfigDoesNotLeakUnderPublicKeys pins the round-5
// finding: the public-key allowlist gates STRUCTURE, not content, so a
// catalog-recognized secret or a URL userinfo credential parked under an
// allowlisted public key must still be redacted, while a genuinely public value
// is shown.
func TestSupportBundleFailClosedConfigDoesNotLeakUnderPublicKeys(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "olivares.env")
	outPath := filepath.Join(dir, "support.tar.gz")
	writeSupportTestFile(t, configPath, strings.Join([]string{
		"OLIVARES_MODEL_ID=AKIAIOSFODNN7EXAMPLE",                                          // aws-access-key shape
		"OLIVARES_BASE_URL=https://gw.example.com/ingest?key=AKIAIOSFODNN7EXAMPLE",        // shape in a URL
		"OLIVARES_SERVER_URL=https://" + supportSeededSecret + "@registry.example.com/v2", // userinfo, no colon
		"OLIVARES_LOG_LEVEL=info",                                                         // genuinely public
	}, "\n")+"\n")

	cmd := supportBundleCmd()
	cmd.SilenceErrors = true
	cmd.SilenceUsage = true
	var out strings.Builder
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{"--offline", "--include", "config", "--config", configPath, "--out", outPath})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("support bundle: %v\n%s", err, out.String())
	}
	config := string(readSupportTestBundle(t, outPath)["config/effective.txt"])
	for _, leak := range []string{"AKIAIOSFODNN7EXAMPLE", supportSeededSecret} {
		if strings.Contains(config, leak) {
			t.Errorf("secret leaked under a public config key:\n%s", config)
		}
	}
	if !strings.Contains(config, "OLIVARES_LOG_LEVEL=info") {
		t.Errorf("a genuinely public value was over-redacted:\n%s", config)
	}
}

// TestSupportBundleRefusesDataDirBlobInput pins the round-5 finding: a data-dir
// file (the SQLite DB and its copies) can never be ingested via --logs/--config/
// --verify-report, so "arbitrary data-dir blobs can never enter" holds.
func TestSupportBundleRefusesDataDirBlobInput(t *testing.T) {
	dir := t.TempDir()
	dataDir := filepath.Join(dir, "data")
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		t.Fatal(err)
	}
	dbPath := filepath.Join(dataDir, "olivares.db")
	writeSupportTestFile(t, dbPath, "SQLite format 3\x00"+supportSeededSecret)
	outPath := filepath.Join(dir, "refused.tar.gz")

	cmd := supportBundleCmd()
	cmd.SilenceErrors = true
	cmd.SilenceUsage = true
	cmd.SetArgs([]string{"--offline", "--include", "logs", "--data-dir", dataDir, "--logs", dbPath, "--out", outPath})
	err := cmd.Execute()
	if err == nil || !strings.Contains(err.Error(), "data-dir content is never ingested") {
		t.Fatalf("data-dir blob ingest = %v, want refusal", err)
	}
	if _, statErr := os.Stat(outPath); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("refused data-dir input left an output bundle")
	}
}

func writeSupportTestFile(t *testing.T, name, contents string) {
	t.Helper()
	if err := os.WriteFile(name, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
}

func writeSupportTestDRBundle(t *testing.T, name, notes string) {
	t.Helper()
	f, err := os.Create(name)
	if err != nil {
		t.Fatal(err)
	}
	writeErr := dr.WriteBundle(f, dr.BundleInput{
		Manifest: &dr.Manifest{
			Format: dr.ManifestFormat, CreatedAt: "2026-07-15T00:00:00Z",
			EngineKind: "postgres", Store: dr.StoreSnapshot{Method: dr.MethodPITR},
			TipMatch: dr.TipAdvisory, Notes: notes,
		},
	})
	closeErr := f.Close()
	if writeErr != nil {
		t.Fatalf("write DR bundle: %v", writeErr)
	}
	if closeErr != nil {
		t.Fatalf("close DR bundle: %v", closeErr)
	}
}

func readSupportTestBundle(t *testing.T, name string) map[string][]byte {
	t.Helper()
	f, err := os.Open(name)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	gz, err := gzip.NewReader(f)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = gz.Close() }()

	entries := map[string][]byte{}
	tr := tar.NewReader(gz)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		if hdr.Typeflag != tar.TypeReg {
			t.Fatalf("non-regular tar entry %s (type %d)", hdr.Name, hdr.Typeflag)
		}
		if hdr.Mode != 0o600 {
			t.Fatalf("tar entry %s mode = %o, want 600", hdr.Name, hdr.Mode)
		}
		b, err := io.ReadAll(tr)
		if err != nil {
			t.Fatal(err)
		}
		entries[hdr.Name] = b
	}
	return entries
}

// A diagnostic command must never MANUFACTURE the installation it was asked to
// read. Measured before this test existed: `support bundle --include secrets`
// against an empty --data-dir created and migrated a store, verified its own
// receipts against itself, exited 0 — and left a directory `serve` then refused
// to open, deterministically and forever, plus three freshly minted signing keys.
// A command that reads like a diagnostic bricked the install it was run against.
//
// Both halves matter and both are asserted: the refusal, and the ABSENCE of any
// file. An exit code alone would still pass if the store were created and then
// the command failed for some other reason.
func TestSupportBundleRefusesToCreateTheInstallationItReads(t *testing.T) {
	dataDir := t.TempDir()
	outPath := filepath.Join(t.TempDir(), "virgin.tar.gz")

	cmd := supportBundleCmd()
	cmd.SetArgs([]string{"--offline", "--include", "secrets", "--data-dir", dataDir, "--out", outPath})
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	err := cmd.Execute()
	if err == nil {
		t.Fatal("support bundle succeeded against a data dir with no store; it must refuse, not build one")
	}
	if !strings.Contains(err.Error(), "no store at") {
		t.Fatalf("error does not name the missing store, so an operator cannot act on it: %v", err)
	}

	left, readErr := os.ReadDir(dataDir)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if len(left) != 0 {
		names := make([]string, 0, len(left))
		for _, e := range left {
			names = append(names, e.Name())
		}
		t.Fatalf("a read-only diagnostic wrote into the data dir: %v", names)
	}
	if _, statErr := os.Stat(outPath); statErr == nil {
		t.Fatal("a bundle was written even though the section could not be collected")
	}
}

// TestSupportBundleTransportFailureNamesTheWayOut pins the guidance on the one
// failure the DEFAULT install walks into. `olivares quickstart` mints a
// self-signed certificate, so the first support bundle collected on a fresh box
// gets x509 "certificate signed by unknown authority" — measured 2026-08-09
// against a real engine: exit 6, zero sections collected, and a message
// that named neither --offline nor --insecure.
//
// The pairing with the HTTP-status test above is the point, and it is why this
// asserts a NEGATIVE too: an HTTP error is the server ANSWERING, where neither
// flag helps and offering --insecure would send an operator to skip TLS over a
// 503. Guidance that fires on both branches is worse than none.
func TestSupportBundleTransportFailureNamesTheWayOut(t *testing.T) {
	dir := t.TempDir()
	outPath := filepath.Join(dir, "transport.tar.gz")

	// A TLS server the client does not trust: the transport fails before any
	// HTTP status exists, which is exactly the self-signed first-boot shape.
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	}))
	t.Cleanup(srv.Close)

	cmd := supportBundleCmd()
	var output strings.Builder
	cmd.SetOut(&output)
	cmd.SetErr(&output)
	cmd.SetArgs([]string{"--include", "status", "--server", srv.URL, "--out", outPath})
	err := cmd.Execute()
	if err == nil {
		t.Fatal("support bundle accepted an untrusted TLS server")
	}
	for _, want := range []string{"--offline", "--insecure", "NOT collected"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("transport error must mention %q; err = %q", want, err)
		}
	}
	if _, statErr := os.Stat(outPath); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("failed status collection left an output bundle: %v", statErr)
	}
}

// TestSupportBundleHTTPErrorDoesNotOfferInsecure is the negative half. Without
// it, moving the guidance up one line — onto the branch that handles an HTTP
// status — would keep the test above green while telling an operator to disable
// certificate verification because the server returned 503.
func TestSupportBundleHTTPErrorDoesNotOfferInsecure(t *testing.T) {
	dir := t.TempDir()
	outPath := filepath.Join(dir, "http-error.tar.gz")

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte(`{"error":"unavailable"}`))
	}))
	t.Cleanup(srv.Close)

	cmd := supportBundleCmd()
	var output strings.Builder
	cmd.SetOut(&output)
	cmd.SetErr(&output)
	cmd.SetArgs([]string{"--include", "status", "--server", srv.URL, "--out", outPath})
	err := cmd.Execute()
	if err == nil {
		t.Fatal("support bundle accepted a 503 status response")
	}
	if strings.Contains(err.Error(), "--insecure") {
		t.Fatalf("a server that ANSWERED must not be met with advice to skip TLS; err = %q", err)
	}
}
