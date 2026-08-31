// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package api_test

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
	"github.com/olivaresai/olivares/core/supportbundle"
	"github.com/olivaresai/olivares/core/updatecheck"
	securitymodule "github.com/olivaresai/olivares/modules/security"
)

const wave2SeededSecret = "SEEDEDSECRETconsolewave2XYZ"

func TestEffectiveConfigHTTPRedactsSecretsAndReportsViolations(t *testing.T) {
	h := newHarnessOpts(t, func(o *api.Options) {
		o.EffectiveConfig = func() []api.EffectiveConfigEntry {
			return []api.EffectiveConfigEntry{
				{
					Key: "OLIVARES_CLAUDE_INFERENCE_KEY", Value: wave2SeededSecret,
					Redacted: true, Source: "env",
				},
				{
					Key: "OLIVARES_VECTOR_DSN", Value: "postgres://app:" + wave2SeededSecret + "@db/olivares",
					Source: "env",
				},
				{
					Key: "OLIVARES_VECTOR_BACKUP_DSN", Value: "file:/run/secrets/vector.dsn",
					Source: "activation",
				},
			}
		}
		o.EffectiveConfigViolations = func() []string {
			return []string{"OLIVARES_UNKNOWN_Z", "OLIVARES_UNKNOWN_A", "OLIVARES_UNKNOWN_Z"}
		}
	})
	admin := h.adminLogin()

	r := h.do(http.MethodGet, "/v1/console/config/effective", admin, nil, nil)
	if r.code != http.StatusOK {
		t.Fatalf("effective config = %d %s", r.code, r.raw)
	}
	if strings.Contains(r.raw, wave2SeededSecret) || strings.Contains(r.raw, "postgres://app:") {
		t.Fatalf("effective-config HTTP response disclosed a secret: %s", r.raw)
	}

	entries, ok := r.body["entries"].([]any)
	if !ok || len(entries) != 3 {
		t.Fatalf("entries = %#v, want three entries", r.body["entries"])
	}
	byKey := make(map[string]map[string]any, len(entries))
	for _, raw := range entries {
		entry := raw.(map[string]any)
		byKey[entry["key"].(string)] = entry
	}
	for _, key := range []string{"OLIVARES_CLAUDE_INFERENCE_KEY", "OLIVARES_VECTOR_DSN"} {
		entry := byKey[key]
		if entry["value"] != "<redacted>" || entry["redacted"] != true {
			t.Errorf("%s = %#v, want a structural redaction", key, entry)
		}
	}
	ref := byKey["OLIVARES_VECTOR_BACKUP_DSN"]
	if ref["value"] != "file:/run/secrets/vector.dsn" || ref["redacted"] != false ||
		ref["source"] != "activation" {
		t.Errorf("externalized DSN reference changed: %#v", ref)
	}
	violations := r.body["strict_violations"].([]any)
	if len(violations) != 2 || violations[0] != "OLIVARES_UNKNOWN_A" || violations[1] != "OLIVARES_UNKNOWN_Z" {
		t.Errorf("strict_violations = %#v, want sorted unique keys", violations)
	}
}

func TestEffectiveConfigNilSeamIsHonestEmptyResponse(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	r := h.do(http.MethodGet, "/v1/console/config/effective", admin, nil, nil)
	if r.code != http.StatusOK || r.raw != "{\"entries\":[],\"strict_violations\":[]}\n" {
		t.Fatalf("nil effective-config seam = %d %s", r.code, r.raw)
	}
}

func TestUpdateCheckNowUnconfiguredAndFreshStatus(t *testing.T) {
	t.Run("unconfigured", func(t *testing.T) {
		h := newHarness(t)
		admin := h.adminLogin()
		r := h.do(http.MethodPost, "/v1/console/update-check", admin, nil, nil)
		if r.code != http.StatusNotImplemented || r.body["error"] != "update checking not configured" {
			t.Fatalf("unconfigured update check = %d %s", r.code, r.raw)
		}
	})

	t.Run("refresh", func(t *testing.T) {
		calls := 0
		checkedAt := time.Date(2026, 7, 24, 12, 0, 0, 0, time.UTC)
		h := newHarnessOpts(t, func(o *api.Options) {
			o.UpdateRefresh = func(ctx context.Context) updatecheck.Status {
				if ctx == nil {
					t.Fatal("refresh received a nil context")
				}
				calls++
				return updatecheck.Status{
					Enabled: true, Available: true, Channel: "security",
					CurrentVersion: "26.7.0", LatestVersion: "26.7.1",
					Security: true, Advisories: []string{"CVE-2026-4242"}, CheckedAt: checkedAt,
				}
			}
		})
		admin := h.adminLogin()
		r := h.do(http.MethodPost, "/v1/console/update-check", admin, nil, nil)
		if r.code != http.StatusOK || calls != 1 {
			t.Fatalf("configured update check = %d calls=%d body=%s", r.code, calls, r.raw)
		}
		if r.body["latest_version"] != "26.7.1" || r.body["security"] != true {
			t.Errorf("fresh update status = %#v", r.body)
		}
		advisories := r.body["advisories"].([]any)
		if len(advisories) != 1 || advisories[0] != "CVE-2026-4242" {
			t.Errorf("fresh advisories = %#v", advisories)
		}
	})
}

func TestConsoleSupportBundleAAL3RedactionIntegrityAndAudit(t *testing.T) {
	broker := api.NewLogBroker(slog.NewTextHandler(io.Discard, nil), 16, nil)
	slog.New(broker).Error(
		"upstream failed token="+wave2SeededSecret,
		"password", wave2SeededSecret,
	)

	h := newHarnessOpts(t, func(o *api.Options) {
		o.LogBroker = broker
		o.EffectiveConfig = func() []api.EffectiveConfigEntry {
			return []api.EffectiveConfigEntry{{
				Key: "OLIVARES_CLAUDE_INFERENCE_KEY", Value: wave2SeededSecret,
				Redacted: true, Source: "env",
			}}
		}
		o.SupportBundleRedact = securitymodule.RedactText
		o.SupportBundleContainsSensitive = securitymodule.ContainsSecretOrPII
	})
	admin := h.adminLogin()

	refused := doWave2Binary(h, http.MethodPost, "/v1/console/support-bundle", admin)
	if refused.Code != http.StatusForbidden || !strings.Contains(refused.Body.String(), "step_up_required") {
		t.Fatalf("support bundle at AAL1 = %d %s", refused.Code, refused.Body.String())
	}

	h.elevate(admin)
	rec := doWave2Binary(h, http.MethodPost, "/v1/console/support-bundle", admin)
	if rec.Code != http.StatusOK {
		t.Fatalf("support bundle at AAL3 = %d %s", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("Content-Type"); got != "application/octet-stream" {
		t.Errorf("Content-Type = %q", got)
	}
	if !strings.Contains(rec.Header().Get("Content-Disposition"), "attachment;") {
		t.Errorf("Content-Disposition = %q", rec.Header().Get("Content-Disposition"))
	}
	if got, err := strconv.Atoi(rec.Header().Get("Content-Length")); err != nil || got != rec.Body.Len() {
		t.Errorf("Content-Length = %q, body=%d, err=%v", rec.Header().Get("Content-Length"), rec.Body.Len(), err)
	}

	entries := readWave2SupportBundle(t, rec.Body.Bytes())
	for name, content := range entries {
		if strings.Contains(string(content), wave2SeededSecret) {
			t.Errorf("seeded secret leaked in %s", name)
		}
	}
	if got := string(entries["config/effective.txt"]); !strings.Contains(got, "OLIVARES_CLAUDE_INFERENCE_KEY=<redacted>") {
		t.Errorf("effective config section = %q", got)
	}
	if got := string(entries["logs/engine.log"]); !strings.Contains(got, "[redacted") {
		t.Errorf("log section lacks canonical redaction: %q", got)
	}
	if got := string(entries["manifests/schema.json"]); !strings.Contains(got, "unavailable in the API layer") {
		t.Errorf("schema skip note = %q", got)
	}
	if got := string(entries["secrets/inventory.txt"]); !strings.Contains(got, "not configured") {
		t.Errorf("secret metadata skip note = %q", got)
	}

	var manifest supportbundle.Manifest
	manifestBytes := entries["manifest.json"]
	if err := json.Unmarshal(manifestBytes, &manifest); err != nil {
		t.Fatalf("decode manifest: %v", err)
	}
	if len(manifest.Sections) != 5 {
		t.Fatalf("manifest sections = %d, want 5", len(manifest.Sections))
	}
	for _, section := range manifest.Sections {
		content, ok := entries[section.Path]
		if !ok {
			t.Errorf("manifest section %q is absent", section.Path)
			continue
		}
		sum := sha256.Sum256(content)
		if section.Bytes != len(content) || section.SHA256 != hex.EncodeToString(sum[:]) {
			t.Errorf("manifest integrity mismatch for %s: %+v", section.Path, section)
		}
	}
	manifestSum := sha256.Sum256(manifestBytes)
	if got, want := rec.Header().Get("X-Olivares-Manifest-SHA256"), hex.EncodeToString(manifestSum[:]); got != want {
		t.Errorf("manifest digest header = %q, want %q", got, want)
	}

	var audited bool
	err := h.st.View(context.Background(), model.SystemTenantID, func(sc store.Scope) error {
		canonical, ok := sc.Audit().(store.CanonicalWalker)
		if !ok {
			t.Fatal("test store lacks canonical audit walker")
		}
		return canonical.WalkCanonical(context.Background(), 1, func(event model.AuditEvent, metaJSON string, _ []byte) error {
			if event.Action == "console.support_bundle" {
				audited = true
				var meta map[string]any
				if err := json.Unmarshal([]byte(metaJSON), &meta); err != nil {
					t.Errorf("decode support-bundle audit metadata: %v", err)
				} else if meta["bytes"] == nil || meta["sections"] == nil {
					t.Errorf("support-bundle audit metadata = %#v", meta)
				}
			}
			return nil
		})
	})
	if err != nil {
		t.Fatal(err)
	}
	if !audited {
		t.Fatal("system ledger lacks console.support_bundle")
	}
}

func TestConsoleSupportBundleWithoutLogBrokerCarriesSkipNote(t *testing.T) {
	h := newHarnessOpts(t, func(o *api.Options) {
		o.SupportBundleRedact = securitymodule.RedactText
		o.SupportBundleContainsSensitive = securitymodule.ContainsSecretOrPII
	})
	admin := h.adminLogin()
	h.elevate(admin)
	rec := doWave2Binary(h, http.MethodPost, "/v1/console/support-bundle", admin)
	if rec.Code != http.StatusOK {
		t.Fatalf("support bundle without LogBroker = %d %s", rec.Code, rec.Body.String())
	}
	entries := readWave2SupportBundle(t, rec.Body.Bytes())
	if got := string(entries["logs/engine.log"]); got != "skipped: API log broker not configured\n" {
		t.Errorf("log skip note = %q", got)
	}
}

func TestConsoleWave2OpenAPIIncludesUpdateAdvisories(t *testing.T) {
	doc := api.OpenAPIDocument()
	paths := doc["paths"].(map[string]any)
	for _, path := range []string{
		"/v1/console/config/effective",
		"/v1/console/support-bundle",
		"/v1/console/update-check",
	} {
		if _, ok := paths[path]; !ok {
			t.Errorf("OpenAPI missing %s", path)
		}
	}
	components := doc["components"].(map[string]any)
	schemas := components["schemas"].(map[string]any)
	update := schemas["UpdateStatus"].(map[string]any)
	properties := update["properties"].(map[string]any)
	advisories, ok := properties["advisories"].(map[string]any)
	if !ok || advisories["type"] != "array" {
		t.Fatalf("UpdateStatus.advisories = %#v, want string array", properties["advisories"])
	}
	items := advisories["items"].(map[string]any)
	if items["type"] != "string" {
		t.Errorf("UpdateStatus.advisories items = %#v", items)
	}
}

func doWave2Binary(h *harness, method, path, token string) *httptest.ResponseRecorder {
	h.t.Helper()
	req := httptest.NewRequest(method, path, nil)
	req.RemoteAddr = "10.0.0.1:1234"
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	h.srv.Handler().ServeHTTP(rec, req)
	return rec
}

func readWave2SupportBundle(t *testing.T, bundle []byte) map[string][]byte {
	t.Helper()
	gz, err := gzip.NewReader(bytes.NewReader(bundle))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = gz.Close() }()

	entries := make(map[string][]byte)
	tr := tar.NewReader(gz)
	for {
		header, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		if header.Typeflag != tar.TypeReg || header.Mode != 0o600 {
			t.Fatalf("unsafe archive entry %s type=%d mode=%o", header.Name, header.Typeflag, header.Mode)
		}
		content, err := io.ReadAll(tr)
		if err != nil {
			t.Fatal(err)
		}
		entries[header.Name] = content
	}
	return entries
}
