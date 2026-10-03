// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package api_test

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"reflect"
	"regexp"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/supportbundle"
)

// This synthetic value and its redactor exercise the API's injected redaction
// contract, not the production secret/PII detection catalog.
const supportBundlePrivacySecret = "SYNTHETICsupportBundlePrivacyValue20260929"

func TestConsoleSupportBundleSecretInventoryOmitsFingerprints(t *testing.T) {
	const name = "support-bundle/privacy-fixture"
	const marker = "[redacted:privacy-fixture]"
	broker := api.NewLogBroker(slog.NewTextHandler(io.Discard, nil), 16, nil)
	slog.New(broker).Error("synthetic support diagnostic", "detail", supportBundlePrivacySecret)
	h := newHarnessOpts(t, func(o *api.Options) {
		o.SecretStore = auth.NewSecretStore(o.Store, fakeSealer{})
		o.LogBroker = broker
		o.EffectiveConfig = func() []api.EffectiveConfigEntry {
			return []api.EffectiveConfigEntry{
				{Key: "OLIVARES_CLAUDE_INFERENCE_KEY", Value: supportBundlePrivacySecret, Source: "env"},
				{Key: "OLIVARES_LOG_LEVEL", Value: "info", Source: "env"},
			}
		}
		// Deliberately redact only the full value. Erasing its fingerprint here
		// would hide a regression in the portable inventory's field selection.
		o.SupportBundleRedact = func(s string) (string, int) {
			return strings.ReplaceAll(s, supportBundlePrivacySecret, marker), strings.Count(s, supportBundlePrivacySecret)
		}
		o.SupportBundleContainsSensitive = func(s string) bool {
			return strings.Contains(s, supportBundlePrivacySecret)
		}
	})
	admin := h.adminLogin()
	h.elevate(admin)
	put := h.do(http.MethodPut, "/v1/console/secrets", admin, map[string]any{
		"name": name, "value": supportBundlePrivacySecret,
		"description": "Synthetic integration " + supportBundlePrivacySecret,
	}, nil)
	if put.code != http.StatusOK {
		t.Fatalf("put synthetic secret = %d %s", put.code, put.raw)
	}
	sum := sha256.Sum256([]byte(supportBundlePrivacySecret))
	hint := hex.EncodeToString(sum[:])[:12]
	if put.body["hint"] != hint {
		t.Fatalf("live secret hint = %v, want computed fixture fingerprint %s", put.body["hint"], hint)
	}
	updated, ok := put.body["updated_at"].(string)
	if !ok || updated == "" {
		t.Fatalf("stored secret lacks updated_at: %v", put.body)
	}

	rec := doWave2Binary(h, http.MethodPost, "/v1/console/support-bundle", admin)
	if rec.Code != http.StatusOK {
		t.Fatalf("support bundle = %d %s", rec.Code, rec.Body.String())
	}
	entries := readWave2SupportBundle(t, rec.Body.Bytes())
	for path, content := range entries {
		for _, forbidden := range []string{supportBundlePrivacySecret, hint} {
			if strings.Contains(string(content), forbidden) {
				t.Errorf("support bundle entry %s disclosed the synthetic value or its fingerprint", path)
			}
		}
	}
	lines := strings.Split(strings.TrimSpace(string(entries["secrets/inventory.txt"])), "\n")
	if len(lines) != 2 {
		t.Fatalf("secret inventory = %q, want header and one row", lines)
	}
	columns := regexp.MustCompile(` {2,}`)
	want := [][]string{
		{"NAME", "DESCRIPTION", "UPDATED"},
		{name, "Synthetic integration " + marker, updated},
	}
	for i, line := range lines {
		if got := columns.Split(strings.TrimSpace(line), -1); !reflect.DeepEqual(got, want[i]) {
			t.Errorf("inventory row %d = %q, want %q", i, got, want[i])
		}
	}
	if got := string(entries["config/effective.txt"]); !strings.Contains(got, "OLIVARES_CLAUDE_INFERENCE_KEY=<redacted>\n") ||
		!strings.Contains(got, "OLIVARES_LOG_LEVEL=info\n") {
		t.Errorf("config redaction or non-sensitive field retention changed: %q", got)
	}
	if got := string(entries["logs/engine.log"]); !strings.Contains(got, marker) {
		t.Errorf("log redaction marker absent: %q", got)
	}
	var manifest supportbundle.Manifest
	if err := json.Unmarshal(entries["manifest.json"], &manifest); err != nil {
		t.Fatal(err)
	}
	var inventoryRecorded bool
	for _, section := range manifest.Sections {
		if section.Path == "secrets/inventory.txt" {
			inventoryRecorded = true
			if section.Redactions != 1 {
				t.Errorf("inventory redactions = %d, want 1", section.Redactions)
			}
		}
	}
	if !inventoryRecorded {
		t.Error("manifest lacks secret inventory")
	}

	listed := h.do(http.MethodGet, "/v1/console/secrets", admin, nil, nil)
	if listed.code != http.StatusOK {
		t.Fatalf("live secret inventory = %d %s", listed.code, listed.raw)
	}
	secrets, ok := listed.body["secrets"].([]any)
	if !ok || len(secrets) != 1 {
		t.Fatalf("live secret inventory = %v, want one secret", listed.body)
	}
	if got := secrets[0].(map[string]any)["hint"]; got != hint {
		t.Errorf("live admin hint = %v, want unchanged %s", got, hint)
	}
}

func TestConsoleSupportBundleSecretInventoryRequiresAdminAndAAL3(t *testing.T) {
	h := newSecretsHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "privacy-fixture")
	member := h.mkMember(admin, "privacy@example.invalid", "SyntheticMemberPassword1", auth.RoleAdmin, tenant)
	// Members are added before the passkey step-up is required: adding a person asks
	// for the same step-up (HU-28).
	h.requirePasskeyStepUp()
	refused := doWave2Binary(h, http.MethodPost, "/v1/console/support-bundle", admin)
	if refused.Code != http.StatusForbidden || !strings.Contains(refused.Body.String(), "step_up_required") {
		t.Fatalf("admin at AAL1 = %d %s, want 403 step_up_required", refused.Code, refused.Body.String())
	}
	h.elevate(member)
	refused = doWave2Binary(h, http.MethodPost, "/v1/console/support-bundle", member)
	if refused.Code != http.StatusForbidden || strings.Contains(refused.Body.String(), "step_up_required") {
		t.Fatalf("tenant admin at AAL3 = %d %s, want permission denial", refused.Code, refused.Body.String())
	}
	if refused.Header().Get("Content-Disposition") != "" {
		t.Error("permission denial included an archive attachment")
	}
}

func TestConsoleSupportBundleSecretInventoryWithoutRedactorIsSkipped(t *testing.T) {
	h := newSecretsHarness(t)
	admin := h.adminLogin()
	h.elevate(admin)
	put := h.do(http.MethodPut, "/v1/console/secrets", admin, map[string]any{
		"name": "support-bundle/privacy-fixture", "value": supportBundlePrivacySecret,
		"description": "Synthetic integration " + supportBundlePrivacySecret,
	}, nil)
	if put.code != http.StatusOK {
		t.Fatalf("put synthetic secret = %d %s", put.code, put.raw)
	}
	rec := doWave2Binary(h, http.MethodPost, "/v1/console/support-bundle", admin)
	if rec.Code != http.StatusOK {
		t.Fatalf("support bundle without redactor = %d %s", rec.Code, rec.Body.String())
	}
	entries := readWave2SupportBundle(t, rec.Body.Bytes())
	if got := string(entries["secrets/inventory.txt"]); got != "skipped: canonical support-bundle redactor not configured\n" {
		t.Errorf("inventory without redactor = %q, want skip note", got)
	}
}
