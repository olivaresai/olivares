// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package dr

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// TestSigV4KAT pins the SigV4 chain to the fully-documented AWS S3 "GET Object"
// example (AmazonS3/latest/API/sig-v4-header-based-auth.html): secret
// wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY, 20130524/us-east-1/s3, over the exact
// canonical request AWS publishes. Matching both the canonical-request hash and the
// final signature proves sigV4SigningKey + the HMAC/SHA-256 chain are byte-correct
// against an external anchor, not a self-check.
func TestSigV4KAT(t *testing.T) {
	const canonicalRequest = "GET\n" +
		"/test.txt\n" +
		"\n" +
		"host:examplebucket.s3.amazonaws.com\n" +
		"range:bytes=0-9\n" +
		"x-amz-content-sha256:e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855\n" +
		"x-amz-date:20130524T000000Z\n" +
		"\n" +
		"host;range;x-amz-content-sha256;x-amz-date\n" +
		"e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"

	if got := hexSHA256([]byte(canonicalRequest)); got != "7344ae5b7ee6c3e7e6b0fe0640412a37625d1fbfff95c48bbb2dc43964946972" {
		t.Fatalf("canonical-request hash mismatch: %s", got)
	}
	stringToSign := "AWS4-HMAC-SHA256\n" +
		"20130524T000000Z\n" +
		"20130524/us-east-1/s3/aws4_request\n" +
		hexSHA256([]byte(canonicalRequest))
	key := sigV4SigningKey("wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY", "20130524", "us-east-1", "s3")
	got := hex.EncodeToString(hmacSHA256(key, []byte(stringToSign)))
	const want = "f0e8bdb87c964420e857bd35b5d6ed310bd44f0170aba48dd91039c6036bdb41"
	if got != want {
		t.Fatalf("SigV4 signature KAT mismatch:\n got  %s\n want %s", got, want)
	}
}

// TestOffsiteEmptyBodyHashConst guards the empty-payload SHA-256 sentinel against a
// typo (it is signed on every GET/LIST/DELETE).
func TestOffsiteEmptyBodyHashConst(t *testing.T) {
	if got := hexSHA256(nil); got != offsiteEmptyBodyHash {
		t.Fatalf("empty-body hash const wrong: got %s want %s", got, offsiteEmptyBodyHash)
	}
}

func TestNewOffsiteClientValidation(t *testing.T) {
	if _, err := NewOffsiteClient(OffsiteConfig{AccessKeyID: "a", SecretAccessKey: "b"}); err == nil {
		t.Fatal("expected error with no bucket")
	}
	if _, err := NewOffsiteClient(OffsiteConfig{Bucket: "b", AccessKeyID: "a"}); err == nil {
		t.Fatal("expected error with no secret (a bundle carries signing keys — never anonymous)")
	}
	c, err := NewOffsiteClient(OffsiteConfig{Bucket: "b", AccessKeyID: "a", SecretAccessKey: "s", Endpoint: "https://x.example"})
	if err != nil {
		t.Fatalf("valid config: %v", err)
	}
	if !c.cfg.PathStyle {
		t.Fatal("a custom endpoint must force path-style addressing")
	}
	if c.cfg.Region != offsiteDefaultRegion {
		t.Fatalf("region default: got %q", c.cfg.Region)
	}
}

// mockS3 is a tiny in-memory S3-compatible store for the round-trip test. It
// enforces that every request carries a well-formed SigV4 Authorization header and
// the signed content-sha256, so the signing path is exercised end to end.
type mockS3 struct {
	mu      sync.Mutex
	objects map[string][]byte
	bucket  string
	t       *testing.T
}

func (m *mockS3) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	auth := r.Header.Get("Authorization")
	if !strings.HasPrefix(auth, "AWS4-HMAC-SHA256 Credential=") {
		m.t.Errorf("missing/invalid SigV4 Authorization: %q", auth)
		http.Error(w, "unsigned", http.StatusForbidden)
		return
	}
	if !strings.Contains(auth, "SignedHeaders=host;x-amz-content-sha256;x-amz-date") {
		m.t.Errorf("unexpected SignedHeaders in %q", auth)
	}
	if r.Header.Get("X-Amz-Content-Sha256") == "" || r.Header.Get("X-Amz-Date") == "" {
		m.t.Errorf("missing x-amz-content-sha256/x-amz-date")
	}

	// Path-style: /{bucket} (list) or /{bucket}/{key...}.
	trimmed := strings.TrimPrefix(r.URL.Path, "/"+m.bucket)
	key := strings.TrimPrefix(trimmed, "/")

	switch r.Method {
	case http.MethodGet:
		if r.URL.Query().Get("list-type") == "2" {
			m.list(w, r)
			return
		}
		m.mu.Lock()
		b, ok := m.objects[key]
		m.mu.Unlock()
		if !ok {
			m.s3Error(w, http.StatusNotFound, "NoSuchKey")
			return
		}
		_, _ = w.Write(b)
	case http.MethodPut:
		if got := r.Header.Get("X-Amz-Content-Sha256"); got != offsiteUnsignedBody {
			m.t.Errorf("PUT should sign UNSIGNED-PAYLOAD, got %q", got)
		}
		b, _ := io.ReadAll(r.Body)
		m.mu.Lock()
		m.objects[key] = b
		m.mu.Unlock()
		w.WriteHeader(http.StatusOK)
	case http.MethodDelete:
		m.mu.Lock()
		delete(m.objects, key)
		m.mu.Unlock()
		w.WriteHeader(http.StatusNoContent)
	default:
		http.Error(w, "method", http.StatusMethodNotAllowed)
	}
}

func (m *mockS3) list(w http.ResponseWriter, r *http.Request) {
	prefix := r.URL.Query().Get("prefix")
	type entry struct {
		Key          string `xml:"Key"`
		Size         int64  `xml:"Size"`
		LastModified string `xml:"LastModified"`
	}
	var res struct {
		XMLName     xml.Name `xml:"ListBucketResult"`
		IsTruncated bool     `xml:"IsTruncated"`
		Contents    []entry  `xml:"Contents"`
	}
	m.mu.Lock()
	for k, v := range m.objects {
		if prefix == "" || strings.HasPrefix(k, prefix) {
			res.Contents = append(res.Contents, entry{Key: k, Size: int64(len(v)), LastModified: "2026-07-09T10:00:00.000Z"})
		}
	}
	m.mu.Unlock()
	w.Header().Set("Content-Type", "application/xml")
	_ = xml.NewEncoder(w).Encode(&res)
}

func (m *mockS3) s3Error(w http.ResponseWriter, status int, code string) {
	w.WriteHeader(status)
	_, _ = fmt.Fprintf(w, "<Error><Code>%s</Code><Message>%s</Message></Error>", code, code)
}

func newMockClient(t *testing.T, prefix string) (*OffsiteClient, *mockS3) {
	t.Helper()
	mock := &mockS3{objects: map[string][]byte{}, bucket: "dr-bucket", t: t}
	srv := httptest.NewServer(mock)
	t.Cleanup(srv.Close)
	c, err := NewOffsiteClient(OffsiteConfig{
		Endpoint: srv.URL, Bucket: "dr-bucket", Region: "auto", Prefix: prefix,
		AccessKeyID: "AKIAEXAMPLE", SecretAccessKey: "secretexample",
	})
	if err != nil {
		t.Fatalf("client: %v", err)
	}
	return c, mock
}

func TestOffsiteRoundTrip(t *testing.T) {
	ctx := context.Background()
	c, _ := newMockClient(t, "backups/prod")

	payload := []byte("this is a fake .drbundle payload, streamed off-box")
	if err := c.Put(ctx, "olivares-20260709-sqlite.drbundle", bytes.NewReader(payload), int64(len(payload))); err != nil {
		t.Fatalf("put: %v", err)
	}

	// List sees it, with the prefix stripped from Name.
	objs, err := c.List(ctx)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(objs) != 1 {
		t.Fatalf("list: want 1 object, got %d (%+v)", len(objs), objs)
	}
	if objs[0].Name != "olivares-20260709-sqlite.drbundle" {
		t.Fatalf("list name not prefix-stripped: %q", objs[0].Name)
	}
	if objs[0].Size != int64(len(payload)) {
		t.Fatalf("list size: got %d want %d", objs[0].Size, len(payload))
	}

	// Get returns the exact bytes.
	rc, err := c.Get(ctx, "olivares-20260709-sqlite.drbundle")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	got, _ := io.ReadAll(rc)
	_ = rc.Close()
	if !bytes.Equal(got, payload) {
		t.Fatalf("round-trip mismatch: got %q", got)
	}

	// Delete removes it.
	if err := c.Delete(ctx, "olivares-20260709-sqlite.drbundle"); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, err := c.Get(ctx, "olivares-20260709-sqlite.drbundle"); err == nil {
		t.Fatal("get after delete should fail")
	}
	objs, err = c.List(ctx)
	if err != nil {
		t.Fatalf("list after delete: %v", err)
	}
	if len(objs) != 0 {
		t.Fatalf("list after delete: want 0, got %d", len(objs))
	}
}

func TestOffsiteGetMissingIsError(t *testing.T) {
	c, _ := newMockClient(t, "")
	if _, err := c.Get(context.Background(), "nope.drbundle"); err == nil {
		t.Fatal("expected an error for a missing object")
	} else if !strings.Contains(err.Error(), "NoSuchKey") {
		t.Fatalf("error should surface the S3 code, got: %v", err)
	}
}

// TestOffsiteSignStable proves the signature is deterministic for a fixed clock and
// inputs (the property a server relies on to recompute it).
func TestOffsiteSignStable(t *testing.T) {
	c, _ := newMockClient(t, "p")
	fixed := time.Date(2026, 7, 9, 12, 0, 0, 0, time.UTC)
	c.now = func() time.Time { return fixed }
	req, _ := http.NewRequest(http.MethodGet, c.objectURL(c.objectKey("b.drbundle"), ""), nil)
	c.sign(req, offsiteEmptyBodyHash)
	first := req.Header.Get("Authorization")

	req2, _ := http.NewRequest(http.MethodGet, c.objectURL(c.objectKey("b.drbundle"), ""), nil)
	c.sign(req2, offsiteEmptyBodyHash)
	if first != req2.Header.Get("Authorization") {
		t.Fatal("signature not deterministic for a fixed clock")
	}
	if !strings.Contains(first, "/auto/s3/aws4_request") {
		t.Fatalf("scope should carry region/service, got %q", first)
	}
}
