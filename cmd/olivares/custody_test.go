// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/core/secure"
)

// fakeKEKServer is an httptest AWS-KMS-shaped KEK: Encrypt/Decrypt with a
// reversible in-memory wrap that ENFORCES EncryptionContext equality (like the
// real service), plus a `revoked` switch that refuses every call — the CMEK
// revocation drill. The custody code reaches it through the standard
// AWS_ENDPOINT_URL_KMS override; nothing test-specific leaks into custody.go.
type fakeKEKServer struct {
	*httptest.Server
	revoked bool
}

func startFakeKEKServer(t *testing.T) *fakeKEKServer {
	t.Helper()
	f := &fakeKEKServer{}
	f.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if f.revoked {
			http.Error(w, `{"__type":"DisabledException"}`, http.StatusBadRequest)
			return
		}
		var in struct {
			KeyID             string            `json:"KeyId"`
			Plaintext         string            `json:"Plaintext"`
			CiphertextBlob    string            `json:"CiphertextBlob"`
			EncryptionContext map[string]string `json:"EncryptionContext"`
		}
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			http.Error(w, "bad body", http.StatusBadRequest)
			return
		}
		ctxCanon := fmt.Sprintf("%v", in.EncryptionContext)
		switch r.Header.Get("X-Amz-Target") {
		case "TrentService.Encrypt":
			pt, _ := base64.StdEncoding.DecodeString(in.Plaintext)
			blob := base64.StdEncoding.EncodeToString([]byte(ctxCanon + "|" + string(pt)))
			// Like the real service: report the RESOLVED key ARN (the alias
			// resolves), which the envelope records and unwrap then pins.
			_ = json.NewEncoder(w).Encode(map[string]string{
				"CiphertextBlob": blob,
				"KeyId":          "arn:aws:kms:eu-west-1:111:key/test-resolved",
			})
		case "TrentService.Decrypt":
			ct, _ := base64.StdEncoding.DecodeString(in.CiphertextBlob)
			want := ctxCanon + "|"
			if !strings.HasPrefix(string(ct), want) {
				http.Error(w, `{"__type":"InvalidCiphertextException"}`, http.StatusBadRequest)
				return
			}
			pt := strings.TrimPrefix(string(ct), want)
			_ = json.NewEncoder(w).Encode(map[string]string{"Plaintext": base64.StdEncoding.EncodeToString([]byte(pt))})
		default:
			http.Error(w, "unknown target", http.StatusBadRequest)
		}
	}))
	t.Cleanup(f.Server.Close)
	t.Cleanup(resetSealedConfigFailure) // the global custody flag must not leak between tests
	t.Setenv(envKeyWrap, "aws-kms")
	t.Setenv("OLIVARES_KEY_WRAP_AWS_REGION", "eu-west-1")
	t.Setenv("OLIVARES_KEY_WRAP_AWS_KEY_ID", "alias/test-kek")
	t.Setenv("AWS_ACCESS_KEY_ID", "AKIA-test")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "secret-test")
	t.Setenv("AWS_ENDPOINT_URL_KMS", f.Server.URL)
	return f
}

func TestLoadKeyWrapConfigValidation(t *testing.T) {
	t.Setenv(envKeyWrap, "")
	if cfg, err := loadKeyWrapConfig(); cfg != nil || err != nil {
		t.Fatalf("unconfigured = (%v, %v), want (nil, nil)", cfg, err)
	}
	t.Setenv(envKeyWrap, "hsm-magic")
	if _, err := loadKeyWrapConfig(); err == nil {
		t.Fatal("unknown kind accepted — a custody typo must never mean 'no custody'")
	}
	t.Setenv(envKeyWrap, "aws-kms")
	t.Setenv("OLIVARES_KEY_WRAP_AWS_REGION", "")
	t.Setenv("OLIVARES_KEY_WRAP_AWS_KEY_ID", "")
	if _, err := loadKeyWrapConfig(); err == nil {
		t.Fatal("aws-kms without region/key accepted")
	}
	t.Setenv(envKeyWrap, "gcp-kms")
	t.Setenv("OLIVARES_KEY_WRAP_GCP_KEY", "projects/p/locations/l/keyRings/r/cryptoKeys/k")
	if _, err := loadKeyWrapConfig(); err == nil {
		t.Fatal("gcp-kms without a token source accepted")
	}
	t.Setenv("OLIVARES_KEY_WRAP_GCP_TOKEN", "tok")
	if cfg, err := loadKeyWrapConfig(); err != nil || cfg.kind != "gcp-kms" {
		t.Fatalf("gcp-kms config = (%+v, %v)", cfg, err)
	}
	t.Setenv(envKeyWrap, "azure-kv")
	t.Setenv("OLIVARES_KEY_WRAP_AZURE_TOKEN", "tok")
	t.Setenv("OLIVARES_KEY_WRAP_AZURE_VAULT_URL", "")
	if _, err := loadKeyWrapConfig(); err == nil {
		t.Fatal("azure-kv without vault/key accepted")
	}
}

func TestCustodyAssertions(t *testing.T) {
	t.Setenv(envKeyCustody, "byok")
	t.Setenv(envLedgerCustody, "hyok")
	a, err := loadCustodyAssertions()
	if err != nil {
		t.Fatal(err)
	}
	// Declared BYOK satisfied by either source, refused for minted/CMEK.
	if err := a.verify(custodyModeBYOKFile, true); err != nil {
		t.Fatalf("byok+file should pass: %v", err)
	}
	if err := a.verify(custodyModeMinted, true); err == nil {
		t.Fatal("declared byok accepted a minted key")
	}
	if err := a.verify(custodyModeCMEK, true); err == nil {
		t.Fatal("declared byok accepted a cmek key (declare cmek instead)")
	}
	// Declared HYOK requires the off-box checkpoint signer.
	if err := a.verify(custodyModeBYOKFile, false); err == nil {
		t.Fatal("declared hyok accepted on-box checkpoints")
	}

	t.Setenv(envKeyCustody, "cmek")
	t.Setenv(envLedgerCustody, "")
	a, err = loadCustodyAssertions()
	if err != nil {
		t.Fatal(err)
	}
	if err := a.verify(custodyModeCMEK, false); err != nil {
		t.Fatalf("cmek+cmek should pass: %v", err)
	}
	if err := a.verify(custodyModeBYOKEnv, false); err == nil {
		t.Fatal("declared cmek accepted a byok key")
	}

	t.Setenv(envKeyCustody, "fort-knox")
	if _, err := loadCustodyAssertions(); err == nil {
		t.Fatal("unknown custody assertion accepted")
	}
}

func TestReadOperatorConfigSealed(t *testing.T) {
	startFakeKEKServer(t)
	dir := t.TempDir()
	plain := []byte(`{"sources":[{"kind":"vault","config":{"token":"hvs.secret"}}]}`)

	// Plaintext configs pass through byte-identical (sealing is opt-in).
	plainPath := filepath.Join(dir, "sources.json")
	if err := os.WriteFile(plainPath, plain, 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := readOperatorConfig(plainPath)
	if err != nil || string(got) != string(plain) {
		t.Fatalf("plaintext passthrough = (%q, %v)", got, err)
	}

	// Sealed config: seal through the configured KEK, read back transparently.
	cfg, err := loadKeyWrapConfig()
	if err != nil {
		t.Fatal(err)
	}
	w, err := cfg.wrapper()
	if err != nil {
		t.Fatal(err)
	}
	env, err := secure.Seal(context.Background(), w, secure.PurposeOperatorConfig, plain)
	if err != nil {
		t.Fatal(err)
	}
	sealedPath := filepath.Join(dir, "sources.sealed.json")
	if err := secure.WriteSealedFile(sealedPath, env); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(sealedPath)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "hvs.secret") {
		t.Fatal("sealed config leaked the secret in clear")
	}
	got, err = readOperatorConfig(sealedPath)
	if err != nil || string(got) != string(plain) {
		t.Fatalf("sealed read = (%q, %v)", got, err)
	}

	// The envelope records the RESOLVED ARN reported by Encrypt, not the
	// configured alias — what makes alias-repoint rotation survivable.
	if env.KeyID != "arn:aws:kms:eu-west-1:111:key/test-resolved" {
		t.Fatalf("envelope key_id = %q, want the resolved ARN", env.KeyID)
	}
	// A successful sealed read records no custody failure.
	if scErr := sealedConfigFailure(); scErr != nil {
		t.Fatalf("unexpected custody failure after a good read: %v", scErr)
	}

	// Sealed config without a KEK configured is a hard error AND a recorded
	// custody failure — the boot's deferred fail-closed check trips on it.
	t.Setenv(envKeyWrap, "")
	if _, err := readOperatorConfig(sealedPath); err == nil {
		t.Fatal("sealed config without a KEK must error")
	}
	if scErr := sealedConfigFailure(); scErr == nil {
		t.Fatal("an unopenable sealed config must record a custody failure (boot fails closed on it)")
	}
	resetSealedConfigFailure()
}
