// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	voiceconn "github.com/olivaresai/olivares/connectors/voice"
	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/audit"
	"github.com/olivaresai/olivares/core/auth"
	coreengine "github.com/olivaresai/olivares/core/engine"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/secure"
	"github.com/olivaresai/olivares/core/store"
	voicemod "github.com/olivaresai/olivares/modules/voice"
)

func TestVoiceWebhookMountedOnlyWithSecret(t *testing.T) {
	log := discardLogger()
	eng := testVoiceWebhookEngine(t)
	bindVoiceWebhookModule(voicemod.New())
	t.Cleanup(func() { bindVoiceWebhookModule(nil) })

	t.Setenv(envVoiceCallConfig, "")
	if srv, err := buildVoiceWebhookServer(eng, true, log); err != nil || srv != nil {
		t.Fatal("webhook server must not mount with no config")
	}

	noSecret := filepath.Join(t.TempDir(), "voice-call.json")
	if err := os.WriteFile(noSecret, []byte(`{"tenant":"`+model.NewTenantID().String()+`"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv(envVoiceCallConfig, noSecret)
	if srv, err := buildVoiceWebhookServer(eng, true, log); err != nil || srv != nil {
		t.Fatal("webhook server must not mount without webhook_secret")
	}

	withSecret := filepath.Join(t.TempDir(), "voice-call.json")
	if err := os.WriteFile(withSecret, []byte(`{"webhook_secret":"whsec_dGVzdA","tenant":"`+model.NewTenantID().String()+`","project_ref":"proj_123"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv(envVoiceCallConfig, withSecret)
	srv, err := buildVoiceWebhookServer(eng, true, log)
	if err != nil {
		t.Fatalf("build webhook server: %v", err)
	}
	if srv == nil || srv.Handler == nil {
		t.Fatal("webhook server must mount when webhook_secret and tenant are present")
	}
}

func TestVoiceTranscriptClassifierAdapter(t *testing.T) {
	hits, err := voiceTranscriptClassifier{}.Classify("card 4111 1111 1111 1111")
	if err != nil {
		t.Fatal(err)
	}
	for _, h := range hits {
		if h.Class == "pii.financial" && h.Rule == "credit-card" && h.Count > 0 {
			return
		}
	}
	t.Fatalf("classifier did not pass text through to security catalog: %+v", hits)
}

func TestVoiceCallControllerAdapterUsesCallClient(t *testing.T) {
	doer := &voiceStubDoer{}
	ctrl := voiceCallController{client: voiceconn.CallClient{APIKey: "sk-test", BaseURL: "https://api.test", Transport: doer}}
	if err := ctrl.Accept(context.Background(), "call_123", voicemod.CallAccept{Model: "gpt-realtime-2", Instructions: "stay governed"}); err != nil {
		t.Fatalf("Accept: %v", err)
	}
	if doer.lastReq == nil {
		t.Fatal("transport was not called")
	}
	if got := doer.lastReq.Method; got != http.MethodPost {
		t.Fatalf("method = %s", got)
	}
	if got := doer.lastReq.URL.Path; got != "/v1/realtime/calls/call_123/accept" {
		t.Fatalf("path = %s", got)
	}
	if got := doer.lastReq.Header.Get("Authorization"); got != "Bearer sk-test" {
		t.Fatalf("auth header = %q", got)
	}
	var body map[string]any
	if err := json.Unmarshal(doer.lastBody, &body); err != nil {
		t.Fatalf("accept body is not JSON: %s", doer.lastBody)
	}
	if body["model"] != "gpt-realtime-2" || body["instructions"] != "stay governed" || body["type"] != "realtime" {
		t.Fatalf("accept body = %s", doer.lastBody)
	}

	doer.lastReq, doer.lastBody = nil, nil
	if err := ctrl.Reject(context.Background(), "call_123", 603); err != nil {
		t.Fatalf("Reject: %v", err)
	}
	if !strings.HasSuffix(doer.lastReq.URL.Path, "/reject") || !bytes.Contains(doer.lastBody, []byte(`"status_code":603`)) {
		t.Fatalf("reject request path/body = %s %s", doer.lastReq.URL.Path, doer.lastBody)
	}
}

func testVoiceWebhookEngine(t *testing.T) *engine {
	t.Helper()
	ctx := context.Background()
	st, err := coreengine.Open(ctx, store.Config{Engine: store.EngineSQLite, DSN: ":memory:"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	_, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := audit.NewSigner(priv)
	if err != nil {
		t.Fatal(err)
	}
	srv, err := api.New(api.Options{
		Store: st, Authenticator: auth.NewAuthenticator(st, nil), Authorizer: auth.NewAuthorizer(nil),
		Signer: signer, SetupToken: secure.NewSetupToken(filepath.Join(t.TempDir(), "setup.token")),
		Logger: discardLogger(), Version: "test",
	})
	if err != nil {
		t.Fatal(err)
	}
	return &engine{store: st, api: srv, log: discardLogger()}
}
