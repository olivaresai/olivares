// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package s3archive

import (
	"context"
	"crypto/md5" //nolint:gosec // asserting the Content-MD5 integrity tag S3 requires
	"encoding/base64"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/olivaresai/olivares/connectors/internal/awssig"
	"github.com/olivaresai/olivares/connectors/internal/delivery"
	"github.com/olivaresai/olivares/sdk"
	sdkmodel "github.com/olivaresai/olivares/sdk/model"
)

// emptySHA256 is the hex SHA-256 of an empty body (what a HEAD signs over).
const emptySHA256 = "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"

var fixedNow = time.Date(2026, 6, 6, 12, 0, 0, 0, time.UTC)

// stubDoer is the injected transport: it records every request (method, URL,
// headers, body) and answers from a script. No live network is ever touched.
type stubDoer struct {
	reqs  []stubReq
	resps []stubResp
}

type stubReq struct {
	method string
	url    string
	header http.Header
	body   []byte
}

type stubResp struct {
	status int
	header map[string]string
	body   string
	err    error
}

func (s *stubDoer) Do(req *http.Request) (*http.Response, error) {
	var body []byte
	if req.Body != nil {
		body, _ = io.ReadAll(req.Body)
	}
	s.reqs = append(s.reqs, stubReq{method: req.Method, url: req.URL.String(), header: req.Header.Clone(), body: body})
	i := len(s.reqs) - 1
	if i >= len(s.resps) {
		i = len(s.resps) - 1
	}
	r := s.resps[i]
	if r.err != nil {
		return nil, r.err
	}
	h := http.Header{}
	for k, v := range r.header {
		h.Set(k, v)
	}
	return &http.Response{StatusCode: r.status, Header: h, Body: io.NopCloser(strings.NewReader(r.body))}, nil
}

// okPut is a successful PUT response carrying the version/etag headers.
func okPut() stubResp {
	return stubResp{status: 200, header: map[string]string{"ETag": `"etag-abc"`, "x-amz-version-id": "ver-1"}}
}

// lockedHead is a HEAD response reporting the given object-lock state.
func lockedHead(mode, retain string, hold bool) stubResp {
	h := map[string]string{}
	if mode != "" {
		h["x-amz-object-lock-mode"] = mode
		h["x-amz-object-lock-retain-until-date"] = retain
	}
	if hold {
		h["x-amz-object-lock-legal-hold"] = "ON"
	}
	return stubResp{status: 200, header: h}
}

// openOutput opens a connector over the stub with a valid base configuration,
// a fixed clock and no real backoff sleeping.
func openOutput(t *testing.T, d delivery.Doer, overrides map[string]string) *Output {
	t.Helper()
	o := New()
	o.doer = d
	o.now = func() time.Time { return fixedNow }
	o.sleep = func(context.Context, time.Duration) error { return nil }
	settings := map[string]string{
		"region":            "eu-west-1",
		"bucket":            "worm-bucket",
		"access_key_id":     "AKIDEXAMPLE",
		"secret_access_key": "SUPERSECRETKEY",
	}
	for k, v := range overrides {
		settings[k] = v
	}
	if err := o.Open(context.Background(), sdk.Config{Settings: settings}); err != nil {
		t.Fatalf("Open: %v", err)
	}
	return o
}

func sampleNotification() sdk.Notification {
	return sdk.Notification{
		Type:     "finding.reported",
		Title:    "secret write blocked",
		Body:     "claude-1 denied",
		Severity: sdkmodel.SeverityHigh,
		Tenant:   "acme",
		Fields:   map[string]string{"agent": "claude-1"},
		Time:     time.Date(2026, 6, 6, 10, 0, 0, 0, time.UTC),
	}
}

// signedHeadersOf extracts the SignedHeaders list from an Authorization header.
func signedHeadersOf(t *testing.T, auth string) string {
	t.Helper()
	_, rest, ok := strings.Cut(auth, "SignedHeaders=")
	if !ok {
		t.Fatalf("Authorization has no SignedHeaders: %q", auth)
	}
	sh, _, _ := strings.Cut(rest, ",")
	return sh
}

func TestPutComplianceHeadersAndReceipt(t *testing.T) {
	body := []byte(`{"seq":1}`)
	retain := time.Date(2027, 6, 6, 12, 0, 0, 0, time.UTC)
	stub := &stubDoer{resps: []stubResp{okPut(), lockedHead("COMPLIANCE", "2027-06-06T12:00:00Z", false)}}
	o := openOutput(t, stub, map[string]string{"prefix": "audit"})

	rec, err := o.Put(context.Background(), "acme/seg-000000000001-000000000010.jsonl", body, PutOptions{RetainUntil: retain})
	if err != nil {
		t.Fatalf("Put: %v", err)
	}
	if len(stub.reqs) != 2 {
		t.Fatalf("requests = %d, want PUT+HEAD", len(stub.reqs))
	}

	put := stub.reqs[0]
	if put.method != http.MethodPut {
		t.Errorf("method = %q, want PUT", put.method)
	}
	if want := "https://worm-bucket.s3.eu-west-1.amazonaws.com/audit/acme/seg-000000000001-000000000010.jsonl"; put.url != want {
		t.Errorf("PUT url = %q, want %q (virtual-host addressing)", put.url, want)
	}
	sum := md5.Sum(body) //nolint:gosec
	if got, want := put.header.Get("Content-MD5"), base64.StdEncoding.EncodeToString(sum[:]); got != want {
		t.Errorf("Content-MD5 = %q, want %q", got, want)
	}
	if got, want := put.header.Get("x-amz-content-sha256"), awssig.HexSHA256(body); got != want {
		t.Errorf("x-amz-content-sha256 = %q, want %q", got, want)
	}
	if got := put.header.Get("x-amz-object-lock-mode"); got != "COMPLIANCE" {
		t.Errorf("lock mode = %q, want COMPLIANCE", got)
	}
	if got := put.header.Get("x-amz-object-lock-retain-until-date"); got != "2027-06-06T12:00:00Z" {
		t.Errorf("retain-until = %q", got)
	}
	auth := put.header.Get("Authorization")
	if !strings.HasPrefix(auth, "AWS4-HMAC-SHA256 Credential=AKIDEXAMPLE/20260606/eu-west-1/s3/aws4_request") {
		t.Errorf("Authorization scope wrong: %q", auth)
	}
	sh := signedHeadersOf(t, auth)
	for _, h := range []string{"content-md5", "content-type", "host", "x-amz-content-sha256", "x-amz-date", "x-amz-object-lock-mode", "x-amz-object-lock-retain-until-date"} {
		if !strings.Contains(sh, h) {
			t.Errorf("SignedHeaders %q is missing %q (the lock headers must be tamper-protected)", sh, h)
		}
	}
	_, sig, ok := strings.Cut(auth, "Signature=")
	if !ok || len(sig) != 64 {
		t.Errorf("Signature missing or not 64 hex chars: %q", auth)
	}

	head := stub.reqs[1]
	if head.method != http.MethodHead {
		t.Errorf("verify method = %q, want HEAD", head.method)
	}
	if !strings.HasSuffix(head.url, "?versionId=ver-1") {
		t.Errorf("HEAD url = %q, want a versionId=ver-1 query (verify the written version)", head.url)
	}
	if head.header.Get("Authorization") == "" {
		t.Error("verify HEAD is not signed")
	}
	if got := head.header.Get("x-amz-content-sha256"); got != emptySHA256 {
		t.Errorf("HEAD x-amz-content-sha256 = %q, want empty-body hash", got)
	}

	want := Receipt{
		Bucket: "worm-bucket", Key: "audit/acme/seg-000000000001-000000000010.jsonl",
		ETag: "etag-abc", VersionID: "ver-1", LockMode: "COMPLIANCE",
		RetainUntil: retain, LockVerified: true,
	}
	if rec != want {
		t.Errorf("receipt = %+v, want %+v", rec, want)
	}
}

func TestPutGovernanceAndLegalHold(t *testing.T) {
	stub := &stubDoer{resps: []stubResp{okPut(), lockedHead("GOVERNANCE", "2026-07-06T12:00:00Z", true)}}
	o := openOutput(t, stub, map[string]string{
		"lock_mode": "governance", "retention_days": "30", "legal_hold": "true",
	})

	rec, err := o.Put(context.Background(), "seg.bin", []byte("x"), PutOptions{})
	if err != nil {
		t.Fatalf("Put: %v", err)
	}
	put := stub.reqs[0]
	if got := put.header.Get("x-amz-object-lock-mode"); got != "GOVERNANCE" {
		t.Errorf("lock mode = %q, want GOVERNANCE", got)
	}
	// retention_days from the fixed clock: 2026-06-06T12:00:00Z + 30d.
	if got := put.header.Get("x-amz-object-lock-retain-until-date"); got != "2026-07-06T12:00:00Z" {
		t.Errorf("retain-until = %q, want 2026-07-06T12:00:00Z (now + retention_days)", got)
	}
	if got := put.header.Get("x-amz-object-lock-legal-hold"); got != "ON" {
		t.Errorf("legal hold header = %q, want ON", got)
	}
	if sh := signedHeadersOf(t, put.header.Get("Authorization")); !strings.Contains(sh, "x-amz-object-lock-legal-hold") {
		t.Errorf("SignedHeaders %q is missing the legal-hold header", sh)
	}
	if rec.LockMode != "GOVERNANCE" || !rec.LockVerified || !rec.RetainUntil.Equal(time.Date(2026, 7, 6, 12, 0, 0, 0, time.UTC)) {
		t.Errorf("receipt = %+v", rec)
	}
}

func TestPutLegalHoldOnly(t *testing.T) {
	stub := &stubDoer{resps: []stubResp{okPut(), lockedHead("", "", true)}}
	o := openOutput(t, stub, map[string]string{"legal_hold": "true"})

	rec, err := o.Put(context.Background(), "seg.bin", []byte("x"), PutOptions{})
	if err != nil {
		t.Fatalf("Put: %v", err)
	}
	put := stub.reqs[0]
	if put.header.Get("x-amz-object-lock-mode") != "" || put.header.Get("x-amz-object-lock-retain-until-date") != "" {
		t.Error("legal-hold-only write must not carry retention headers")
	}
	if put.header.Get("x-amz-object-lock-legal-hold") != "ON" {
		t.Error("legal hold header missing")
	}
	if !rec.LockVerified || rec.LockMode != "" || !rec.RetainUntil.IsZero() {
		t.Errorf("receipt = %+v", rec)
	}
}

func TestVerifyAfterWriteDenies(t *testing.T) {
	retain := time.Date(2027, 1, 1, 0, 0, 0, 0, time.UTC)
	cases := []struct {
		name      string
		overrides map[string]string
		opts      PutOptions
		head      stubResp
		wantErr   string
	}{
		{
			name: "no lock at all (bucket default missing)",
			head: lockedHead("", "", false), wantErr: "no retention and no legal hold",
		},
		{
			name: "wrong mode",
			opts: PutOptions{RetainUntil: retain},
			head: lockedHead("GOVERNANCE", "2027-01-01T00:00:00Z", false), wantErr: `mode "GOVERNANCE"`,
		},
		{
			name: "retain-until earlier than requested",
			opts: PutOptions{RetainUntil: retain},
			head: lockedHead("COMPLIANCE", "2026-12-31T00:00:00Z", false), wantErr: "earlier than requested",
		},
		{
			name:      "legal hold not on",
			overrides: map[string]string{"legal_hold": "true"},
			head:      lockedHead("", "", false), wantErr: "legal hold is not ON",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			stub := &stubDoer{resps: []stubResp{okPut(), tc.head}}
			o := openOutput(t, stub, tc.overrides)
			rec, err := o.Put(context.Background(), "seg.bin", []byte("x"), tc.opts)
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("Put = (%+v, %v), want error containing %q", rec, err, tc.wantErr)
			}
			if rec != (Receipt{}) {
				t.Errorf("a failed verify must not return a receipt, got %+v", rec)
			}
		})
	}
}

func TestVerifyDisabledSkipsHead(t *testing.T) {
	stub := &stubDoer{resps: []stubResp{okPut()}}
	o := openOutput(t, stub, map[string]string{"verify_lock": "false", "retention_days": "30"})

	rec, err := o.Put(context.Background(), "seg.bin", []byte("x"), PutOptions{})
	if err != nil {
		t.Fatalf("Put: %v", err)
	}
	if len(stub.reqs) != 1 {
		t.Fatalf("requests = %d, want only the PUT", len(stub.reqs))
	}
	if rec.LockVerified {
		t.Error("LockVerified must be false when verification did not run")
	}
	if rec.LockMode != "COMPLIANCE" || rec.VersionID != "ver-1" {
		t.Errorf("receipt = %+v", rec)
	}
}

func TestPutRetriesTransientThenSucceeds(t *testing.T) {
	stub := &stubDoer{resps: []stubResp{
		{status: 500, body: "InternalError"},
		okPut(),
		lockedHead("COMPLIANCE", "2026-07-06T12:00:00Z", false),
	}}
	o := openOutput(t, stub, map[string]string{"retention_days": "30"})

	if _, err := o.Put(context.Background(), "seg.bin", []byte("x"), PutOptions{}); err != nil {
		t.Fatalf("Put after transient 500: %v", err)
	}
	if len(stub.reqs) != 3 {
		t.Fatalf("requests = %d, want PUT(500)+PUT(200)+HEAD", len(stub.reqs))
	}
	if string(stub.reqs[0].body) != string(stub.reqs[1].body) {
		t.Error("retry must resend the body verbatim")
	}
}

func TestPutTerminal403DoesNotRetry(t *testing.T) {
	stub := &stubDoer{resps: []stubResp{{status: 403, body: "AccessDenied"}}}
	o := openOutput(t, stub, map[string]string{"retention_days": "30"})

	_, err := o.Put(context.Background(), "seg.bin", []byte("x"), PutOptions{})
	if err == nil || !strings.Contains(err.Error(), "403") {
		t.Fatalf("want a 403 error, got %v", err)
	}
	if len(stub.reqs) != 1 {
		t.Fatalf("requests = %d, want 1 (4xx is terminal)", len(stub.reqs))
	}
}

func TestPutPathStyleForCustomEndpoint(t *testing.T) {
	stub := &stubDoer{resps: []stubResp{okPut(), lockedHead("COMPLIANCE", "2026-07-06T12:00:00Z", false)}}
	o := openOutput(t, stub, map[string]string{"endpoint": "https://minio.local:9000", "retention_days": "30"})

	if _, err := o.Put(context.Background(), "seg.bin", []byte("x"), PutOptions{}); err != nil {
		t.Fatalf("Put: %v", err)
	}
	if want := "https://minio.local:9000/worm-bucket/seg.bin"; stub.reqs[0].url != want {
		t.Errorf("PUT url = %q, want %q (path-style for a custom endpoint)", stub.reqs[0].url, want)
	}
}

func TestPutContentSHA256MismatchAborts(t *testing.T) {
	stub := &stubDoer{resps: []stubResp{okPut()}}
	o := openOutput(t, stub, nil)

	_, err := o.Put(context.Background(), "seg.bin", []byte("x"), PutOptions{ContentSHA256: "deadbeef"})
	if err == nil || !strings.Contains(err.Error(), "hash mismatch") {
		t.Fatalf("want a content-hash mismatch error, got %v", err)
	}
	if len(stub.reqs) != 0 {
		t.Fatalf("a hash mismatch must abort before any request, sent %d", len(stub.reqs))
	}
}

func TestNotifyJSONObjectKeyAndBody(t *testing.T) {
	stub := &stubDoer{resps: []stubResp{okPut(), lockedHead("COMPLIANCE", "2026-07-06T12:00:00Z", false)}}
	o := openOutput(t, stub, map[string]string{"prefix": "audit/", "retention_days": "30"})

	if err := o.Notify(context.Background(), sampleNotification()); err != nil {
		t.Fatalf("Notify: %v", err)
	}
	put := stub.reqs[0]
	// The key uses the notification's OWN time (10:00), not the wall clock
	// (12:00); the RFC3339 colons travel URI-encoded so the signed path and the
	// wire path agree.
	if want := "https://worm-bucket.s3.eu-west-1.amazonaws.com/audit/notifications/acme/2026-06-06T10%3A00%3A00Z-finding.reported.json"; put.url != want {
		t.Errorf("Notify url = %q, want %q", put.url, want)
	}
	if got := put.header.Get("Content-Type"); got != "application/json" {
		t.Errorf("Content-Type = %q", got)
	}
	if got := put.header.Get("x-amz-object-lock-mode"); got != "COMPLIANCE" {
		t.Errorf("a notification object must be locked too; mode = %q", got)
	}
	want := `{"type":"finding.reported","title":"secret write blocked","body":"claude-1 denied","severity":"high","tenant":"acme","fields":{"agent":"claude-1"},"time":"2026-06-06T10:00:00Z"}`
	if string(put.body) != want {
		t.Errorf("body = %s, want %s", put.body, want)
	}
}

func TestNotifyCEFFormat(t *testing.T) {
	stub := &stubDoer{resps: []stubResp{okPut(), lockedHead("COMPLIANCE", "2026-07-06T12:00:00Z", false)}}
	o := openOutput(t, stub, map[string]string{"format": "cef", "retention_days": "30"})

	n := sampleNotification()
	n.Time = time.Date(2020, 1, 2, 3, 4, 5, 0, time.UTC)
	if err := o.Notify(context.Background(), n); err != nil {
		t.Fatalf("Notify: %v", err)
	}
	put := stub.reqs[0]
	if !strings.HasPrefix(string(put.body), "CEF:0|") {
		t.Errorf("body is not CEF: %q", put.body)
	}
	if got := put.header.Get("Content-Type"); got != "text/plain" {
		t.Errorf("Content-Type = %q", got)
	}
	if !strings.Contains(put.url, "/notifications/acme/2020-01-02T03%3A04%3A05Z-finding.reported.cef") {
		t.Errorf("Notify url = %q, want the notification time and a .cef extension", put.url)
	}
}

func TestSecretsNeverInErrors(t *testing.T) {
	for name, resp := range map[string]stubResp{
		"transport error": {err: errors.New("dial tcp: connection refused")},
		"terminal 403":    {status: 403, body: "AccessDenied"},
	} {
		t.Run(name, func(t *testing.T) {
			stub := &stubDoer{resps: []stubResp{resp}}
			o := openOutput(t, stub, map[string]string{"session_token": "TOKSESSION", "retention_days": "30"})
			_, err := o.Put(context.Background(), "seg.bin", []byte("x"), PutOptions{})
			if err == nil {
				t.Fatal("want an error")
			}
			for _, secret := range []string{"SUPERSECRETKEY", "TOKSESSION"} {
				if strings.Contains(err.Error(), secret) {
					t.Errorf("error leaks a credential: %v", err)
				}
			}
		})
	}
}

func TestNotOpened(t *testing.T) {
	o := New()
	if _, err := o.Put(context.Background(), "k", nil, PutOptions{}); err == nil || !strings.Contains(err.Error(), "not opened") {
		t.Errorf("Put on an unopened connector = %v", err)
	}
	if err := o.Notify(context.Background(), sdk.Notification{}); err == nil || !strings.Contains(err.Error(), "not opened") {
		t.Errorf("Notify on an unopened connector = %v", err)
	}
}

func TestOpenRejectsBadConfig(t *testing.T) {
	base := func() map[string]string {
		return map[string]string{
			"region":            "eu-west-1",
			"bucket":            "worm-bucket",
			"access_key_id":     "AKIDEXAMPLE",
			"secret_access_key": "S",
		}
	}
	cases := map[string]map[string]string{
		"missing region":         {"region": ""},
		"missing bucket":         {"bucket": ""},
		"invalid bucket":         {"bucket": "Bad/Name"},
		"missing access key":     {"access_key_id": ""},
		"missing secret":         {"secret_access_key": ""},
		"unknown lock_mode":      {"lock_mode": "worm"},
		"unknown format":         {"format": "xml"},
		"non-http endpoint":      {"endpoint": "ftp://minio.local"},
		"negative retention":     {"retention_days": "-1"},
		"non-numeric retention":  {"retention_days": "abc"},
		"oversized retention":    {"retention_days": "40000"},
		"non-boolean legal_hold": {"legal_hold": "maybe"},
		"non-boolean verify":     {"verify_lock": "nope"},
	}
	for name, override := range cases {
		t.Run(name, func(t *testing.T) {
			settings := base()
			for k, v := range override {
				settings[k] = v
			}
			if err := New().Open(context.Background(), sdk.Config{Settings: settings}); err == nil {
				t.Errorf("Open(%v) = nil, want error", override)
			}
		})
	}
	if err := New().Open(context.Background(), sdk.Config{Settings: base()}); err != nil {
		t.Fatalf("base config must open: %v", err)
	}
}

// TestSignV4KnownAnswer pins the local signer to the same AWS SigV4 test-suite
// vector awssig pins: with no extra headers it must reduce to awssig.Sign
// byte for byte (same canonical request, same documented signature).
func TestSignV4KnownAnswer(t *testing.T) {
	req, _ := http.NewRequest(http.MethodGet, "https://example.amazonaws.com/", nil)
	req.Host = "example.amazonaws.com"
	when := time.Date(2015, 8, 30, 12, 36, 0, 0, time.UTC)
	signV4(req, "service", "us-east-1",
		awssig.Creds{AKID: "AKIDEXAMPLE", Secret: "wJalrXUtnFEMI/K7MDENG+bPxRfiCYEXAMPLEKEY"}, when)

	auth := req.Header.Get("Authorization")
	if !strings.HasPrefix(auth, "AWS4-HMAC-SHA256 Credential=AKIDEXAMPLE/20150830/us-east-1/service/aws4_request") {
		t.Fatalf("authorization scope wrong: %s", auth)
	}
	const wantSig = "Signature=5fa00fa31553b73ebf1942676e86291e8372ff2a2260956d9b8aae1d763fbf31"
	if !strings.Contains(auth, wantSig) {
		t.Fatalf("signature mismatch:\n got %s\n want ...%s", auth, wantSig)
	}
	if got := signedHeadersOf(t, auth); got != "host;x-amz-date" {
		t.Fatalf("SignedHeaders = %q, want host;x-amz-date", got)
	}
}

func TestSignV4SignsSessionToken(t *testing.T) {
	req, _ := http.NewRequest(http.MethodGet, "https://example.amazonaws.com/", nil)
	signV4(req, "s3", "eu-west-1", awssig.Creds{AKID: "A", Secret: "S", Token: "FQoG-session"}, fixedNow)
	if req.Header.Get("X-Amz-Security-Token") != "FQoG-session" {
		t.Fatal("session token header not set")
	}
	if !strings.Contains(signedHeadersOf(t, req.Header.Get("Authorization")), "x-amz-security-token") {
		t.Fatal("session token must be a signed header")
	}
}
