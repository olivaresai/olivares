// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package dr

import (
	"bytes"
	"context"
	"crypto/md5"
	"encoding/base64"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// lockRecorder records every request the client sends and answers with a canned
// status and body, so a test can read the exact headers and bodies on the wire.
type lockRecorder struct {
	mu     sync.Mutex
	reqs   []*http.Request
	bodies [][]byte
	status int
	body   string
}

func (l *lockRecorder) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	b, _ := io.ReadAll(r.Body)
	l.mu.Lock()
	l.reqs = append(l.reqs, r)
	l.bodies = append(l.bodies, b)
	l.mu.Unlock()
	status := l.status
	if status == 0 {
		status = http.StatusOK
	}
	w.WriteHeader(status)
	_, _ = io.WriteString(w, l.body)
}

func newLockClient(t *testing.T, cfg OffsiteConfig, rec *lockRecorder) *OffsiteClient {
	t.Helper()
	srv := httptest.NewServer(rec)
	t.Cleanup(srv.Close)
	cfg.Endpoint, cfg.Bucket, cfg.AccessKeyID, cfg.SecretAccessKey = srv.URL, "dr-bucket", "AKIAEXAMPLE", "secretexample"
	c, err := NewOffsiteClient(cfg)
	if err != nil {
		t.Fatal(err)
	}
	c.now = func() time.Time { return time.Date(2026, 10, 28, 4, 0, 0, 0, time.UTC) }
	return c
}

func md5Base64(b []byte) string {
	sum := md5.Sum(b)
	return base64.StdEncoding.EncodeToString(sum[:])
}

// An append-only writer locks every object it puts in COMPLIANCE mode until now +
// RetainFor, with the Content-MD5 S3 requires on a locked put, and signs those
// headers so no proxy can strip the lock from a request the bucket accepts.
func TestOffsiteImmutablePutLocksObject(t *testing.T) {
	rec := &lockRecorder{}
	c := newLockClient(t, OffsiteConfig{Immutable: true, RetainFor: 7 * 24 * time.Hour}, rec)
	payload := []byte("bundle bytes")
	if err := c.Put(context.Background(), "a.drbundle", bytes.NewReader(payload), int64(len(payload))); err != nil {
		t.Fatal(err)
	}
	r := rec.reqs[0]
	for header, want := range map[string]string{
		"X-Amz-Object-Lock-Mode":              "COMPLIANCE",
		"X-Amz-Object-Lock-Retain-Until-Date": "2026-11-04T04:00:00Z",
		"Content-Md5":                         md5Base64(payload),
	} {
		if got := r.Header.Get(header); got != want {
			t.Errorf("%s = %q, want %q", header, got, want)
		}
	}
	auth := r.Header.Get("Authorization")
	if !strings.Contains(auth, "SignedHeaders=content-md5;host;x-amz-content-sha256;x-amz-date;x-amz-object-lock-mode;x-amz-object-lock-retain-until-date,") {
		t.Errorf("lock headers are not signed: %s", auth)
	}
	if !bytes.Equal(rec.bodies[0], payload) {
		t.Errorf("body after the MD5 pass = %q, want the whole bundle", rec.bodies[0])
	}
}

// The writer of an immutable copy must never delete: Delete is refused before any
// request leaves the host, whatever the credential would allow.
func TestOffsiteImmutableDeleteRefused(t *testing.T) {
	rec := &lockRecorder{status: http.StatusNoContent}
	c := newLockClient(t, OffsiteConfig{Immutable: true, RetainFor: time.Hour}, rec)
	err := c.Delete(context.Background(), "a.drbundle")
	if !errors.Is(err, ErrOffsiteImmutable) {
		t.Fatalf("Delete on the immutable writer = %v, want ErrOffsiteImmutable", err)
	}
	if len(rec.reqs) != 0 {
		t.Fatalf("Delete reached the bucket: %d request(s)", len(rec.reqs))
	}
}

func TestOffsiteImmutableConfigNeedsRetention(t *testing.T) {
	if _, err := NewOffsiteClient(OffsiteConfig{Bucket: "b", AccessKeyID: "a", SecretAccessKey: "s", Immutable: true}); err == nil {
		t.Fatal("an immutable writer without a retention period was accepted")
	}
}

// A locked put needs the MD5 before the body is sent, so the reader must rewind;
// a stream that cannot is refused rather than sent unlocked.
func TestOffsiteImmutablePutNeedsSeekableBody(t *testing.T) {
	rec := &lockRecorder{}
	c := newLockClient(t, OffsiteConfig{Immutable: true, RetainFor: time.Hour}, rec)
	if err := c.Put(context.Background(), "a.drbundle", io.LimitReader(strings.NewReader("x"), 1), 1); err == nil {
		t.Fatal("a non-seekable body was accepted for a locked put")
	}
	if len(rec.reqs) != 0 {
		t.Fatal("the unlocked put was sent")
	}
}

func TestOffsiteLockStatus(t *testing.T) {
	cases := []struct {
		name        string
		status      int
		body        string
		wantEnabled bool
		wantMode    string
		wantErr     bool
	}{
		{"compliance", 200, `<ObjectLockConfiguration><ObjectLockEnabled>Enabled</ObjectLockEnabled><Rule><DefaultRetention><Mode>COMPLIANCE</Mode><Days>7</Days></DefaultRetention></Rule></ObjectLockConfiguration>`, true, "COMPLIANCE", false},
		{"governance", 200, `<ObjectLockConfiguration><ObjectLockEnabled>Enabled</ObjectLockEnabled><Rule><DefaultRetention><Mode>GOVERNANCE</Mode><Days>1</Days></DefaultRetention></Rule></ObjectLockConfiguration>`, true, "GOVERNANCE", false},
		{"enabled without default", 200, `<ObjectLockConfiguration><ObjectLockEnabled>Enabled</ObjectLockEnabled></ObjectLockConfiguration>`, true, "", false},
		{"no lock", 404, `<Error><Code>ObjectLockConfigurationNotFoundError</Code></Error>`, false, "", false},
		{"denied", 403, `<Error><Code>AccessDenied</Code></Error>`, false, "", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := &lockRecorder{status: tc.status, body: tc.body}
			c := newLockClient(t, OffsiteConfig{}, rec)
			st, err := c.LockStatus(context.Background())
			if (err != nil) != tc.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, tc.wantErr)
			}
			if st.Enabled != tc.wantEnabled || st.DefaultMode != tc.wantMode {
				t.Fatalf("status = %+v, want enabled=%v mode=%q", st, tc.wantEnabled, tc.wantMode)
			}
			if got := rec.reqs[0].URL.RawQuery; got != "object-lock=" && got != "object-lock" {
				t.Fatalf("query = %q, want the object-lock subresource", got)
			}
		})
	}
}

// The pruner keeps an older bundle locked as long as GFS keeps it: it extends the
// object's COMPLIANCE retention with PutObjectRetention.
func TestOffsiteExtendRetention(t *testing.T) {
	rec := &lockRecorder{}
	c := newLockClient(t, OffsiteConfig{Prefix: "p"}, rec)
	until := time.Date(2027, 1, 1, 0, 0, 0, 0, time.UTC)
	if err := c.ExtendRetention(context.Background(), "a.drbundle", until); err != nil {
		t.Fatal(err)
	}
	r, body := rec.reqs[0], rec.bodies[0]
	if r.Method != http.MethodPut || r.URL.Path != "/dr-bucket/p/a.drbundle" || !strings.HasPrefix(r.URL.RawQuery, "retention") {
		t.Fatalf("request = %s %s?%s", r.Method, r.URL.Path, r.URL.RawQuery)
	}
	want := `<Retention><Mode>COMPLIANCE</Mode><RetainUntilDate>2027-01-01T00:00:00Z</RetainUntilDate></Retention>`
	if string(body) != want {
		t.Fatalf("body = %s, want %s", body, want)
	}
	if r.Header.Get("Content-Md5") != md5Base64(body) {
		t.Fatal("retention body has no matching Content-MD5")
	}
}

func TestGFSLockSpans(t *testing.T) {
	day := 24 * time.Hour
	cases := []struct {
		policy GFSPolicy
		writer time.Duration
		reason string
		keep   time.Duration
	}{
		{GFSPolicy{Daily: 7, Weekly: 4, Monthly: 12}, 7 * day, "weekly+monthly", 12 * 31 * day},
		{GFSPolicy{Weekly: 2, Yearly: 1}, 14 * day, "weekly", 14 * day},
		{GFSPolicy{Monthly: 3, KeepLast: 5}, 3 * 31 * day, "keep-last", 0},
		{GFSPolicy{Yearly: 2}, 2 * 366 * day, "yearly", 2 * 366 * day},
		{GFSPolicy{KeepLast: 3}, 0, "keep-last", 0},
		{GFSPolicy{}, 0, "retention-disabled", 0},
	}
	for _, tc := range cases {
		if got := tc.policy.WriterLockSpan(); got != tc.writer {
			t.Errorf("%+v WriterLockSpan = %v, want %v", tc.policy, got, tc.writer)
		}
		if got := tc.policy.KeepLockSpan(tc.reason); got != tc.keep {
			t.Errorf("%+v KeepLockSpan(%q) = %v, want %v", tc.policy, tc.reason, got, tc.keep)
		}
	}
}
