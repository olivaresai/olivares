// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package kmswrap_test

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/core/secure"
	"github.com/olivaresai/olivares/core/secure/kmswrap"
)

// fakeKEK is an injectable Doer that answers each provider's exact wire format
// with a reversible in-memory "wrap": ciphertext = marker ‖ AAD ‖ plaintext. It
// enforces the provider's real AAD semantics — AWS EncryptionContext and GCP
// additionalAuthenticatedData must MATCH between wrap and unwrap; Azure has no
// AAD channel — so the envelope-layer binding tests run against honest fakes.
type fakeKEK struct {
	t        *testing.T
	provider string // "aws" | "gcp" | "azure"
	sawAuth  bool
	gotPaths []string
}

const marker = "WRAPPED|"

func wrapBlob(aadCanon string, pt []byte) []byte {
	return []byte(marker + aadCanon + "|" + string(pt))
}

func unwrapBlob(aadCanon string, ct []byte) ([]byte, bool) {
	want := marker + aadCanon + "|"
	if !strings.HasPrefix(string(ct), want) {
		return nil, false
	}
	return []byte(strings.TrimPrefix(string(ct), want)), true
}

func jsonResp(t *testing.T, v map[string]any) *http.Response {
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return &http.Response{StatusCode: 200, Body: io.NopCloser(bytes.NewReader(b)), Header: http.Header{}}
}

func httpErr(code int) *http.Response {
	return &http.Response{StatusCode: code, Body: io.NopCloser(strings.NewReader("{}")), Header: http.Header{}}
}

func (f *fakeKEK) Do(req *http.Request) (*http.Response, error) {
	f.gotPaths = append(f.gotPaths, req.Method+" "+req.URL.Path)
	var body []byte
	if req.Body != nil {
		body, _ = io.ReadAll(req.Body)
	}
	switch f.provider {
	case "aws":
		if strings.HasPrefix(req.Header.Get("Authorization"), "AWS4-HMAC-SHA256 ") {
			f.sawAuth = true
		}
		var in struct {
			KeyID             string            `json:"KeyId"`
			Plaintext         string            `json:"Plaintext"`
			CiphertextBlob    string            `json:"CiphertextBlob"`
			EncryptionContext map[string]string `json:"EncryptionContext"`
		}
		if err := json.Unmarshal(body, &in); err != nil {
			f.t.Fatalf("aws body: %v", err)
		}
		aadCanon := fmt.Sprintf("%v", in.EncryptionContext)
		switch req.Header.Get("X-Amz-Target") {
		case "TrentService.Encrypt":
			if in.KeyID == "" {
				return httpErr(400), nil
			}
			pt, _ := base64.StdEncoding.DecodeString(in.Plaintext)
			// Like the real service: the response reports the RESOLVED key ARN
			// (an alias input resolves to the key it currently points at).
			return jsonResp(f.t, map[string]any{
				"CiphertextBlob": base64.StdEncoding.EncodeToString(wrapBlob(aadCanon, pt)),
				"KeyId":          "arn:aws:kms:eu-west-1:111:key/resolved-1",
			}), nil
		case "TrentService.Decrypt":
			if in.KeyID == "" {
				f.t.Fatal("aws Decrypt did not pin KeyId")
			}
			ct, _ := base64.StdEncoding.DecodeString(in.CiphertextBlob)
			pt, ok := unwrapBlob(aadCanon, ct)
			if !ok {
				return httpErr(400), nil // InvalidCiphertextException: context mismatch
			}
			return jsonResp(f.t, map[string]any{"Plaintext": base64.StdEncoding.EncodeToString(pt)}), nil
		}
		f.t.Fatalf("aws: unexpected target %q", req.Header.Get("X-Amz-Target"))
	case "gcp":
		if strings.HasPrefix(req.Header.Get("Authorization"), "Bearer ") {
			f.sawAuth = true
		}
		var in struct {
			Plaintext  string `json:"plaintext"`
			Ciphertext string `json:"ciphertext"`
			AAD        string `json:"additionalAuthenticatedData"`
		}
		if err := json.Unmarshal(body, &in); err != nil {
			f.t.Fatalf("gcp body: %v", err)
		}
		switch {
		case strings.HasSuffix(req.URL.Path, ":encrypt"):
			pt, _ := base64.StdEncoding.DecodeString(in.Plaintext)
			return jsonResp(f.t, map[string]any{"ciphertext": base64.StdEncoding.EncodeToString(wrapBlob(in.AAD, pt))}), nil
		case strings.HasSuffix(req.URL.Path, ":decrypt"):
			ct, _ := base64.StdEncoding.DecodeString(in.Ciphertext)
			pt, ok := unwrapBlob(in.AAD, ct)
			if !ok {
				return httpErr(400), nil
			}
			return jsonResp(f.t, map[string]any{"plaintext": base64.StdEncoding.EncodeToString(pt)}), nil
		}
		f.t.Fatalf("gcp: unexpected path %q", req.URL.Path)
	case "azure":
		if strings.HasPrefix(req.Header.Get("Authorization"), "Bearer ") {
			f.sawAuth = true
		}
		if req.Method == http.MethodGet {
			// resolveVersion: report the key's current version via its kid.
			return jsonResp(f.t, map[string]any{"key": map[string]any{"kid": "https://v.vault.azure.net/keys/kek/VER1"}}), nil
		}
		var in struct {
			Alg   string `json:"alg"`
			Value string `json:"value"`
		}
		if err := json.Unmarshal(body, &in); err != nil {
			f.t.Fatalf("azure body: %v", err)
		}
		if in.Alg != "RSA-OAEP-256" {
			f.t.Fatalf("azure alg = %q, want RSA-OAEP-256", in.Alg)
		}
		val, err := base64.RawURLEncoding.DecodeString(in.Value)
		if err != nil {
			f.t.Fatalf("azure value not base64url: %v", err)
		}
		switch {
		case strings.HasSuffix(req.URL.Path, "/wrapkey"):
			return jsonResp(f.t, map[string]any{"value": base64.RawURLEncoding.EncodeToString(wrapBlob("", val))}), nil
		case strings.HasSuffix(req.URL.Path, "/unwrapkey"):
			pt, ok := unwrapBlob("", val)
			if !ok {
				return httpErr(400), nil
			}
			return jsonResp(f.t, map[string]any{"value": base64.RawURLEncoding.EncodeToString(pt)}), nil
		}
		f.t.Fatalf("azure: unexpected path %q", req.URL.Path)
	}
	return nil, fmt.Errorf("unreachable")
}

// roundtrip runs the FULL custody path (secure.Seal → secure.Open) through a
// real backend speaking to the provider fake, proving the envelope layer and
// the wire layer compose.
func roundtrip(t *testing.T, w secure.KeyWrapper) {
	t.Helper()
	ctx := context.Background()
	payload := []byte("the payload")
	e, err := secure.Seal(ctx, w, secure.PurposeOperatorConfig, payload)
	if err != nil {
		t.Fatalf("Seal: %v", err)
	}
	got, err := e.Open(ctx, w, secure.PurposeOperatorConfig)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if !bytes.Equal(got, payload) {
		t.Fatalf("roundtrip mismatch: %q", got)
	}
}

func TestAWSWrapper(t *testing.T) {
	fake := &fakeKEK{t: t, provider: "aws"}
	w, err := kmswrap.NewAWS(kmswrap.AWSConfig{
		Region: "eu-west-1", KeyID: "arn:aws:kms:eu-west-1:111:key/abc",
		Creds: kmswrap.AWSCreds{AccessKeyID: "AKIA", SecretAccessKey: "secret"},
		Doer:  fake,
	})
	if err != nil {
		t.Fatal(err)
	}
	if w.Provider() != "aws-kms" {
		t.Fatalf("provider = %q", w.Provider())
	}
	roundtrip(t, w)
	if !fake.sawAuth {
		t.Fatal("AWS requests were not SigV4-signed")
	}
	// After the first wrap, KeyID reports the RESOLVED ARN from the Encrypt
	// response — what the envelope must record so an alias repoint (manual
	// rotation) cannot brick the unwrap of pre-rotation envelopes.
	if w.KeyID() != "arn:aws:kms:eu-west-1:111:key/resolved-1" {
		t.Fatalf("KeyID after wrap = %q, want the resolved ARN", w.KeyID())
	}

	// Context mismatch at the KMS (AWS authenticates EncryptionContext).
	ctx := context.Background()
	ct, err := w.WrapKey(ctx, bytes.Repeat([]byte{7}, 32), map[string]string{"olivares:purpose": "a"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.UnwrapKey(ctx, ct, map[string]string{"olivares:purpose": "b"}); err == nil {
		t.Fatal("AWS unwrap accepted a different EncryptionContext")
	}

	// Config validation.
	if _, err := kmswrap.NewAWS(kmswrap.AWSConfig{KeyID: "k"}); err == nil {
		t.Fatal("NewAWS accepted an empty region")
	}
	if _, err := kmswrap.NewAWS(kmswrap.AWSConfig{Region: "r"}); err == nil {
		t.Fatal("NewAWS accepted an empty key id")
	}
}

func TestGCPWrapper(t *testing.T) {
	fake := &fakeKEK{t: t, provider: "gcp"}
	w, err := kmswrap.NewGCP(kmswrap.GCPConfig{
		KeyName: "projects/p/locations/l/keyRings/r/cryptoKeys/k",
		Token:   kmswrap.StaticToken("tok"),
		Doer:    fake,
	})
	if err != nil {
		t.Fatal(err)
	}
	roundtrip(t, w)
	if !fake.sawAuth {
		t.Fatal("GCP requests carried no bearer token")
	}

	// AAD mismatch refused.
	ctx := context.Background()
	ct, err := w.WrapKey(ctx, bytes.Repeat([]byte{7}, 32), map[string]string{"olivares:purpose": "a"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.UnwrapKey(ctx, ct, map[string]string{"olivares:purpose": "b"}); err == nil {
		t.Fatal("GCP unwrap accepted different AAD")
	}

	// A cryptoKeyVersion path is a config error (decrypt is key-scoped).
	if _, err := kmswrap.NewGCP(kmswrap.GCPConfig{
		KeyName: "projects/p/locations/l/keyRings/r/cryptoKeys/k/cryptoKeyVersions/1",
		Token:   kmswrap.StaticToken("tok"),
	}); err == nil {
		t.Fatal("NewGCP accepted a cryptoKeyVersion resource")
	}
	if _, err := kmswrap.NewGCP(kmswrap.GCPConfig{KeyName: "projects/p/secrets/x", Token: kmswrap.StaticToken("t")}); err == nil {
		t.Fatal("NewGCP accepted a non-cryptoKeys resource")
	}
}

func TestAzureWrapper(t *testing.T) {
	fake := &fakeKEK{t: t, provider: "azure"}
	w, err := kmswrap.NewAzure(kmswrap.AzureConfig{
		VaultURL: "https://v.vault.azure.net", KeyName: "kek",
		Token: kmswrap.StaticToken("tok"), Doer: fake,
	})
	if err != nil {
		t.Fatal(err)
	}
	roundtrip(t, w)
	if !fake.sawAuth {
		t.Fatal("Azure requests carried no bearer token")
	}

	// With no configured version, the backend resolved and PINNED one: every
	// wrap/unwrap call must address /keys/kek/VER1, never the floating key URL.
	if w.KeyID() != "https://v.vault.azure.net/keys/kek/VER1" {
		t.Fatalf("KeyID did not pin the resolved version: %s", w.KeyID())
	}
	for _, p := range fake.gotPaths {
		if strings.Contains(p, "wrapkey") || strings.Contains(p, "unwrapkey") {
			if !strings.Contains(p, "/keys/kek/VER1/") {
				t.Fatalf("key op did not pin the version: %s", p)
			}
		}
	}

	// An explicitly configured version is used as-is (no resolution GET).
	fake2 := &fakeKEK{t: t, provider: "azure"}
	w2, err := kmswrap.NewAzure(kmswrap.AzureConfig{
		VaultURL: "https://v.vault.azure.net", KeyName: "kek", KeyVersion: "PINNED",
		Token: kmswrap.StaticToken("tok"), Doer: fake2,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w2.WrapKey(context.Background(), bytes.Repeat([]byte{7}, 32), nil); err != nil {
		t.Fatal(err)
	}
	for _, p := range fake2.gotPaths {
		if strings.HasPrefix(p, "GET ") {
			t.Fatalf("configured version still triggered a resolution GET: %s", p)
		}
	}
}

func TestParseAzureKeyID(t *testing.T) {
	v, n, ver, err := kmswrap.ParseAzureKeyID("https://v.vault.azure.net/keys/kek/VER1")
	if err != nil || v != "https://v.vault.azure.net" || n != "kek" || ver != "VER1" {
		t.Fatalf("got %q %q %q %v", v, n, ver, err)
	}
	v, n, ver, err = kmswrap.ParseAzureKeyID("https://v.vault.azure.net/keys/kek")
	if err != nil || n != "kek" || ver != "" {
		t.Fatalf("versionless: %q %q %q %v", v, n, ver, err)
	}
	if _, _, _, err := kmswrap.ParseAzureKeyID("https://v.vault.azure.net/secrets/x"); err == nil {
		t.Fatal("accepted a non-key identifier")
	}
}
