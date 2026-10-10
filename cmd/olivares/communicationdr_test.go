// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/dr"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/modules/sessions"
)

func TestDRDefaultCommunicationCustodyRestoresContentAndCursor(t *testing.T) {
	for _, console := range []bool{false, true} {
		t.Run(fmt.Sprintf("console=%t", console), func(t *testing.T) { testDRCommunicationRecovery(t, console) })
	}
}

func testDRCommunicationRecovery(t *testing.T, console bool) {
	previous := version
	version = "26.1002"
	t.Cleanup(func() { version = previous })
	for _, name := range []string{envKeyWrap, envCommunicationActivation, envCommunicationContentKeyringFile, envCommunicationCursorKeyringFile} {
		t.Setenv(name, "")
	}
	estate := communicationHTTPTestSQLiteStore(t)
	bootEstate := func() *engine {
		t.Helper()
		prepareCompositionTestBoot(t)
		cfg := estate.bootConfig()
		cfg.Version = version
		eng, err := boot(context.Background(), cfg)
		if err != nil {
			t.Fatal(err)
		}
		return eng
	}
	eng := bootEstate()
	t.Cleanup(func() { _ = eng.Close() })
	request := func(method, path, token string, tenant model.TenantID, body any, headers map[string]string, want int) communicationHTTPTestResponse {
		t.Helper()
		r := communicationHTTPTestRequest(t, eng, method, path, token, tenant, body, headers)
		if r.status != want {
			t.Fatalf("%s %s = %d, want %d: %s", method, path, r.status, want, r.raw)
		}
		return r
	}
	setupToken, _, err := eng.setupTok.Ensure()
	if err != nil {
		t.Fatal(err)
	}
	request(http.MethodPost, "/v1/setup", "", "", map[string]any{"token": setupToken, "email": "admin@recovery.test", "password": "recovery-admin-password"}, nil, http.StatusCreated)
	login := request(http.MethodPost, "/v1/auth/login", "", "", map[string]any{"email": "admin@recovery.test", "password": "recovery-admin-password"}, nil, http.StatusOK)
	admin := communicationHTTPTestDecode[struct {
		Token string `json:"token"`
	}](t, login)
	org := request(http.MethodPost, "/v1/system/orgs", admin.Token, "", map[string]any{"name": "Recovery", "slug": "recovery"}, nil, http.StatusCreated)
	tenant := communicationHTTPTestDecode[struct {
		Tenant model.TenantID `json:"tenant_id"`
	}](t, org).Tenant
	owner := createCommunicationHTTPTestUser(t, eng, admin.Token, tenant, "owner@recovery.test", auth.RoleOwner)
	reader := createCommunicationHTTPTestUser(t, eng, admin.Token, tenant, "reader@recovery.test", auth.RoleEditor)
	if err := eng.Close(); err != nil {
		t.Fatal(err)
	}
	eng = bootEstate()
	owner = loginCommunicationHTTPTestUser(t, eng, owner.id, "owner@recovery.test")
	reader = loginCommunicationHTTPTestUser(t, eng, reader.id, "reader@recovery.test")
	stepUpCommunicationHTTPTestUser(t, eng, owner.token)
	ws := request(http.MethodPost, "/v1/workspaces", owner.token, tenant, map[string]any{"name": "Recovery", "slug": "recovery"}, nil, http.StatusCreated)
	workspace := communicationHTTPTestDecode[struct {
		ID model.ID `json:"id"`
	}](t, ws).ID
	channelResponse := request(http.MethodPost, "/v1/m/sessions/channels", owner.token, tenant, map[string]any{
		"workspace_id": workspace, "slug": "recovery", "name": "Recovery", "content_protection": "application_sealed",
		"initial_grants": []map[string]any{
			channelCatalogGrant(channelCatalogSubject("user", owner.id), true, true, true),
			channelCatalogGrant(channelCatalogSubject("user", reader.id), true, false, false),
		},
	}, nil, http.StatusCreated)
	channel := communicationHTTPTestDecode[sessions.ChannelMutationResult](t, channelResponse)
	var deliveries []model.ID
	for i := 0; i < 2; i++ {
		sent := request(http.MethodPost, "/v1/m/sessions/messages/send", owner.token, tenant, map[string]any{
			"channel_id": channel.Channel.ID, "recipient": map[string]any{"kind": "user", "ref": reader.id},
			"content": map[string]any{"subject": "Recoverable notice", "blocks": []map[string]any{{"type": "text", "format": "plain", "text": fmt.Sprintf("saved-private-content-%d", i)}}},
		}, map[string]string{"Idempotency-Key": model.NewID().String()}, http.StatusCreated)
		result := communicationHTTPTestDecode[sessions.DirectNoticePublishResult](t, sent)
		deliveries = append(deliveries, result.DeliveryID)
		assertCommunicationStoredPayload(t, eng, tenant, result.MessageID, "sealed_v1", fmt.Sprintf("saved-private-content-%d", i))
	}
	first := request(http.MethodGet, communicationHTTPTestInboxPath(workspace, 1, ""), reader.token, tenant, nil, nil, http.StatusOK)
	page := communicationHTTPTestDecode[sessions.DirectNoticeInboxPage](t, first)
	if !page.HasMore || page.Continuation == "" || len(page.Items) != 1 {
		t.Fatal("expected a signed continuation for the second saved notice")
	}
	if err := eng.signer.CheckpointAll(context.Background(), eng.store); err != nil {
		t.Fatal(err)
	}
	pass := filepath.Join(t.TempDir(), "passphrase")
	if err := os.WriteFile(pass, []byte("test recovery passphrase"), 0o600); err != nil {
		t.Fatal(err)
	}
	bundle := filepath.Join(t.TempDir(), "backup.drbundle")
	if console {
		if err := eng.api.RunStartupBackup(context.Background(), "test recovery passphrase", "", "test-recovery"); err != nil {
			t.Fatal(err)
		}
		paths, err := filepath.Glob(filepath.Join(estate.dataDir, "backups", "*.drbundle"))
		if err != nil || len(paths) != 1 {
			t.Fatalf("console bundle count %d: %v", len(paths), err)
		}
		if err := dr.CopyFile(paths[0], bundle); err != nil {
			t.Fatal(err)
		}
	}
	if err := eng.Close(); err != nil {
		t.Fatal(err)
	}
	if !console {
		if out, err := runDR("backup", "--data-dir", estate.dataDir, "--engine", "sqlite", "--out", bundle, "--passphrase-file", pass); err != nil {
			t.Fatalf("backup: %v\n%s", err, out)
		}
	}

	if err := os.RemoveAll(estate.dataDir); err != nil {
		t.Fatal(err)
	}
	estate.dataDir = t.TempDir()
	if out, err := runDR("restore", "--in", bundle, "--data-dir", estate.dataDir, "--engine", "sqlite", "--passphrase-file", pass); err != nil {
		t.Fatalf("restore: %v\n%s", err, out)
	} else if !strings.Contains(out, "key custody intact") {
		// The console and CLI bundles both record the sealer probes this line checks.
		t.Fatalf("restore did not confirm the sealer keys (console=%v):\n%s", console, out)
	}
	eng = bootEstate()
	reader = loginCommunicationHTTPTestUser(t, eng, reader.id, "reader@recovery.test")
	for i, id := range deliveries {
		r := request(http.MethodGet, "/v1/m/sessions/deliveries/"+id.String(), reader.token, tenant, nil, nil, http.StatusOK)
		if !strings.Contains(string(r.raw), fmt.Sprintf("saved-private-content-%d", i)) {
			t.Fatal("restored delivery lost its content")
		}
	}
	next := request(http.MethodGet, communicationHTTPTestInboxPath(workspace, 1, page.Continuation), reader.token, tenant, nil, nil, http.StatusOK)
	restoredPage := communicationHTTPTestDecode[sessions.DirectNoticeInboxPage](t, next)
	if len(restoredPage.Items) != 1 || restoredPage.HasMore || restoredPage.Items[0].Delivery.ID == page.Items[0].Delivery.ID {
		t.Fatal("pre-backup cursor did not resume the restored inbox")
	}
}

func TestDRCommunicationCustodyRejectsIncompleteOrInvalidKeys(t *testing.T) {
	for _, damage := range []string{"missing-content", "missing-cursor", "invalid-content", "invalid-cursor", "permissions"} {
		t.Run(damage, func(t *testing.T) {
			dir := t.TempDir()
			content := filepath.Join(dir, localCommunicationContentKeyFile)
			cursor := filepath.Join(dir, localCommunicationCursorKeyFile)
			if err := createLocalCommunicationKeyring(content, true); err != nil {
				t.Fatal(err)
			}
			if err := createLocalCommunicationKeyring(cursor, false); err != nil {
				t.Fatal(err)
			}
			var err error
			switch damage {
			case "missing-content":
				err = os.Remove(content)
			case "missing-cursor":
				err = os.Remove(cursor)
			case "invalid-content":
				err = os.WriteFile(content, []byte(`{"format":"invalid"}`), 0o600)
			case "invalid-cursor":
				err = os.WriteFile(cursor, []byte(`{"format":"invalid"}`), 0o600)
			case "permissions":
				err = os.Chmod(content, 0o644)
			}
			if err != nil {
				t.Fatal(err)
			}
			cipher, err := dr.NewPassphraseCipher([]byte("test recovery passphrase"))
			if err != nil {
				t.Fatal(err)
			}
			sealed, refs, _, err := sealSigningKeys(dir, cipher)
			if err == nil || sealed != nil || refs != nil {
				t.Fatalf("damaged custody accepted: %v", err)
			}
		})
	}
}
