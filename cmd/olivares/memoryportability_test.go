// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

func wireMemoryImport(t *testing.T, handler http.Handler, token string, tenant model.TenantID, bundle string) (int, map[string]any, string) {
	t.Helper()
	r := httptest.NewRequest("POST", "/v1/m/knowledge/memory/import", strings.NewReader(bundle))
	r.Header.Set("Authorization", "Bearer "+token)
	r.Header.Set("X-Olivares-Tenant", tenant.String())
	r.Header.Set("Content-Type", "application/x-ndjson")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)
	var out map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &out)
	return w.Code, out, w.Body.String()
}

func TestMemoryPortabilityWired(t *testing.T) {
	for _, cfg := range wireBackends(t) {
		t.Run(string(cfg.Engine), func(t *testing.T) {
			dir := t.TempDir()
			if cfg.Engine == store.EngineSQLite {
				cfg.DSN = filepath.Join(dir, "olivares.db")
			}
			eng, token, tenant := wireBoot(t, cfg, dir)
			code, keys, raw := doDemoViewJSON(t, eng.api.Handler(), "GET", "/v1/console/keys", token, "", nil)
			if code != http.StatusOK {
				t.Fatalf("key inventory = %d", code)
			}
			portability := ""
			otherFingerprints := []string{}
			doc := api.OpenAPIDocument()
			schemas := doc["components"].(map[string]any)["schemas"].(map[string]any)
			purposeSchema := schemas["KeyInfo"].(map[string]any)["properties"].(map[string]any)["purpose"].(map[string]any)
			declaredPurposes := map[string]bool{}
			for _, purpose := range purposeSchema["enum"].([]any) {
				declaredPurposes[purpose.(string)] = true
			}
			for _, item := range keys["keys"].([]any) {
				key := item.(map[string]any)
				if !declaredPurposes[key["purpose"].(string)] {
					t.Fatal("runtime key purpose is absent from the published API schema")
				}
				if key["purpose"] == "memory-portability" {
					portability, _ = key["fingerprint"].(string)
					if key["algorithm"] != "ed25519" || key["present"] != true {
						t.Fatal("portability inventory algorithm is incorrect")
					}
				} else if fingerprint, ok := key["fingerprint"].(string); ok {
					otherFingerprints = append(otherFingerprints, fingerprint)
				}
			}
			if portability == "" {
				t.Fatal("dedicated key is missing from the public inventory")
			}
			for _, fingerprint := range otherFingerprints {
				if portability == fingerprint {
					t.Fatal("portability reused another signing key")
				}
			}
			private, err := os.ReadFile(filepath.Join(dir, memoryPortabilityKeyFile))
			if err != nil || strings.Contains(raw, strings.TrimSpace(string(private))) {
				t.Fatal("key inventory disclosed private material")
			}
			code, _, _ = doDemoViewJSON(t, eng.api.Handler(), "POST", "/v1/m/knowledge/memory", token, tenant.String(), map[string]any{
				"agent_ref": "agent-1", "key": "portable", "content": "portable fixture", "classification": "public",
			})
			if code != http.StatusOK {
				t.Fatalf("write memory = %d", code)
			}
			code, _, bundle := doDemoViewJSON(t, eng.api.Handler(), "GET", "/v1/m/knowledge/memory/export?agent_ref=agent-1", token, tenant.String(), nil)
			if code != http.StatusOK {
				t.Fatalf("export = %d", code)
			}
			var manifest map[string]any
			if err := json.Unmarshal([]byte(strings.SplitN(bundle, "\n", 2)[0]), &manifest); err != nil {
				t.Fatal(err)
			}
			if manifest["count"] != float64(1) || manifest["signature"] == "" {
				t.Fatal("export did not carry a signed memory entry")
			}
			code, org, _ := doDemoViewJSON(t, eng.api.Handler(), "POST", "/v1/system/orgs", token, "", map[string]any{"name": "Target", "slug": "wire-memory-target"})
			if code != http.StatusCreated {
				t.Fatalf("create target org = %d", code)
			}
			target := model.TenantID(org["tenant_id"].(string))
			code, _, raw = wireMemoryImport(t, eng.api.Handler(), token, target, strings.Replace(bundle, "portable fixture", "tampered fixture", 1))
			if code != http.StatusBadRequest || !strings.Contains(raw, "digest does not match") {
				t.Fatalf("tampered import = %d", code)
			}
			code, empty, _ := doDemoViewJSON(t, eng.api.Handler(), "GET", "/v1/m/knowledge/memory?agent_ref=agent-1", token, target.String(), nil)
			if code != http.StatusOK || len(empty["items"].([]any)) != 0 {
				t.Fatal("tampered bundle wrote target memory")
			}
			if err := eng.Close(); err != nil {
				t.Fatal(err)
			}
			restarted, err := boot(context.Background(), bootConfig{DataDir: dir, Engine: string(cfg.Engine), DSN: cfg.DSN,
				OwnerDSN: cfg.OwnerDSN, AdminDSN: cfg.AdminDSN, Version: "test", Logger: quietLog()})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = restarted.Close() })
			code, out, _ := wireMemoryImport(t, restarted.api.Handler(), token, target, bundle)
			if code != http.StatusOK || out["imported"] != float64(1) || out["integrity_verified"] != true {
				t.Fatalf("same-installation import after restart = %d", code)
			}
			code, memories, _ := doDemoViewJSON(t, restarted.api.Handler(), "GET", "/v1/m/knowledge/memory?agent_ref=agent-1", token, target.String(), nil)
			if code != http.StatusOK || len(memories["items"].([]any)) != 1 {
				t.Fatal("target did not receive imported memory")
			}
			entry := memories["items"].([]any)[0].(map[string]any)
			if entry["content"] != "portable fixture" {
				t.Fatal("imported memory content changed")
			}
		})
	}
}

func TestMemoryPortabilityForeignInstallationRefused(t *testing.T) {
	cfg := store.Config{Engine: store.EngineSQLite, DSN: ":memory:"}
	first, firstToken, firstTenant := wireBoot(t, cfg, t.TempDir())
	code, _, _ := doDemoViewJSON(t, first.api.Handler(), "POST", "/v1/m/knowledge/memory", firstToken, firstTenant.String(), map[string]any{
		"agent_ref": "agent-1", "key": "foreign", "content": "foreign fixture", "classification": "public",
	})
	if code != http.StatusOK {
		t.Fatalf("write foreign memory = %d", code)
	}
	code, _, bundle := doDemoViewJSON(t, first.api.Handler(), "GET", "/v1/m/knowledge/memory/export", firstToken, firstTenant.String(), nil)
	if code != http.StatusOK {
		t.Fatalf("export = %d", code)
	}
	second, token, tenant := wireBoot(t, cfg, t.TempDir())
	code, _, raw := wireMemoryImport(t, second.api.Handler(), token, tenant, bundle)
	if code != http.StatusBadRequest || !strings.Contains(raw, "signature did not verify") {
		t.Fatalf("foreign installation import = %d", code)
	}
	code, out, _ := doDemoViewJSON(t, second.api.Handler(), "GET", "/v1/m/knowledge/memory", token, tenant.String(), nil)
	if code != http.StatusOK || len(out["items"].([]any)) != 0 {
		t.Fatal("foreign bundle wrote memory")
	}
}

func TestMemoryPortabilityKeyCustody(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, memoryPortabilityKeyFile)
	if _, err := loadMemoryPortabilityKey(dir, withoutMinting()); err == nil {
		t.Fatal("missing read-only key was accepted")
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("read-only load created a key")
	}
	key, err := loadMemoryPortabilityKey(dir)
	if err != nil || !key.created {
		t.Fatal("new dedicated key was not created")
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatal("dedicated key has wider permissions")
	}
	reloaded, err := loadMemoryPortabilityKey(dir, withoutMinting())
	if err != nil || reloaded.created || !bytes.Equal(key.priv, reloaded.priv) {
		t.Fatal("restart replaced the portability key")
	}
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := loadMemoryPortabilityKey(dir); err == nil {
		t.Fatal("world-readable key was accepted")
	}
	if _, err := loadMemoryPortabilityKey(dir, withoutMinting()); err == nil {
		t.Fatal("world-readable read-only key was accepted")
	}
	if err := os.Chmod(path, 0o600); err != nil {
		t.Fatal(err)
	}
	corrupted := append([]byte(nil), key.priv...)
	corrupted[len(corrupted)-1] ^= 1
	if err := os.WriteFile(path, []byte(base64.StdEncoding.EncodeToString(corrupted)), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := loadMemoryPortabilityKey(dir); err == nil {
		t.Fatal("inconsistent Ed25519 key material was accepted")
	}
	if _, err := loadMemoryPortabilityKey(dir, withoutMinting()); err == nil {
		t.Fatal("inconsistent read-only Ed25519 key material was accepted")
	}
	if err := os.WriteFile(path, []byte("invalid-fixture-key"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := loadMemoryPortabilityKey(dir); err == nil {
		t.Fatal("corrupt key was replaced or accepted")
	}
}

func TestMemoryPortabilityUnavailableDoesNotBlockBoot(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, memoryPortabilityKeyFile), []byte("invalid-fixture-key"), 0o600); err != nil {
		t.Fatal(err)
	}
	eng, token, tenant := wireBoot(t, store.Config{Engine: store.EngineSQLite, DSN: ":memory:"}, dir)
	code, keys, _ := doDemoViewJSON(t, eng.api.Handler(), "GET", "/v1/console/keys", token, "", nil)
	if code != http.StatusOK {
		t.Fatalf("unavailable key inventory = %d", code)
	}
	missing := false
	for _, item := range keys["keys"].([]any) {
		key := item.(map[string]any)
		if key["purpose"] == "memory-portability" {
			missing = key["present"] == false && key["custody_mode"] == nil && key["fingerprint"] == nil
		}
	}
	if !missing {
		t.Fatal("unavailable key inventory claimed usable custody")
	}
	code, _, raw := doDemoViewJSON(t, eng.api.Handler(), "GET", "/v1/m/knowledge/memory/export", token, tenant.String(), nil)
	if code != http.StatusNotImplemented || !strings.Contains(raw, "no signing key wired") {
		t.Fatalf("unavailable export = %d", code)
	}
	code, _, raw = wireMemoryImport(t, eng.api.Handler(), token, tenant, "{}\n")
	if code != http.StatusNotImplemented || !strings.Contains(raw, "no verify key wired") {
		t.Fatalf("unavailable import = %d", code)
	}
}

func TestMemoryPortabilityBootDoesNotMintUnderLoadOnlyCustody(t *testing.T) {
	for _, mode := range []string{"read-only", "shared"} {
		t.Run(mode, func(t *testing.T) {
			dir := t.TempDir()
			dsn := filepath.Join(dir, "olivares.db")
			eng, token, tenant := wireBoot(t, store.Config{Engine: store.EngineSQLite, DSN: dsn}, dir)
			if err := eng.Close(); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(dir, memoryPortabilityKeyFile)
			if err := os.Remove(path); err != nil {
				t.Fatal(err)
			}
			if mode == "shared" {
				t.Setenv(envAuditKeyFile, filepath.Join(dir, "audit-signing.key"))
			}
			loaded, err := boot(context.Background(), bootConfig{DataDir: dir, Engine: "sqlite", DSN: dsn,
				ReadOnly: mode == "read-only", Version: "test", Logger: quietLog()})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = loaded.Close() })
			if _, err := os.Stat(path); !os.IsNotExist(err) {
				t.Fatal("load-only boot minted a replacement portability key")
			}
			code, _, _ := doDemoViewJSON(t, loaded.api.Handler(), "GET", "/v1/m/knowledge/memory/export", token, tenant.String(), nil)
			if code != http.StatusNotImplemented {
				t.Fatalf("load-only missing-key export = %d", code)
			}
		})
	}
}
