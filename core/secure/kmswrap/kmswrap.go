// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package kmswrap

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"time"

	"github.com/olivaresai/olivares/core/secure"
)

// Provider names recorded in sealed envelopes and validated at open.
const (
	ProviderAWS   = "aws-kms"
	ProviderGCP   = "gcp-kms"
	ProviderAzure = "azure-kv"
)

// Doer is the minimal HTTP surface the backends use (injectable for tests). The
// stdlib *http.Client satisfies it.
type Doer interface {
	Do(*http.Request) (*http.Response, error)
}

// TokenSource yields a fresh bearer access token for GCP/Azure. It is called per
// request so a short-lived token is refreshed by the operator's chosen mechanism
// (workload-identity, metadata server, gcloud/az token). Returning the token,
// never storing it here, keeps credential lifetime out of this package.
type TokenSource func(ctx context.Context) (string, error)

// StaticToken is a TokenSource for a fixed token (tests / a short-lived run).
// For a long-lived engine prefer a refreshing source.
func StaticToken(tok string) TokenSource {
	return func(context.Context) (string, error) { return tok, nil }
}

// canonicalAAD flattens the non-secret wrap context into deterministic bytes
// for providers whose AAD is a byte string (GCP). The encoding is INJECTIVE —
// each key and value is 4-byte-BE length-prefixed, sorted by key — so two
// different maps can never canonicalize to the same bytes (a "k=v\n" join
// would let {"x":"1\ny=2"} collide with {"x":"1","y":"2"}, silently weakening
// the UnwrapKey must-match contract). AWS takes the map natively
// (EncryptionContext); Azure has no AAD channel.
func canonicalAAD(aad map[string]string) []byte {
	if len(aad) == 0 {
		return nil
	}
	keys := make([]string, 0, len(aad))
	for k := range aad {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var buf []byte
	for _, k := range keys {
		buf = appendLenPrefixed(buf, []byte(k))
		buf = appendLenPrefixed(buf, []byte(aad[k]))
	}
	return buf
}

func appendLenPrefixed(dst, b []byte) []byte {
	dst = append(dst, byte(len(b)>>24), byte(len(b)>>16), byte(len(b)>>8), byte(len(b)))
	return append(dst, b...)
}

// doJSON performs an HTTP request with a JSON body and decodes a JSON response,
// returning a clear error for a non-2xx status. A SUCCESS body is never logged
// (an unwrap response carries the DEK); an ERROR body never carries key
// material, and it holds the one fact custody diagnostics need — the provider's
// error code that distinguishes "the customer revoked the KEK" (the CMEK
// kill-switch working as designed) from a tampered envelope, a mis-pointed key
// or missing IAM — so the discriminator is extracted and surfaced.
func doJSON(ctx context.Context, doer Doer, req *http.Request, out any) error {
	resp, err := doer.Do(req.WithContext(ctx))
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode/100 != 2 {
		return fmt.Errorf("kmswrap: %s returned HTTP %d%s", req.URL.Host, resp.StatusCode, errDetail(body))
	}
	if out == nil {
		return nil
	}
	if err := json.Unmarshal(body, out); err != nil {
		return fmt.Errorf("kmswrap: decode response: %w", err)
	}
	return nil
}

// errDetail extracts the provider's error discriminator from a non-2xx body:
// AWS `__type` (+ optional message), Azure/GCP `error.code|status` (+ message).
// Messages are clamped — they are diagnostics, not payload.
func errDetail(body []byte) string {
	var e struct {
		Type    string `json:"__type"`  // AWS, e.g. "DisabledException"
		Message string `json:"message"` // AWS detail
		Error   struct {
			Code    any    `json:"code"`   // Azure string ("KeyDisabled") / GCP number
			Status  string `json:"status"` // GCP, e.g. "PERMISSION_DENIED"
			Message string `json:"message"`
		} `json:"error"`
	}
	if json.Unmarshal(body, &e) != nil {
		return ""
	}
	kind, msg := e.Type, e.Message
	if kind == "" {
		if s, ok := e.Error.Code.(string); ok && s != "" {
			kind = s
		} else if e.Error.Status != "" {
			kind = e.Error.Status
		}
		if msg == "" {
			msg = e.Error.Message
		}
	}
	if kind == "" && msg == "" {
		return ""
	}
	const maxMsg = 200
	if len(msg) > maxMsg {
		msg = msg[:maxMsg] + "…"
	}
	switch {
	case kind != "" && msg != "":
		return " (" + kind + ": " + msg + ")"
	case kind != "":
		return " (" + kind + ")"
	default:
		return " (" + msg + ")"
	}
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

// compile-time proof that every backend satisfies the custody seam.
var (
	_ secure.KeyWrapper = (*AWS)(nil)
	_ secure.KeyWrapper = (*GCP)(nil)
	_ secure.KeyWrapper = (*Azure)(nil)
)
