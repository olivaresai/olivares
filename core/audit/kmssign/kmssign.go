// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package kmssign

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/sha512"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/olivaresai/olivares/core/audit"
)

// Doer is the minimal HTTP surface the backends use (injectable for tests). The
// stdlib *http.Client satisfies it.
type Doer interface {
	Do(*http.Request) (*http.Response, error)
}

// hashFor returns the digest of a preimage for a SigAlg's hash. The off-box
// verifier (core/audit) reproduces the same digest, so both sides MUST agree.
func hashFor(alg audit.SigAlg, preimage []byte) []byte {
	switch alg {
	case audit.AlgECDSAP384SHA384:
		d := sha512.Sum384(preimage)
		return d[:]
	default: // ECDSA P-256 / RSA SHA-256 / default
		d := sha256.Sum256(preimage)
		return d[:]
	}
}

// doJSON performs an HTTP request with a JSON body and decodes a JSON response,
// returning a clear error for a non-2xx status. It never logs the body (it can
// carry a key id, though never a private key).
func doJSON(ctx context.Context, doer Doer, req *http.Request, out any) error {
	resp, err := doer.Do(req.WithContext(ctx))
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode/100 != 2 {
		return fmt.Errorf("kmssign: %s returned HTTP %d", req.URL.Host, resp.StatusCode)
	}
	if out == nil {
		return nil
	}
	if err := json.Unmarshal(body, out); err != nil {
		return fmt.Errorf("kmssign: decode response: %w", err)
	}
	return nil
}

// newJSONPost builds a POST with a JSON body and the given content-type.
func newJSONPost(url, contentType string, body []byte) (*http.Request, []byte, error) {
	req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return nil, nil, err
	}
	req.Header.Set("Content-Type", contentType)
	return req, body, nil
}

func mustJSON(v any) []byte {
	b, _ := json.Marshal(v)
	return b
}

func nowOr(now func() time.Time) time.Time {
	if now != nil {
		return now()
	}
	return time.Now()
}

// compile-time proof that every backend satisfies the off-box seam.
var (
	_ audit.CheckpointKey = (*AWS)(nil)
	_ audit.CheckpointKey = (*GCP)(nil)
	_ audit.CheckpointKey = (*Azure)(nil)
)
