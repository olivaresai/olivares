// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/core/supportbundle"
)

// The support bundle is the console surface that leaves the machine, and boot()
// injects its redaction policy (SupportBundleRedact and
// SupportBundleContainsSensitive from modules/security). core/api tests the handler
// contract with test-local doubles; only this package can show that the production
// composition root wires the canonical catalog into the real endpoint.
const (
	// An AWS access-key SHAPE, not a key: nothing here was ever issued. It lives in a
	// secret DESCRIPTION, which the console log floor never sees, so the
	// support-bundle redactor is the only thing between it and the archive.
	supportBundleWiringWitness       = "AKIASUPPORTBUNDLEWIT"
	supportBundleWiringSecretName    = "support-bundle-wiring-fixture"
	supportBundleWiringSecretValue   = "support-bundle-wiring-sealed-value"
	supportBundleWiringAdminEmail    = "admin@support-bundle-wiring.test"
	supportBundleWiringAdminPassword = "support-bundle-wiring-admin-password"
)

// TestSupportBundleHTTPRedactsCatalogSecretThroughCompositionRoot fails if boot()
// omits the support-bundle redaction injection or wires an identity redactor: the
// witness is collected from the runtime secret store, which only that redactor
// scrubs before the archive is written.
func TestSupportBundleHTTPRedactsCatalogSecretThroughCompositionRoot(t *testing.T) {
	// A synthetic 32-byte sealer key, so the booted secret store accepts a write.
	t.Setenv(secretStoreKeyEnv, base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{0x5a}, 32)))
	eng := bootedEngineForLogWiring(t)
	admin := supportBundleWiringAdmin(t, eng)
	stepUpCommunicationHTTPTestUser(t, eng, admin)

	put := communicationHTTPTestRequest(t, eng, http.MethodPut, "/v1/console/secrets", admin, "",
		map[string]any{
			"name":        supportBundleWiringSecretName,
			"value":       supportBundleWiringSecretValue,
			"description": "rotated from " + supportBundleWiringWitness + " during onboarding",
		}, nil)
	if put.status != http.StatusOK {
		t.Fatalf("store fixture secret = %d: %s", put.status, put.raw)
	}

	bundle := communicationHTTPTestRequest(t, eng, http.MethodPost, "/v1/console/support-bundle", admin, "", nil, nil)
	if bundle.status != http.StatusOK {
		t.Fatalf("support bundle = %d: %s", bundle.status, bundle.raw)
	}
	archive := filepath.Join(t.TempDir(), "support-bundle.tar.gz")
	if err := os.WriteFile(archive, bundle.raw, 0o600); err != nil {
		t.Fatalf("write returned archive: %v", err)
	}
	entries := readSupportTestBundle(t, archive)
	for name, content := range entries {
		for _, fixture := range []string{supportBundleWiringWitness, supportBundleWiringSecretValue} {
			if bytes.Contains(content, []byte(fixture)) {
				t.Errorf("%s carries fixture value %q", name, fixture)
			}
		}
	}
	inventory := string(entries["secrets/inventory.txt"])
	if !strings.Contains(inventory, supportBundleWiringSecretName) ||
		!strings.Contains(inventory, "rotated from [redacted:aws-access-key] during onboarding") {
		t.Errorf("secret inventory lacks the stored secret with the catalog redaction: %q", inventory)
	}

	var manifest supportbundle.Manifest
	if err := json.Unmarshal(entries["manifest.json"], &manifest); err != nil {
		t.Fatalf("decode manifest: %v", err)
	}
	for _, section := range manifest.Sections {
		if section.Path == "secrets/inventory.txt" {
			if section.Redactions < 1 {
				t.Errorf("inventory section records %d redactions, want at least 1", section.Redactions)
			}
			return
		}
	}
	t.Error("manifest lacks the secrets/inventory.txt section")
}

// supportBundleWiringAdmin completes first-run setup through the real API and
// returns the first administrator's session token.
func supportBundleWiringAdmin(t *testing.T, eng *engine) string {
	t.Helper()
	setupToken, _, err := eng.setupTok.Ensure()
	if err != nil {
		t.Fatalf("setup token: %v", err)
	}
	setup := communicationHTTPTestRequest(t, eng, http.MethodPost, "/v1/setup", "", "",
		map[string]any{
			"token": setupToken, "email": supportBundleWiringAdminEmail,
			"password": supportBundleWiringAdminPassword,
		}, nil)
	if setup.status != http.StatusCreated {
		t.Fatalf("setup = %d: %s", setup.status, setup.raw)
	}
	login := communicationHTTPTestRequest(t, eng, http.MethodPost, "/v1/auth/login", "", "",
		map[string]any{"email": supportBundleWiringAdminEmail, "password": supportBundleWiringAdminPassword}, nil)
	if login.status != http.StatusOK {
		t.Fatalf("admin login = %d: %s", login.status, login.raw)
	}
	return communicationHTTPTestDecode[struct {
		Token string `json:"token"`
	}](t, login).Token
}
