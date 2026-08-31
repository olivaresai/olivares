// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package kmssign

import (
	"context"
	"encoding/base64"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/olivaresai/olivares/core/audit"
)

// AWS is an off-box checkpoint signer backed by AWS KMS. It signs via the KMS Sign
// API (X-Amz-Target TrentService.Sign, AWS JSON 1.1, SigV4) in DIGEST mode and
// returns the X9.62 ASN.1-DER ECDSA signature an off-box verifier checks with the
// key's public half (KMS GetPublicKey → DER SubjectPublicKeyInfo). Pure-Go.
//
// Verified against docs.aws.amazon.com (KMS Sign / GetPublicKey API reference):
// endpoint kms.<region>.amazonaws.com, request {KeyId, Message(base64),
// MessageType:"DIGEST", SigningAlgorithm}, response {Signature(base64)}; for an
// ECC_NIST_P256 key with ECDSA_SHA_256 the Signature is the ASN.1-DER ECDSA value.
type AWS struct {
	region     string
	keyID      string
	creds      AWSCreds
	signingAlg string // KMS SigningAlgorithmSpec, e.g. ECDSA_SHA_256
	alg        audit.SigAlg
	doer       Doer
	now        func() time.Time
	endpoint   string // override (tests); default kms.<region>.amazonaws.com

	pubDER []byte // cached GetPublicKey result
}

// AWSConfig configures an AWS KMS checkpoint signer.
type AWSConfig struct {
	Region string
	KeyID  string // key id, key ARN or alias (kms:Sign resolves it)
	Creds  AWSCreds
	// SigningAlgorithm is a KMS SigningAlgorithmSpec; empty defaults to
	// ECDSA_SHA_256 (the cross-cloud default, an ECC_NIST_P256 key).
	SigningAlgorithm string
	// Doer overrides the HTTP client (tests); nil uses http.DefaultClient.
	Doer Doer
	// Endpoint overrides the host (tests); empty uses kms.<region>.amazonaws.com.
	Endpoint string
	// Now overrides the clock (tests); nil uses time.Now.
	Now func() time.Time
}

// awsAlgMap maps a KMS SigningAlgorithmSpec to the audit.SigAlg the off-box
// verifier uses. Only the ECDSA X9.62 algorithms are supported here (the ledger's
// detached-signature model); RSA/Ed25519/ML-DSA KMS keys are a documented seam.
var awsAlgMap = map[string]audit.SigAlg{
	"ECDSA_SHA_256": audit.AlgECDSAP256SHA256,
	"ECDSA_SHA_384": audit.AlgECDSAP384SHA384,
}

// NewAWS builds an AWS KMS off-box checkpoint signer.
func NewAWS(cfg AWSConfig) (*AWS, error) {
	if strings.TrimSpace(cfg.Region) == "" {
		return nil, fmt.Errorf("kmssign: aws region is required")
	}
	if strings.TrimSpace(cfg.KeyID) == "" {
		return nil, fmt.Errorf("kmssign: aws key id is required")
	}
	sa := cfg.SigningAlgorithm
	if sa == "" {
		sa = "ECDSA_SHA_256"
	}
	alg, ok := awsAlgMap[sa]
	if !ok {
		return nil, fmt.Errorf("kmssign: unsupported AWS KMS SigningAlgorithm %q for the ledger (use ECDSA_SHA_256 or ECDSA_SHA_384; RSA/Ed25519 keys are a documented seam)", sa)
	}
	doer := cfg.Doer
	if doer == nil {
		doer = http.DefaultClient
	}
	endpoint := cfg.Endpoint
	if endpoint == "" {
		endpoint = "https://kms." + cfg.Region + ".amazonaws.com/"
	}
	return &AWS{
		region: cfg.Region, keyID: cfg.KeyID, creds: cfg.Creds,
		signingAlg: sa, alg: alg, doer: doer, now: cfg.Now, endpoint: endpoint,
	}, nil
}

// Algorithm reports the off-box signature scheme.
func (a *AWS) Algorithm() audit.SigAlg { return a.alg }

// KeyID reports the non-secret key reference recorded in the checkpoint Meta.
func (a *AWS) KeyID() string { return a.keyID }

// SignCheckpoint signs SHA-256(preimage) with KMS in DIGEST mode and returns the
// ASN.1-DER ECDSA signature.
func (a *AWS) SignCheckpoint(ctx context.Context, preimage []byte) ([]byte, error) {
	digest := hashFor(a.alg, preimage)
	body := mustJSON(map[string]string{
		"KeyId":            a.keyID,
		"Message":          base64.StdEncoding.EncodeToString(digest),
		"MessageType":      "DIGEST",
		"SigningAlgorithm": a.signingAlg,
	})
	var resp struct {
		Signature string `json:"Signature"`
	}
	if err := a.call(ctx, "TrentService.Sign", body, &resp); err != nil {
		return nil, err
	}
	sig, err := base64.StdEncoding.DecodeString(resp.Signature)
	if err != nil {
		return nil, fmt.Errorf("kmssign: decode AWS signature: %w", err)
	}
	if len(sig) == 0 {
		return nil, fmt.Errorf("kmssign: AWS KMS returned an empty signature")
	}
	return sig, nil
}

// PublicKey fetches (and caches) the DER SubjectPublicKeyInfo via GetPublicKey.
func (a *AWS) PublicKey(ctx context.Context) ([]byte, error) {
	if a.pubDER != nil {
		return a.pubDER, nil
	}
	body := mustJSON(map[string]string{"KeyId": a.keyID})
	var resp struct {
		PublicKey string `json:"PublicKey"`
	}
	if err := a.call(ctx, "TrentService.GetPublicKey", body, &resp); err != nil {
		return nil, err
	}
	der, err := base64.StdEncoding.DecodeString(resp.PublicKey)
	if err != nil {
		return nil, fmt.Errorf("kmssign: decode AWS public key: %w", err)
	}
	a.pubDER = der
	return der, nil
}

// call performs one signed AWS JSON 1.1 KMS request.
func (a *AWS) call(ctx context.Context, target string, body []byte, out any) error {
	req, raw, err := newJSONPost(a.endpoint, "application/x-amz-json-1.1", body)
	if err != nil {
		return err
	}
	req.Header.Set("X-Amz-Target", target)
	signSigV4(req, raw, "kms", a.region, a.creds, nowOr(a.now))
	return doJSON(ctx, a.doer, req, out)
}
