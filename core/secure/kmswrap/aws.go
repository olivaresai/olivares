// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package kmswrap

import (
	"context"
	"encoding/base64"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/olivaresai/olivares/core/internal/sigv4"
)

// AWS is a KEK wrapper backed by AWS KMS Encrypt/Decrypt on a symmetric KMS key
// (SYMMETRIC_DEFAULT). The wrap context travels as EncryptionContext, which KMS
// authenticates: a ciphertext wrapped under one context can never be decrypted
// under another, so the envelope's purpose binding holds at the KMS itself.
// Pure-Go (SigV4 via core/internal/sigv4, AWS JSON 1.1).
//
// Verified against docs.aws.amazon.com (KMS Encrypt / Decrypt API reference):
// endpoint kms.<region>.amazonaws.com, X-Amz-Target TrentService.Encrypt
// {KeyId, Plaintext(base64), EncryptionContext} → {CiphertextBlob(base64)};
// TrentService.Decrypt {CiphertextBlob(base64), KeyId, EncryptionContext} →
// {Plaintext(base64)}. KeyId on Decrypt is optional for a symmetric key (the
// blob self-describes) but passing it is the documented best practice: it pins
// the unwrap to the configured KEK.
type AWS struct {
	region   string
	keyID    string
	creds    sigv4.Credentials
	doer     Doer
	now      func() time.Time
	endpoint string // override (tests / KMS-compatible endpoints); default kms.<region>.amazonaws.com

	// resolvedARN is the key ARN the Encrypt response reported (cached after the
	// first wrap). It matters when keyID is an ALIAS: the envelope must record
	// the key that ACTUALLY wrapped the DEK, because Decrypt pinned to an alias
	// resolves at call time — after the operator repoints the alias (the
	// AWS-documented manual-rotation pattern), an alias-pinned unwrap throws
	// IncorrectKeyException even though the old key still decrypts the blob.
	resolvedARN string
}

// AWSCreds is the minimal credential triple a SigV4 signer needs. It aliases
// the shared core signer (core/internal/sigv4) — the same audited
// implementation the ledger signer uses — so callers outside the core module
// (the composition root) can name it.
type AWSCreds = sigv4.Credentials

// AWSConfig configures an AWS KMS KEK wrapper.
type AWSConfig struct {
	Region string
	KeyID  string // key id, key ARN or alias
	Creds  AWSCreds
	// Doer overrides the HTTP client (tests); nil uses http.DefaultClient.
	Doer Doer
	// Endpoint overrides the host (tests / KMS-compatible endpoints); empty uses
	// kms.<region>.amazonaws.com.
	Endpoint string
	// Now overrides the clock (tests); nil uses time.Now.
	Now func() time.Time
}

// NewAWS builds an AWS KMS KEK wrapper.
func NewAWS(cfg AWSConfig) (*AWS, error) {
	if strings.TrimSpace(cfg.Region) == "" {
		return nil, fmt.Errorf("kmswrap: aws region is required")
	}
	if strings.TrimSpace(cfg.KeyID) == "" {
		return nil, fmt.Errorf("kmswrap: aws key id is required")
	}
	doer := cfg.Doer
	if doer == nil {
		doer = http.DefaultClient
	}
	endpoint := cfg.Endpoint
	if endpoint == "" {
		endpoint = "https://kms." + cfg.Region + ".amazonaws.com/"
	}
	return &AWS{region: cfg.Region, keyID: cfg.KeyID, creds: cfg.Creds, doer: doer, now: cfg.Now, endpoint: endpoint}, nil
}

// Provider reports the backend name recorded in envelopes.
func (a *AWS) Provider() string { return ProviderAWS }

// KeyID reports the non-secret KEK reference: the RESOLVED key ARN once a wrap
// reported it (so an alias-configured KEK records the actual key — see
// resolvedARN), else the configured id.
func (a *AWS) KeyID() string {
	if a.resolvedARN != "" {
		return a.resolvedARN
	}
	return a.keyID
}

// WrapKey encrypts the DEK under the KEK with the context bound as
// EncryptionContext, caching the resolved key ARN from the response.
func (a *AWS) WrapKey(ctx context.Context, plaintext []byte, aad map[string]string) ([]byte, error) {
	body := map[string]any{
		"KeyId":     a.keyID,
		"Plaintext": base64.StdEncoding.EncodeToString(plaintext),
	}
	if len(aad) > 0 {
		body["EncryptionContext"] = aad
	}
	var resp struct {
		CiphertextBlob string `json:"CiphertextBlob"`
		KeyID          string `json:"KeyId"` // the resolved key ARN
	}
	if err := a.call(ctx, "TrentService.Encrypt", mustJSON(body), &resp); err != nil {
		return nil, err
	}
	if resp.KeyID != "" {
		a.resolvedARN = resp.KeyID
	}
	ct, err := base64.StdEncoding.DecodeString(resp.CiphertextBlob)
	if err != nil {
		return nil, fmt.Errorf("kmswrap: decode AWS ciphertext: %w", err)
	}
	if len(ct) == 0 {
		return nil, fmt.Errorf("kmswrap: AWS KMS returned an empty ciphertext")
	}
	return ct, nil
}

// UnwrapKey decrypts the DEK, pinning the configured KEK and requiring the same
// EncryptionContext it was wrapped under.
func (a *AWS) UnwrapKey(ctx context.Context, ciphertext []byte, aad map[string]string) ([]byte, error) {
	body := map[string]any{
		"CiphertextBlob": base64.StdEncoding.EncodeToString(ciphertext),
		"KeyId":          a.keyID,
	}
	if len(aad) > 0 {
		body["EncryptionContext"] = aad
	}
	var resp struct {
		Plaintext string `json:"Plaintext"`
	}
	if err := a.call(ctx, "TrentService.Decrypt", mustJSON(body), &resp); err != nil {
		return nil, err
	}
	pt, err := base64.StdEncoding.DecodeString(resp.Plaintext)
	if err != nil {
		return nil, fmt.Errorf("kmswrap: decode AWS plaintext: %w", err)
	}
	if len(pt) == 0 {
		return nil, fmt.Errorf("kmswrap: AWS KMS returned an empty plaintext")
	}
	return pt, nil
}

// call performs one signed AWS JSON 1.1 KMS request.
func (a *AWS) call(ctx context.Context, target string, body []byte, out any) error {
	req, raw, err := newJSONPost(a.endpoint, "application/x-amz-json-1.1", body)
	if err != nil {
		return err
	}
	req.Header.Set("X-Amz-Target", target)
	sigv4.Sign(req, raw, "kms", a.region, a.creds, nowOr(a.now))
	return doJSON(ctx, a.doer, req, out)
}
