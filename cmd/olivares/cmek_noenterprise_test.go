// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

//go:build !enterprise

package main

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCommunityCMEKCommandsAreAbsent(t *testing.T) {
	clearOlivaresEnv(t)
	cmd := newKeysCmd()
	for _, verb := range []string{"wrap", "rotate", "rewrap", "seal", "unseal"} {
		for _, child := range cmd.Commands() {
			if child.Name() == verb {
				t.Errorf("Community exposes keys %s", verb)
			}
		}
	}
	found := false
	for _, child := range cmd.Commands() {
		if child.Name() == "status" {
			found = true
		}
	}
	if !found {
		t.Fatal("Community lost keys status")
	}
}

func TestCommunityCMEKBootRefusesBeforeCreatingAnything(t *testing.T) {
	clearOlivaresEnv(t)
	for _, source := range []string{envAuditWrapped, envCatalogWrapped, envPolicyWrapped, envKeyCustody, envKeyWrap, "OLIVARES_SOURCES_CONFIG", envEventingEgressPolicy, "nested-piv-ca", "nested-reporting-key"} {
		t.Run(source, func(t *testing.T) {
			root := t.TempDir()
			setCommunityCMEKSource(t, root, source)
			dir := filepath.Join(root, "data")
			eng, err := boot(context.Background(), bootConfig{DataDir: dir, Logger: discardLog(), NoIngest: true})
			if eng != nil {
				defer eng.Close()
			}
			if err == nil || !strings.Contains(err.Error(), "Business") || !strings.Contains(err.Error(), "dr backup") {
				t.Fatalf("CMEK refusal must name Business data recovery, got %v", err)
			}
			if _, err := os.Stat(dir); !os.IsNotExist(err) {
				t.Fatalf("refused boot changed data directory: %v", err)
			}
		})
	}
}

func TestCommunityOperatorKeysAndCustodyRemainAvailable(t *testing.T) {
	clearOlivaresEnv(t)
	for _, file := range []bool{false, true} {
		t.Run(map[bool]string{false: "env", true: "file"}[file], func(t *testing.T) {
			_, private, err := ed25519.GenerateKey(rand.Reader)
			if err != nil {
				t.Fatal(err)
			}
			value := base64.StdEncoding.EncodeToString(private)
			key := envAuditKey
			if file {
				key = envAuditKeyFile
				path := filepath.Join(t.TempDir(), "audit.key")
				if err := os.WriteFile(path, []byte(value), 0600); err != nil {
					t.Fatal(err)
				}
				value = path
			}
			t.Setenv(key, value)
			t.Setenv(envKeyCustody, "byok")
			for node := 0; node < 2; node++ {
				dir := t.TempDir()
				eng, err := boot(context.Background(), bootConfig{DataDir: dir, Logger: discardLog(), NoIngest: true})
				if err != nil {
					t.Fatal(err)
				}
				defer eng.Close()
				got, err := loadAuditSigningKey(dir, discardLog())
				if err != nil || !bytes.Equal(got.priv, private) {
					t.Fatal("boot did not keep the operator's key")
				}
				if _, err := os.Stat(filepath.Join(dir, "audit-signing.key")); !os.IsNotExist(err) {
					t.Fatal("BYOK boot minted a replacement audit key")
				}
				cmd := newKeysCmd()
				cmd.SetOut(io.Discard)
				cmd.SetErr(io.Discard)
				cmd.SetArgs([]string{"status"})
				if err := cmd.Execute(); err != nil {
					t.Fatal(err)
				}
			}
		})
	}
	t.Setenv(envKeyCustody, "byok")
	t.Setenv(envLedgerCustody, "hyok")
	assertions, err := loadCustodyAssertions()
	if err != nil {
		t.Fatal(err)
	}
	if err := assertions.verify(custodyModeBYOKFile, true); err != nil {
		t.Fatal(err)
	}
	if err := assertions.verify(custodyModeBYOKFile, false); err == nil {
		t.Fatal("HYOK accepted an on-box signer")
	}
}

func TestCommunityCMEKOfflineCommandsRefuse(t *testing.T) {
	clearOlivaresEnv(t)
	for _, source := range []string{envAuditWrapped, envCatalogWrapped, envPolicyWrapped, envKeyCustody, envKeyWrap, "OLIVARES_SOURCES_CONFIG", envCommunicationContentKeyringFile, envCommunicationCursorKeyringFile, envEventingEgressPolicy, "nested-piv-ca", "nested-reporting-key"} {
		t.Run(source, func(t *testing.T) {
			rootDir := t.TempDir()
			setCommunityCMEKSource(t, rootDir, source)
			kek := filepath.Join(rootDir, "backup.key")
			if err := os.WriteFile(kek, bytes.Repeat([]byte{0x42}, 32), 0600); err != nil {
				t.Fatal(err)
			}
			dir := filepath.Join(rootDir, "data")
			for _, args := range [][]string{
				{"dr", "backup", "--data-dir", dir, "--out", filepath.Join(rootDir, "backup.tgz"), "--kek-key-file", kek},
				{"audit", "verify", "--data-dir", dir, "--tenant", "00000000-0000-0000-0000-000000000001"},
				{"migrate", "status", "--data-dir", dir},
				{"migrate", "apply"},
				{"db", "check", "--dsn", filepath.Join(dir, "olivares.db")},
				{"db", "init", "--print-sql"},
				{"dr", "verify", "--in", filepath.Join(rootDir, "missing.bundle")},
				{"dr", "drill", "--events", "1"},
				{"setup", "--out", filepath.Join(rootDir, "setup.env"), "--secrets-dir", filepath.Join(rootDir, "secrets")},
			} {
				cmd := newRootCmd()
				// Script the real wizard up to its direct provisioning operation.
				cmd.SetIn(strings.NewReader(strings.Join([]string{"3", "", "", "", "", "", "fixture-app", "", "fixture-owner", "", "fixture-admin", "yes", "invalid-maintenance-dsn"}, "\n") + "\n"))
				cmd.SetOut(io.Discard)
				cmd.SetErr(io.Discard)
				cmd.SetArgs(args)
				err := cmd.Execute()
				if err == nil || !strings.Contains(err.Error(), "Business") {
					t.Errorf("%v: want Business refusal, got %v", args[:2], err)
				}
			}
			if _, err := os.Stat(dir); !os.IsNotExist(err) {
				t.Fatalf("offline refusal changed data directory: %v", err)
			}
		})
	}
}

// Inspection remains available to an operator deciding which Business build/key
// service can recover the installation. Metadata is never labelled authenticated.
func TestCommunityCMEKStatusKeepsUnverifiedMetadata(t *testing.T) {
	clearOlivaresEnv(t)
	t.Setenv(envKeyWrap, "")
	t.Setenv(envKeyCustody, "cmek")
	path := filepath.Join(t.TempDir(), "audit.sealed")
	if err := os.WriteFile(path, []byte(`{"olivares_sealed":1,"purpose":"audit-signing-key","provider":"aws-kms","key_id":"fixture-key","public_key":"AQ=="}`), 0600); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	cmd := newRootCmd()
	cmd.SetOut(&out)
	cmd.SetErr(io.Discard)
	cmd.SetArgs([]string{"keys", "status", "--audit-envelope", path, "-o", "json"})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	var report struct {
		Envelopes map[string]struct {
			Authenticated bool   `json:"authenticated"`
			Provenance    string `json:"provenance"`
			PublicKey     string `json:"public_key"`
		} `json:"envelopes"`
	}
	if err := json.Unmarshal(out.Bytes(), &report); err != nil {
		t.Fatal(err)
	}
	slot, ok := report.Envelopes["audit"]
	if !ok || slot.Authenticated || slot.PublicKey != "AQ==" || !strings.Contains(slot.Provenance, "NOT proven") {
		t.Fatalf("inspection lost metadata/provenance: %s", out.String())
	}
}

func setCommunityCMEKSource(t *testing.T, root, source string) {
	t.Helper()
	path := filepath.Join(root, "config.sealed")
	if err := os.WriteFile(path, []byte(`{"olivares_sealed":1,"purpose":"operator-config"}`), 0600); err != nil {
		t.Fatal(err)
	}
	value := path
	switch source {
	case "sealed-with-extra-field":
		if err := os.WriteFile(path, []byte(`{"olivares_sealed":1,"client_ca_file":false}`), 0600); err != nil {
			t.Fatal(err)
		}
		source = "OLIVARES_SOURCES_CONFIG"
	case envKeyCustody:
		value = "cmek"
	case envKeyWrap:
		value = "aws-kms"
	case "nested-piv-ca", "nested-reporting-key":
		parent := map[string]any{"client_ca_file": path}
		if source == "nested-reporting-key" {
			parent = map[string]any{"bundle_signing": map[string]string{"private_key_path": path}}
			source = "OLIVARES_REPORTING_CONFIG"
		} else {
			source = "OLIVARES_PIV_CONFIG"
		}
		raw, err := json.Marshal(parent)
		if err != nil {
			t.Fatal(err)
		}
		value = filepath.Join(root, "parent.json")
		if err := os.WriteFile(value, raw, 0600); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv(source, value)
}

func TestCommunityCMEKReviewGuards(t *testing.T) {
	clearOlivaresEnv(t)
	for _, source := range []string{"OLIVARES_CODEX_HOOK_PEP_CONFIG", "OLIVARES_GROK_HOOK_PEP_CONFIG", "sealed-with-extra-field"} {
		t.Run(source, func(t *testing.T) {
			root := t.TempDir()
			setCommunityCMEKSource(t, root, source)
			dir := filepath.Join(root, "data")
			eng, err := boot(context.Background(), bootConfig{DataDir: dir, Logger: discardLog(), NoIngest: true})
			if eng != nil {
				defer eng.Close()
			}
			if err == nil || !strings.Contains(err.Error(), "Business") {
				t.Fatalf("guard bypassed: %v", err)
			}
			if _, err := os.Stat(dir); !os.IsNotExist(err) {
				t.Fatalf("guard changed installation: %v", err)
			}
		})
	}
	t.Run("directory-maintenance", func(t *testing.T) {
		t.Setenv(envKeyCustody, "cmek")
		dir := filepath.Join(t.TempDir(), "absent")
		cmd := newRootCmd()
		cmd.SetOut(io.Discard)
		cmd.SetErr(io.Discard)
		cmd.SetArgs([]string{"db", "activate-directory-writer", "--data-dir", dir, "--expected-generation", "1", "--writers-upgraded", "--writers-drained", "--actor", "fixture", "--reason", "fixture"})
		if err := cmd.Execute(); err == nil || !strings.Contains(err.Error(), "Business") {
			t.Fatalf("maintenance bypassed refusal: %v", err)
		}
		if _, err := os.Stat(dir); !os.IsNotExist(err) {
			t.Fatalf("maintenance changed installation: %v", err)
		}
	})
}

func TestCommunityCMEKSavedActivationRefuses(t *testing.T) {
	clearOlivaresEnv(t)
	setActivationOverlayForTest(nil)
	t.Cleanup(func() { setActivationOverlayForTest(nil) })
	for _, source := range []string{"OLIVARES_SOURCES_CONFIG", "OLIVARES_CODEX_HOOK_PEP_CONFIG"} {
		for _, pending := range []bool{false, true} {
			t.Run(source+"/"+map[bool]string{false: "active", true: "pending"}[pending], func(t *testing.T) {
				dir := t.TempDir()
				sealed := filepath.Join(dir, "sources.sealed")
				if err := os.WriteFile(sealed, []byte(`{"olivares_sealed":1}`), 0600); err != nil {
					t.Fatal(err)
				}
				state := ActivationActive
				if pending {
					state = ActivationPending
				}
				m := ActivationManifest{Version: activationManifestVersion, Entries: []ActivationEntry{{Addon: "fixture", Env: source, Value: sealed, State: state}}}
				raw, err := json.Marshal(m)
				if err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(ActivationManifestPath(dir), raw, 0600); err != nil {
					t.Fatal(err)
				}
				if pending {
					// A pending entry is inert and must not block ordinary key inspection.
					cmd := newRootCmd()
					cmd.SetOut(io.Discard)
					cmd.SetErr(io.Discard)
					cmd.SetArgs([]string{"migrate", "status", "--data-dir", dir})
					if err := cmd.Execute(); err != nil && strings.Contains(err.Error(), "Business") {
						t.Fatalf("pending config was treated as active: %v", err)
					}
					return
				}
				eng, err := boot(context.Background(), bootConfig{DataDir: dir, Logger: discardLog(), NoIngest: true})
				if eng != nil {
					defer eng.Close()
				}
				if err == nil || !strings.Contains(err.Error(), "Business") {
					t.Errorf("saved config boot bypass: %v", err)
				}
				for _, args := range [][]string{
					{"audit", "verify", "--data-dir", dir, "--tenant", "00000000-0000-0000-0000-000000000001"},
					{"migrate", "status", "--data-dir", dir},
					{"db", "init", "--data-dir", dir, "--print-sql", "--database", "fixturedb", "--app-role", "fixtureapp", "--owner-role", "fixtureowner"},
					{"db", "activate-directory-writer", "--data-dir", dir, "--expected-generation", "1", "--writers-upgraded", "--writers-drained", "--actor", "fixture", "--reason", "fixture"},
				} {
					cmd := newRootCmd()
					cmd.SetOut(io.Discard)
					cmd.SetErr(io.Discard)
					cmd.SetArgs(args)
					if err := cmd.Execute(); err == nil || !strings.Contains(err.Error(), "Business") {
						t.Errorf("saved config offline bypass %v: %v", args[:2], err)
					}
				}
				entries, err := os.ReadDir(dir)
				if err != nil {
					t.Fatal(err)
				}
				if len(entries) != 2 {
					t.Errorf("refused installation changed: %v", entries)
				}
			})
		}
	}
}

func TestCommunityCMEKDSNInstallationRefuses(t *testing.T) {
	clearOlivaresEnv(t)
	setActivationOverlayForTest(nil)
	t.Cleanup(func() { setActivationOverlayForTest(nil) })
	dir := t.TempDir()
	sealed := filepath.Join(dir, "sources.sealed")
	if err := os.WriteFile(sealed, []byte(`{"olivares_sealed":1}`), 0600); err != nil {
		t.Fatal(err)
	}
	manifest := ActivationManifest{Version: activationManifestVersion, Entries: []ActivationEntry{{Addon: "fixture", Env: "OLIVARES_SOURCES_CONFIG", Value: sealed, State: ActivationActive}}}
	raw, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(ActivationManifestPath(dir), raw, 0600); err != nil {
		t.Fatal(err)
	}
	dsn := filepath.Join(dir, "olivares.db")
	// A regular placeholder lets read-only maintenance reach the open on RED.
	if err := os.WriteFile(dsn, []byte("existing database placeholder"), 0600); err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(t.TempDir(), "database-alias")
	if err := os.Symlink(dsn, alias); err != nil {
		t.Fatal(err)
	}
	for _, target := range []string{dsn, alias} {
		for _, args := range [][]string{
			{"db", "check", "--engine", "sqlite", "--dsn", target},
			{"migrate", "status", "--dsn", target},
			{"db", "activate-directory-writer", "--dsn", target, "--expected-generation", "1", "--writers-upgraded", "--writers-drained", "--actor", "fixture", "--reason", "fixture"},
			{"audit", "verify", "--dsn", target, "--data-dir", t.TempDir(), "--tenant", "00000000-0000-0000-0000-000000000001"},
		} {
			cmd := newRootCmd()
			cmd.SetOut(io.Discard)
			cmd.SetErr(io.Discard)
			cmd.SetArgs(args)
			if err := cmd.Execute(); err == nil || !strings.Contains(err.Error(), "Business") {
				t.Errorf("DSN-only bypass %v: %v", args[:2], err)
			}
		}
		outputDir := filepath.Join(t.TempDir(), "absent")
		eng, err := boot(context.Background(), bootConfig{DataDir: outputDir, Engine: "sqlite", DSN: target, Logger: discardLog(), NoIngest: true})
		if eng != nil {
			defer eng.Close()
		}
		if err == nil || !strings.Contains(err.Error(), "Business") {
			t.Errorf("DSN-only boot bypass: %v", err)
		}
		if _, err := os.Stat(outputDir); !os.IsNotExist(err) {
			t.Errorf("DSN refusal created keys directory: %v", err)
		}
	}
	got, err := os.ReadFile(dsn)
	if err != nil || string(got) != "existing database placeholder" {
		t.Fatalf("DSN refusal changed store: %v", err)
	}
}

func TestCommunityCMEKGuardPreservesPlaintextOverridesAndNoHome(t *testing.T) {
	clearOlivaresEnv(t)
	t.Run("zero-marker-plaintext", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "sources.json")
		if err := os.WriteFile(path, []byte(`{"sources":[],"olivares_sealed":0}`), 0600); err != nil {
			t.Fatal(err)
		}
		t.Setenv("OLIVARES_SOURCES_CONFIG", path)
		cmd := newRootCmd()
		cmd.SetOut(io.Discard)
		cmd.SetErr(io.Discard)
		cmd.SetArgs([]string{"db", "init", "--print-sql"})
		if err := cmd.Execute(); err != nil {
			t.Fatalf("ordinary JSON was treated as CMEK: %v", err)
		}
	})
	t.Run("no-home", func(t *testing.T) {
		t.Setenv("HOME", "")
		t.Setenv("XDG_DATA_HOME", "")
		cmd := newRootCmd()
		cmd.SetOut(io.Discard)
		cmd.SetErr(io.Discard)
		cmd.SetArgs([]string{"db", "init", "--print-sql"})
		if err := cmd.Execute(); err != nil {
			t.Fatalf("plaintext preview gained a directory requirement: %v", err)
		}
	})
	t.Run("process-override", func(t *testing.T) {
		dir := t.TempDir()
		sealed := filepath.Join(dir, "sources.sealed")
		if err := os.WriteFile(sealed, []byte(`{"olivares_sealed":1}`), 0600); err != nil {
			t.Fatal(err)
		}
		m := ActivationManifest{Version: activationManifestVersion, Entries: []ActivationEntry{{Addon: "fixture", Env: "OLIVARES_SOURCES_CONFIG", Value: sealed, State: ActivationActive}}}
		raw, err := json.Marshal(m)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(ActivationManifestPath(dir), raw, 0600); err != nil {
			t.Fatal(err)
		}
		plain := filepath.Join(dir, "sources.json")
		if err := os.WriteFile(plain, []byte(`{"sources":[]}`), 0600); err != nil {
			t.Fatal(err)
		}
		t.Setenv("OLIVARES_SOURCES_CONFIG", plain)
		cmd := newRootCmd()
		cmd.SetOut(io.Discard)
		cmd.SetErr(io.Discard)
		cmd.SetArgs([]string{"db", "init", "--data-dir", dir, "--print-sql", "--database", "fixturedb", "--app-role", "fixtureapp", "--owner-role", "fixtureowner"})
		if err := cmd.Execute(); err != nil {
			t.Fatalf("process plaintext override was lost: %v", err)
		}
	})
}

func TestCommunityCMEKJSONProbeUsesLoaderContract(t *testing.T) {
	resetSealedConfigFailure()
	t.Cleanup(resetSealedConfigFailure)
	clearOlivaresEnv(t)
	for _, name := range []string{"uppercase-marker", "duplicate-marker-null", "uppercase-piv-parent", "duplicate-piv-parent-null", "uppercase-reporting-parent", "duplicate-reporting-parent-null"} {
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			child := filepath.Join(root, "child.sealed")
			if err := os.WriteFile(child, []byte(`{"olivares_sealed":1}`), 0600); err != nil {
				t.Fatal(err)
			}
			parent := filepath.Join(root, "parent.json")
			envName := "OLIVARES_SOURCES_CONFIG"
			var body []byte
			quoted, _ := json.Marshal(child)
			switch name {
			case "uppercase-marker":
				body = []byte(`{"OLIVARES_SEALED":1}`)
			case "duplicate-marker-null":
				body = []byte(`{"olivares_sealed":1,"olivares_sealed":null}`)
			case "uppercase-piv-parent":
				envName = "OLIVARES_PIV_CONFIG"
				body = []byte(`{"CLIENT_CA_FILE":` + string(quoted) + `}`)
			case "duplicate-piv-parent-null":
				envName = "OLIVARES_PIV_CONFIG"
				body = []byte(`{"client_ca_file":` + string(quoted) + `,"client_ca_file":null}`)
			case "uppercase-reporting-parent":
				envName = "OLIVARES_REPORTING_CONFIG"
				body = []byte(`{"BUNDLE_SIGNING":{"PRIVATE_KEY_PATH":` + string(quoted) + `}}`)
			case "duplicate-reporting-parent-null":
				envName = "OLIVARES_REPORTING_CONFIG"
				body = []byte(`{"bundle_signing":{"private_key_path":` + string(quoted) + `},"bundle_signing":null}`)
			}
			if err := os.WriteFile(parent, body, 0600); err != nil {
				t.Fatal(err)
			}
			t.Setenv(envName, parent)
			dir := filepath.Join(root, "absent")
			cmd := newRootCmd()
			cmd.SetOut(io.Discard)
			cmd.SetErr(io.Discard)
			cmd.SetArgs([]string{"db", "init", "--data-dir", dir, "--print-sql", "--database", "fixturedb", "--app-role", "fixtureapp", "--owner-role", "fixtureowner"})
			if err := cmd.Execute(); err == nil || !strings.Contains(err.Error(), "Business") {
				t.Errorf("JSON contract bypassed preflight: %v", err)
			}
			eng, err := boot(context.Background(), bootConfig{DataDir: dir, Logger: discardLog(), NoIngest: true})
			if eng != nil {
				defer eng.Close()
			}
			if err == nil || !strings.Contains(err.Error(), "Business") {
				t.Errorf("JSON contract bypassed boot: %v", err)
			}
			if _, err := os.Stat(dir); !os.IsNotExist(err) {
				t.Errorf("JSON refusal created installation: %v", err)
			}
		})
	}
}

func TestCommunityCMEKResolvedURIRefuses(t *testing.T) {
	clearOlivaresEnv(t)
	setActivationOverlayForTest(nil)
	t.Cleanup(func() { setActivationOverlayForTest(nil) })
	dir := t.TempDir()
	dsn := filepath.Join(dir, "olivares.db")
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec("CREATE TABLE fixture (v INTEGER)"); err != nil {
		db.Close()
		t.Fatal(err)
	}
	if err = db.Close(); err != nil {
		t.Fatal(err)
	}
	sealed := filepath.Join(dir, "sources.sealed")
	if err = os.WriteFile(sealed, []byte(`{"olivares_sealed":1}`), 0600); err != nil {
		t.Fatal(err)
	}
	manifest := ActivationManifest{Version: activationManifestVersion, Entries: []ActivationEntry{{Addon: "fixture", Env: "OLIVARES_SOURCES_CONFIG", Value: sealed, State: ActivationActive}}}
	raw, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(ActivationManifestPath(dir), raw, 0600); err != nil {
		t.Fatal(err)
	}
	uri := (&url.URL{Scheme: "file", Path: dsn, RawQuery: "mode=ro"}).String()
	t.Setenv("CMEK_TEST_SQLITE_DSN", uri)
	before, err := os.ReadFile(dsn)
	if err != nil {
		t.Fatal(err)
	}
	kek := filepath.Join(t.TempDir(), "backup.key")
	if err = os.WriteFile(kek, bytes.Repeat([]byte{7}, 32), 0600); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{
		{"db", "check", "--engine", "sqlite", "--dsn", "env:CMEK_TEST_SQLITE_DSN"},
		{"dr", "backup", "--engine", "sqlite", "--dsn", "env:CMEK_TEST_SQLITE_DSN", "--data-dir", t.TempDir(), "--out", filepath.Join(t.TempDir(), "refused.backup"), "--kek-key-file", kek},
	} {
		cmd := newRootCmd()
		cmd.SetOut(io.Discard)
		cmd.SetErr(io.Discard)
		cmd.SetArgs(args)
		if err := cmd.Execute(); err == nil || !strings.Contains(err.Error(), "Business") {
			t.Errorf("resolved URI bypass %v: %v", args[:2], err)
		}
	}
	after, err := os.ReadFile(dsn)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatalf("URI refusal changed database: %v", err)
	}
}

func TestCommunityCMEKWhitespacePathContract(t *testing.T) {
	resetSealedConfigFailure()
	t.Cleanup(resetSealedConfigFailure)
	clearOlivaresEnv(t)
	for _, name := range []string{"OLIVARES_AGENT_GATEWAY_CONFIG", "OLIVARES_APPROVAL_BRIDGE_CONFIG", "OLIVARES_CLAUDE_ADMIN_ACTUATOR_CONFIG", "OLIVARES_CLAUDE_ERASER_CONFIG", "OLIVARES_CLAUDE_FILES_CONFIG", "OLIVARES_CODEX_HOOK_PEP_CONFIG", "OLIVARES_DEPLOY_EXECUTOR_CONFIG", "OLIVARES_GROK_HOOK_PEP_CONFIG", "OLIVARES_HITL_CONFIG", "OLIVARES_HOOK_PEP_CONFIG", "OLIVARES_INFERENCE_PROXY_CONFIG", "OLIVARES_NHI_ACTUATORS_CONFIG", "OLIVARES_NOTIFY_CONFIG", "OLIVARES_ORCH_DISPATCH_CONFIG", "OLIVARES_PIV_CONFIG", "OLIVARES_RATELIMIT_CONFIG", "OLIVARES_SANDBOX_RUNTIME_CONFIG", "OLIVARES_SOURCES_CONFIG", "OLIVARES_VOICE_CALL_CONFIG", "OLIVARES_VOICE_DISPATCH_CONFIG", "OLIVARES_COMMUNICATION_CONTENT_KEYRING_FILE", "OLIVARES_COMMUNICATION_CURSOR_KEYRING_FILE"} {
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			literal := filepath.Join(root, "operator.json ")
			trimmed := strings.TrimSpace(literal)
			if err := os.WriteFile(literal, []byte(`{"olivares_sealed":1}`), 0600); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(trimmed, []byte(`{}`), 0600); err != nil {
				t.Fatal(err)
			}
			t.Setenv(name, literal)
			if _, err := readOperatorConfig(literal); err == nil || !strings.Contains(err.Error(), "Business") {
				t.Fatalf("loader must recognize literal ciphertext: %v", err)
			}
			cmd := newRootCmd()
			cmd.SetOut(io.Discard)
			cmd.SetErr(io.Discard)
			cmd.SetArgs([]string{"db", "init", "--data-dir", filepath.Join(root, "absent"), "--print-sql", "--database", "fixturedb", "--app-role", "fixtureapp", "--owner-role", "fixtureowner"})
			if err := cmd.Execute(); err == nil || !strings.Contains(err.Error(), "Business") {
				t.Errorf("literal path refusal bypassed: %v", err)
			}
			// The raw loader chooses this plaintext file, not the unused sealed sibling.
			if err := os.WriteFile(literal, []byte(`{}`), 0600); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(trimmed, []byte(`{"olivares_sealed":1}`), 0600); err != nil {
				t.Fatal(err)
			}
			cmd = newRootCmd()
			cmd.SetOut(io.Discard)
			cmd.SetErr(io.Discard)
			cmd.SetArgs([]string{"db", "init", "--data-dir", filepath.Join(root, "absent"), "--print-sql", "--database", "fixturedb", "--app-role", "fixtureapp", "--owner-role", "fixtureowner"})
			if err := cmd.Execute(); err != nil {
				t.Errorf("literal plaintext path changed: %v", err)
			}
		})
	}
	t.Run("trimmed-reporting-child", func(t *testing.T) {
		root := t.TempDir()
		child := filepath.Join(root, "child.sealed")
		if err := os.WriteFile(child, []byte(`{"olivares_sealed":1}`), 0600); err != nil {
			t.Fatal(err)
		}
		parent := filepath.Join(root, "reporting.json")
		body, _ := json.Marshal(map[string]any{"bundle_signing": map[string]string{"private_key_path": child + " "}})
		if err := os.WriteFile(parent, body, 0600); err != nil {
			t.Fatal(err)
		}
		t.Setenv("OLIVARES_REPORTING_CONFIG", parent)
		cmd := newRootCmd()
		cmd.SetOut(io.Discard)
		cmd.SetErr(io.Discard)
		cmd.SetArgs([]string{"db", "init", "--print-sql", "--database", "fixturedb", "--app-role", "fixtureapp", "--owner-role", "fixtureowner"})
		if err := cmd.Execute(); err == nil || !strings.Contains(err.Error(), "Business") {
			t.Errorf("reporting child trim bypass: %v", err)
		}
	})
}

func TestCommunityCMEKMigrateHardlinkRefuses(t *testing.T) {
	resetSealedConfigFailure()
	t.Cleanup(resetSealedConfigFailure)
	clearOlivaresEnv(t)
	setActivationOverlayForTest(nil)
	t.Cleanup(func() { setActivationOverlayForTest(nil) })
	root := t.TempDir()
	eng, err := boot(context.Background(), bootConfig{DataDir: root, Logger: discardLog(), NoIngest: true})
	if err != nil {
		t.Fatal(err)
	}
	if err = eng.Close(); err != nil {
		t.Fatal(err)
	}
	dsn := filepath.Join(root, "olivares.db")
	linked := filepath.Join(root, "hardlink.db")
	if err = os.Link(dsn, linked); err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(t.TempDir(), "linked-symlink.db")
	if err = os.Symlink(linked, alias); err != nil {
		t.Fatal(err)
	}
	sealed := filepath.Join(root, "sources.sealed")
	if err = os.WriteFile(sealed, []byte(`{"olivares_sealed":1}`), 0600); err != nil {
		t.Fatal(err)
	}
	for _, active := range []bool{false, true} {
		state := ActivationPending
		if active {
			state = ActivationActive
		}
		manifest := ActivationManifest{Version: activationManifestVersion, Entries: []ActivationEntry{{Addon: "fixture", Env: "OLIVARES_SOURCES_CONFIG", Value: sealed, State: state}}}
		body, err := json.Marshal(manifest)
		if err != nil {
			t.Fatal(err)
		}
		if err = os.WriteFile(ActivationManifestPath(root), body, 0600); err != nil {
			t.Fatal(err)
		}
		for _, target := range []string{linked, alias} {
			cmd := newRootCmd()
			cmd.SetOut(io.Discard)
			cmd.SetErr(io.Discard)
			cmd.SetArgs([]string{"migrate", "status", "--dsn", target})
			err := cmd.Execute()
			if active && (err == nil || !strings.Contains(err.Error(), "Business")) {
				t.Errorf("hardlink CMEK status bypass: %v", err)
			}
			if !active && err != nil {
				t.Errorf("ordinary hardlinked store status changed: %v", err)
			}
		}
	}
}
