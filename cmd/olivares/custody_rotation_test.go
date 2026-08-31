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
	"strings"
	"testing"

	"github.com/olivaresai/olivares/core/secure"
	"github.com/olivaresai/olivares/core/secure/kmswrap"
)

// (release gate S-5, U1): the custody question a customer actually asks
// after rotating their KEK — "do my PREVIOUSLY WRAPPED keys still open?".
//
// The answer lives entirely in keyWrapConfig.wrapperFor (custody.go), which pins
// what the ENVELOPE recorded over what the environment configures. It is
// load-bearing on three production paths — the audit signing key load
// (auditkey.go), every sealed operator config (custody.go openSealedEnvelope)
// and `keys rewrap` (cmd_keys.go) — and before this file it had NO test.
//
// TestReadOperatorConfigSealed asserts the envelope RECORDS the resolved ARN,
// but its fake KMS ignores KeyId on Decrypt, so nothing could observe whether
// unwrap USES it: the re-pin could have been deleted and every test stayed
// green. The fakes below add the one behavior that makes the re-pin
// observable — a KEK that refuses an unwrap addressed at the wrong key/version,
// which is what the real services do — and each case carries a CONTROL that
// fails without the re-pin, so a green here means the pin worked and not that
// the fixture is blind.

// --- AWS: alias repoint (the documented manual-rotation pattern) ------------

// fakeRotatingKMS models the two AWS KMS behaviors the re-pin exists for:
// an ALIAS resolves at CALL time, and Decrypt addressed at a key that did not
// wrap the blob fails with IncorrectKeyException. Repointing aliasTarget is the
// operator rotating their KEK.
type fakeRotatingKMS struct {
	*httptest.Server
	aliasTarget string
}

const (
	kekARNv1  = "arn:aws:kms:eu-west-1:111:key/kek-generation-1"
	kekARNv2  = "arn:aws:kms:eu-west-1:111:key/kek-generation-2"
	kekAlias  = "alias/olivares-kek"
	kekRegion = "eu-west-1"
)

func startRotatingKMS(t *testing.T) *fakeRotatingKMS {
	t.Helper()
	f := &fakeRotatingKMS{aliasTarget: kekARNv1}
	// resolve mirrors the service: an alias is late-bound, an ARN is itself.
	resolve := func(id string) string {
		if strings.HasPrefix(id, "alias/") {
			return f.aliasTarget
		}
		return id
	}
	f.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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
		// The blob carries the context and the ARN that actually wrapped it, so
		// Decrypt can enforce both exactly as the service does.
		ctxCanon := fmt.Sprintf("%v", in.EncryptionContext)
		switch r.Header.Get("X-Amz-Target") {
		case "TrentService.Encrypt":
			arn := resolve(in.KeyID)
			pt, _ := base64.StdEncoding.DecodeString(in.Plaintext)
			_ = json.NewEncoder(w).Encode(map[string]string{
				"CiphertextBlob": base64.StdEncoding.EncodeToString([]byte(ctxCanon + "|" + arn + "|" + string(pt))),
				"KeyId":          arn,
			})
		case "TrentService.Decrypt":
			raw, _ := base64.StdEncoding.DecodeString(in.CiphertextBlob)
			parts := strings.SplitN(string(raw), "|", 3)
			if len(parts) != 3 || parts[0] != ctxCanon {
				http.Error(w, `{"__type":"InvalidCiphertextException"}`, http.StatusBadRequest)
				return
			}
			if resolve(in.KeyID) != parts[1] {
				http.Error(w, `{"__type":"IncorrectKeyException"}`, http.StatusBadRequest)
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]string{"Plaintext": base64.StdEncoding.EncodeToString([]byte(parts[2]))})
		default:
			http.Error(w, "unknown target", http.StatusBadRequest)
		}
	}))
	t.Cleanup(f.Server.Close)
	t.Setenv(envKeyWrap, "aws-kms")
	t.Setenv("OLIVARES_KEY_WRAP_AWS_REGION", kekRegion)
	t.Setenv("OLIVARES_KEY_WRAP_AWS_KEY_ID", kekAlias)
	t.Setenv("AWS_ACCESS_KEY_ID", "AKIA-test")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "secret-test")
	t.Setenv("AWS_ENDPOINT_URL_KMS", f.Server.URL)
	return f
}

func TestAWSAliasRepointStillOpensPriorEnvelopes(t *testing.T) {
	ctx := context.Background()
	kms := startRotatingKMS(t)
	secretPayload := []byte("audit-signing-key-material")

	cfg, err := loadKeyWrapConfig()
	if err != nil {
		t.Fatalf("loadKeyWrapConfig: %v", err)
	}
	wrapW, err := cfg.wrapper()
	if err != nil {
		t.Fatalf("build wrap-side KEK: %v", err)
	}
	env, err := secure.Seal(ctx, wrapW, secure.PurposeAuditSigningKey, secretPayload)
	if err != nil {
		t.Fatalf("seal under generation-1 KEK: %v", err)
	}
	if env.KeyID != kekARNv1 {
		t.Fatalf("envelope recorded key_id %q, want the RESOLVED ARN %q — the re-pin has nothing to pin without it", env.KeyID, kekARNv1)
	}

	// The operator rotates: the alias now points at a brand-new key. Nothing on
	// disk changed; the envelope still holds generation-1's ARN.
	kms.aliasTarget = kekARNv2

	// CONTROL: without the re-pin (addressing the configured ALIAS) the unwrap
	// is refused by the KEK. This is what makes the assertion below meaningful —
	// if this control ever passes, the fixture stopped discriminating.
	aliasW, err := cfg.wrapper()
	if err != nil {
		t.Fatalf("build alias-addressed KEK: %v", err)
	}
	_, ctrlErr := env.Open(ctx, aliasW, secure.PurposeAuditSigningKey)
	if ctrlErr == nil {
		t.Fatal("CONTROL FAILED: the alias-addressed unwrap succeeded after the repoint, so this fixture cannot observe the re-pin at all")
	}
	if !strings.Contains(ctrlErr.Error(), "IncorrectKeyException") {
		t.Fatalf("CONTROL FAILED for the wrong reason: want IncorrectKeyException from the KEK, got %v", ctrlErr)
	}

	// The production unwrap path re-pins the recorded ARN, so the prior envelope
	// still opens after the rotation.
	got, err := openSealedEnvelope(ctx, cfg, env, secure.PurposeAuditSigningKey)
	if err != nil {
		t.Fatalf("a KEK alias repoint bricked a previously wrapped key: openSealedEnvelope: %v", err)
	}
	if string(got) != string(secretPayload) {
		t.Fatalf("unwrapped payload = %q, want %q", got, secretPayload)
	}
}

// --- Azure: Key Vault key rotation (a new key VERSION) ----------------------

// fakeRotatingVault models Key Vault wrapKey/unwrapKey with the behavior the
// re-pin exists for: unwrap addressed at a version other than the wrapping one
// fails, and the version-less GET reports whatever is CURRENT.
type fakeRotatingVault struct {
	*httptest.Server
	current string
}

const (
	vaultKeyName = "olivares-kek"
	vaultVerV1   = "1111111111111111111111111111111a"
	vaultVerV2   = "2222222222222222222222222222222b"
)

func startRotatingVault(t *testing.T) *fakeRotatingVault {
	t.Helper()
	f := &fakeRotatingVault{current: vaultVerV1}
	f.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
		// GET /keys/{name} -> the kid of the CURRENT version.
		if r.Method == http.MethodGet && len(parts) == 2 && parts[0] == "keys" {
			kid := f.Server.URL + "/keys/" + parts[1] + "/" + f.current
			_ = json.NewEncoder(w).Encode(map[string]any{"key": map[string]string{"kid": kid}})
			return
		}
		// POST /keys/{name}/{version}/{wrapkey|unwrapkey}
		if r.Method != http.MethodPost || len(parts) != 4 || parts[0] != "keys" {
			http.Error(w, `{"error":{"code":"BadRequest"}}`, http.StatusBadRequest)
			return
		}
		version, op := parts[2], parts[3]
		var in struct {
			Alg   string `json:"alg"`
			Value string `json:"value"`
		}
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			http.Error(w, `{"error":{"code":"BadRequest"}}`, http.StatusBadRequest)
			return
		}
		if in.Alg != "RSA-OAEP-256" {
			http.Error(w, `{"error":{"code":"BadParameter","message":"unexpected wrap algorithm"}}`, http.StatusBadRequest)
			return
		}
		raw, err := base64.RawURLEncoding.DecodeString(in.Value)
		if err != nil {
			http.Error(w, `{"error":{"code":"BadParameter"}}`, http.StatusBadRequest)
			return
		}
		switch op {
		case "wrapkey":
			out := base64.RawURLEncoding.EncodeToString([]byte(version + "|" + string(raw)))
			_ = json.NewEncoder(w).Encode(map[string]string{"value": out})
		case "unwrapkey":
			wrapped := strings.SplitN(string(raw), "|", 2)
			if len(wrapped) != 2 {
				http.Error(w, `{"error":{"code":"BadParameter"}}`, http.StatusBadRequest)
				return
			}
			// An RSA private key only reverses what its own public half wrapped.
			if wrapped[0] != version {
				http.Error(w, `{"error":{"code":"KeyOperationError","message":"the addressed key version did not wrap this value"}}`, http.StatusBadRequest)
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]string{"value": base64.RawURLEncoding.EncodeToString([]byte(wrapped[1]))})
		default:
			http.Error(w, `{"error":{"code":"BadRequest"}}`, http.StatusBadRequest)
		}
	}))
	t.Cleanup(f.Server.Close)
	t.Setenv(envKeyWrap, "azure-kv")
	t.Setenv("OLIVARES_KEY_WRAP_AZURE_VAULT_URL", f.Server.URL)
	t.Setenv("OLIVARES_KEY_WRAP_AZURE_KEY_NAME", vaultKeyName)
	t.Setenv("OLIVARES_KEY_WRAP_AZURE_KEY_VERSION", "")
	t.Setenv("OLIVARES_KEY_WRAP_AZURE_TOKEN", "test-bearer")
	return f
}

func TestAzureKeyRotationStillOpensPriorEnvelopes(t *testing.T) {
	ctx := context.Background()
	vault := startRotatingVault(t)
	secretPayload := []byte("catalog-signing-key-material")

	cfg, err := loadKeyWrapConfig()
	if err != nil {
		t.Fatalf("loadKeyWrapConfig: %v", err)
	}
	wrapW, err := cfg.wrapper()
	if err != nil {
		t.Fatalf("build wrap-side KEK: %v", err)
	}
	env, err := secure.Seal(ctx, wrapW, secure.PurposeCatalogSigningKey, secretPayload)
	if err != nil {
		t.Fatalf("seal under version v1: %v", err)
	}
	wantKID := vault.Server.URL + "/keys/" + vaultKeyName + "/" + vaultVerV1
	if env.KeyID != wantKID {
		t.Fatalf("envelope recorded key_id %q, want the pinned wrapping version %q — the re-pin has nothing to pin without it", env.KeyID, wantKID)
	}

	// The operator rotates the Key Vault key: a new version becomes current.
	vault.current = vaultVerV2

	// CONTROL: a fresh wrapper with no version configured resolves the CURRENT
	// version and is refused by the vault. Without this the assertion below
	// could pass on a fixture that ignores versions entirely.
	floatingW, err := cfg.wrapper()
	if err != nil {
		t.Fatalf("build version-floating KEK: %v", err)
	}
	_, ctrlErr := env.Open(ctx, floatingW, secure.PurposeCatalogSigningKey)
	if ctrlErr == nil {
		t.Fatal("CONTROL FAILED: an unwrap addressed at the post-rotation version succeeded, so this fixture cannot observe the re-pin at all")
	}
	if !strings.Contains(ctrlErr.Error(), "KeyOperationError") {
		t.Fatalf("CONTROL FAILED for the wrong reason: want KeyOperationError from the vault, got %v", ctrlErr)
	}

	// The production unwrap path re-pins the recorded version.
	got, err := openSealedEnvelope(ctx, cfg, env, secure.PurposeCatalogSigningKey)
	if err != nil {
		t.Fatalf("a Key Vault key rotation bricked a previously wrapped key: openSealedEnvelope: %v", err)
	}
	if string(got) != string(secretPayload) {
		t.Fatalf("unwrapped payload = %q, want %q", got, secretPayload)
	}

	// The recorded version must win even over an explicitly CONFIGURED one —
	// custody.go documents this as what keeps `keys rewrap` from opening the
	// envelope at the post-rotation version and bricking the ceremony.
	t.Setenv("OLIVARES_KEY_WRAP_AZURE_KEY_VERSION", vaultVerV2)
	cfgPinnedForward, err := loadKeyWrapConfig()
	if err != nil {
		t.Fatalf("reload config with a forward-pinned version: %v", err)
	}
	if _, err := openSealedEnvelope(ctx, cfgPinnedForward, env, secure.PurposeCatalogSigningKey); err != nil {
		t.Fatalf("a configured post-rotation version overrode the envelope's recorded one, which bricks `keys rewrap`: %v", err)
	}
}

// TestSealedEnvelopeFromAnotherVaultIsRefused covers the other half of
// wrapperFor's Azure branch: a recorded key id that does NOT reference the
// configured vault/key is a custody mismatch reported by name, never silently
// attempted against the wrong KEK.
func TestSealedEnvelopeFromAnotherVaultIsRefused(t *testing.T) {
	ctx := context.Background()
	vault := startRotatingVault(t)

	cfg, err := loadKeyWrapConfig()
	if err != nil {
		t.Fatalf("loadKeyWrapConfig: %v", err)
	}
	w, err := cfg.wrapper()
	if err != nil {
		t.Fatalf("build KEK: %v", err)
	}
	env, err := secure.Seal(ctx, w, secure.PurposeOperatorConfig, []byte(`{"ok":true}`))
	if err != nil {
		t.Fatalf("seal: %v", err)
	}
	// Someone points the engine at a DIFFERENT vault than the one that wrapped it.
	env.KeyID = "https://someone-elses.vault.azure.net/keys/" + vaultKeyName + "/" + vaultVerV1
	_, err = openSealedEnvelope(ctx, cfg, env, secure.PurposeOperatorConfig)
	if err == nil {
		t.Fatal("an envelope recorded against a foreign vault was opened against the configured KEK")
	}
	if !strings.Contains(err.Error(), "someone-elses.vault.azure.net") || !strings.Contains(err.Error(), "keys rewrap") {
		t.Fatalf("the custody mismatch must name the foreign KEK and the remedy, got: %v", err)
	}
	_ = vault
}

// TestWrapperForCoversTheShapesItsContractGeneralizes closes finding F-03 of the
// adversarial contrast run against this work on 2026-08-06 (MEDIUM).
//
// wrapperFor's doc comment states two things without qualification: what the
// envelope RECORDS wins over what the environment configures, and an Azure id
// pointing at another vault/key is "a custody mismatch reported loudly, not
// silently tried". The predicates underneath were narrower than both sentences,
// and my rotation tests above used only the shapes that happen to work — a
// resolved AWS ARN and a versioned Azure id — so neither gap was observable.
func TestWrapperForCoversTheShapesItsContractGeneralizes(t *testing.T) {
	t.Run("a recorded bare AWS key id still wins over a configured alias", func(t *testing.T) {
		startRotatingKMS(t) // configures OLIVARES_KEY_WRAP_AWS_KEY_ID=alias/olivares-kek
		cfg, err := loadKeyWrapConfig()
		if err != nil {
			t.Fatalf("loadKeyWrapConfig: %v", err)
		}
		// AWS accepts "key id, key ARN or alias", so a bare key id is a legitimate
		// recorded identity — and it is a CONCRETE key, unlike the alias, which is
		// late-bound and moves when the operator rotates.
		const bareKeyID = "12345678-1234-1234-1234-123456789012"
		w, err := cfg.wrapperFor(&secure.SealedEnvelope{Provider: kmswrap.ProviderAWS, KeyID: bareKeyID})
		if err != nil {
			t.Fatalf("wrapperFor: %v", err)
		}
		if got := w.KeyID(); got != bareKeyID {
			t.Fatalf("unwrap targets %q but the envelope recorded %q: after an alias repoint this opens the WRONG key, which is the whole failure the re-pin exists to prevent", got, bareKeyID)
		}
	})

	t.Run("a versionless foreign-vault envelope is refused, not quietly retargeted", func(t *testing.T) {
		startRotatingVault(t)
		cfg, err := loadKeyWrapConfig()
		if err != nil {
			t.Fatalf("loadKeyWrapConfig: %v", err)
		}
		// ParseAzureKeyID models an empty version as VALID, so this is a shape the
		// code must answer for, not a malformed input.
		foreign := "https://someone-elses.vault.azure.net/keys/" + vaultKeyName
		w, err := cfg.wrapperFor(&secure.SealedEnvelope{Provider: kmswrap.ProviderAzure, KeyID: foreign})
		if err == nil {
			t.Fatalf("an envelope recorded against %s was answered with a wrapper for %q instead of a custody mismatch", foreign, w.KeyID())
		}
		if !strings.Contains(err.Error(), "someone-elses.vault.azure.net") {
			t.Fatalf("the mismatch must name the foreign KEK, got: %v", err)
		}
	})

	t.Run("an alias ARN is an alias too, and must not be pinned", func(t *testing.T) {
		startRotatingKMS(t)
		cfg, err := loadKeyWrapConfig()
		if err != nil {
			t.Fatalf("loadKeyWrapConfig: %v", err)
		}
		// KMS accepts an alias ARN wherever it accepts an alias name, and this form
		// does NOT start with "alias/" — so a prefix-only test reads it as a
		// concrete key and pins a late-bound name, which is the failure the re-pin
		// exists to prevent.
		const aliasARN = "arn:aws:kms:eu-west-1:111:alias/olivares-kek"
		w, err := cfg.wrapperFor(&secure.SealedEnvelope{Provider: kmswrap.ProviderAWS, KeyID: aliasARN})
		if err != nil {
			t.Fatalf("wrapperFor: %v", err)
		}
		if got := w.KeyID(); got == aliasARN {
			t.Fatalf("unwrap pinned the alias ARN %q: an alias resolves at call time, so pinning it re-introduces exactly the repoint failure the concrete-key pin exists to avoid", aliasARN)
		}
	})

	t.Run("an alias recorded by an unhelpful KMS is not mistaken for a concrete key", func(t *testing.T) {
		startRotatingKMS(t)
		cfg, err := loadKeyWrapConfig()
		if err != nil {
			t.Fatalf("loadKeyWrapConfig: %v", err)
		}
		// If Encrypt returned no KeyId, the envelope records the configured alias.
		// Re-pinning THAT would pin a late-bound name and defeat the purpose, so it
		// must fall through to the configured wrapper unchanged.
		w, err := cfg.wrapperFor(&secure.SealedEnvelope{Provider: kmswrap.ProviderAWS, KeyID: kekAlias})
		if err != nil {
			t.Fatalf("wrapperFor: %v", err)
		}
		if got := w.KeyID(); got != kekAlias {
			t.Fatalf("wrapper targets %q, want the configured alias %q", got, kekAlias)
		}
	})
}
