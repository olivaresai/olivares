// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package s3content

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/olivaresai/olivares/connectors/contentsource"
	"github.com/olivaresai/olivares/connectors/internal/awssig"
	"github.com/olivaresai/olivares/sdk"
)

func openLiveS3Source(t *testing.T, handler http.Handler) (*Source, func()) {
	t.Helper()
	srv := httptest.NewServer(handler)
	s := New()
	if err := s.Open(context.Background(), sdk.Config{Settings: map[string]string{
		"mode":              "live",
		"bucket":            "bucket",
		"prefix":            "docs/",
		"region":            "us-east-1",
		"endpoint":          srv.URL,
		"access_key_id":     "AKIDEXAMPLE",
		"secret_access_key": "wJalrXUtnFEMI/K7MDENG+bPxRfiCYEXAMPLEKEY",
	}}); err != nil {
		srv.Close()
		t.Fatalf("Open: %v", err)
	}
	s.live.now = func() time.Time { return time.Date(2026, 7, 1, 12, 0, 0, 0, time.UTC) }
	return s, srv.Close
}

func TestS3ContentLiveListPaginatesAndSigns(t *testing.T) {
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.Header.Get("Authorization"), "AWS4-HMAC-SHA256") {
			http.Error(w, "missing SigV4", http.StatusUnauthorized)
			return
		}
		if r.URL.Path != "/bucket" {
			http.Error(w, "unexpected path "+r.URL.Path, http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/xml")
		if r.URL.Query().Get("continuation-token") == "token-2" {
			_, _ = w.Write([]byte(`<ListBucketResult><IsTruncated>false</IsTruncated><Contents><Key>docs/two.txt</Key><LastModified>2026-07-01T11:00:00Z</LastModified></Contents></ListBucketResult>`))
			return
		}
		if r.URL.Query().Get("prefix") != "docs/" {
			http.Error(w, "missing prefix", http.StatusBadRequest)
			return
		}
		_, _ = w.Write([]byte(`<ListBucketResult><IsTruncated>true</IsTruncated><NextContinuationToken>token-2</NextContinuationToken><Contents><Key>docs/one.md</Key><LastModified>2026-07-01T10:00:00Z</LastModified></Contents></ListBucketResult>`))
	})
	s, cleanup := openLiveS3Source(t, handler)
	defer cleanup()

	refs, next, err := s.List(context.Background(), "")
	if err != nil {
		t.Fatalf("List page1: %v", err)
	}
	if len(refs) != 1 || refs[0].DocID != "docs/one.md" || refs[0].Title != "one.md" || refs[0].ContentType != "text/markdown; charset=utf-8" || next != "token-2" {
		t.Fatalf("page1 refs/next = %+v/%q", refs, next)
	}
	refs, next, err = s.List(context.Background(), next)
	if err != nil {
		t.Fatalf("List page2: %v", err)
	}
	if len(refs) != 1 || refs[0].DocID != "docs/two.txt" || refs[0].Title != "two.txt" || next != "" {
		t.Fatalf("page2 refs/next = %+v/%q", refs, next)
	}
}

func TestS3ContentLiveFetchWithACLAndTags(t *testing.T) {
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.Header.Get("Authorization"), "AWS4-HMAC-SHA256") {
			http.Error(w, "missing SigV4", http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/xml")
		switch {
		case r.URL.Path == "/bucket/docs/handbook.md" && r.URL.RawQuery == "":
			w.Header().Set("Content-Type", "text/markdown")
			_, _ = w.Write([]byte("# Handbook"))
		case r.URL.Path == "/bucket/docs/handbook.md" && r.URL.RawQuery == "acl":
			_, _ = w.Write([]byte(`<AccessControlPolicy><AccessControlList>
				<Grant><Grantee xmlns:xsi="http://www.w3.org/2001/XMLSchema-instance" xsi:type="CanonicalUser"><ID>abc123</ID></Grantee></Grant>
				<Grant><Grantee xmlns:xsi="http://www.w3.org/2001/XMLSchema-instance" xsi:type="Group"><URI>http://acs.amazonaws.com/groups/global/AuthenticatedUsers</URI></Grantee></Grant>
				<Grant><Grantee xmlns:xsi="http://www.w3.org/2001/XMLSchema-instance" xsi:type="AmazonCustomerByEmail"><EmailAddress>team@example.com</EmailAddress></Grantee></Grant>
			</AccessControlList></AccessControlPolicy>`))
		case r.URL.Path == "/bucket/docs/handbook.md" && r.URL.RawQuery == "tagging":
			_, _ = w.Write([]byte(`<Tagging><TagSet><Tag><Key>Classification</Key><Value>internal</Value></Tag></TagSet></Tagging>`))
		default:
			http.Error(w, "unexpected request "+r.URL.String(), http.StatusNotFound)
		}
	})
	s, cleanup := openLiveS3Source(t, handler)
	defer cleanup()

	doc, err := s.Fetch(context.Background(), "docs/handbook.md")
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if doc.Source != contentsource.SourceS3 || doc.Title != "handbook.md" || doc.Body != "# Handbook" {
		t.Fatalf("doc = %+v", doc)
	}
	if doc.Classification != "internal" {
		t.Fatalf("Classification = %q, want internal", doc.Classification)
	}
	wantACL := "user:abc123,group:AuthenticatedUsers,email:team@example.com"
	if got := strings.Join(doc.ACL, ","); got != wantACL {
		t.Fatalf("ACL = %q, want %q", got, wantACL)
	}
}

func TestS3ContentDeltaFilteringTokenRoundTripAndExpired(t *testing.T) {
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/xml")
		if r.URL.Query().Get("continuation-token") == "token-2" {
			_, _ = w.Write([]byte(`<ListBucketResult><IsTruncated>false</IsTruncated><Contents><Key>docs/newer.txt</Key><LastModified>2026-07-01T12:00:00Z</LastModified></Contents></ListBucketResult>`))
			return
		}
		_, _ = w.Write([]byte(`<ListBucketResult><IsTruncated>true</IsTruncated><NextContinuationToken>token-2</NextContinuationToken>
			<Contents><Key>docs/old.txt</Key><LastModified>2026-07-01T09:00:00Z</LastModified></Contents>
			<Contents><Key>docs/new.txt</Key><LastModified>2026-07-01T11:00:00Z</LastModified></Contents>
		</ListBucketResult>`))
	})
	s, cleanup := openLiveS3Source(t, handler)
	defer cleanup()

	page1, err := s.DeltaList(context.Background(), "2026-07-01T10:30:00Z")
	if err != nil {
		t.Fatalf("DeltaList page1: %v", err)
	}
	if len(page1.Changes) != 1 || page1.Changes[0].DocRef.DocID != "docs/new.txt" || page1.NextToken == "" || page1.ResumeToken != "" {
		t.Fatalf("page1 = %+v", page1)
	}
	page2, err := s.DeltaList(context.Background(), page1.NextToken)
	if err != nil {
		t.Fatalf("DeltaList page2: %v", err)
	}
	if len(page2.Changes) != 1 || page2.Changes[0].DocRef.DocID != "docs/newer.txt" || page2.NextToken != "" || page2.ResumeToken != "2026-07-01T12:00:00Z" {
		t.Fatalf("page2 = %+v", page2)
	}
	expired, err := s.DeltaList(context.Background(), "not-a-token")
	if err != nil {
		t.Fatalf("DeltaList garbage: %v", err)
	}
	if !expired.Expired {
		t.Fatal("garbage token should force Expired=true")
	}
}

func TestS3ContentURLConstructionPathStyleAndVirtualHost(t *testing.T) {
	pathStyle := &liveClient{endpoint: "https://objects.example.com", bucket: "bucket", pathStyle: true}
	if got := pathStyle.s3URL("docs/a b.txt", "acl"); got != "https://objects.example.com/bucket/docs/a%20b.txt?acl" {
		t.Fatalf("path-style URL = %q", got)
	}
	virtual := &liveClient{endpoint: "https://s3.us-east-1.amazonaws.com", bucket: "bucket", pathStyle: false}
	if got := virtual.s3URL("docs/a.txt", ""); got != "https://bucket.s3.us-east-1.amazonaws.com/docs/a.txt" {
		t.Fatalf("virtual-host URL = %q", got)
	}
}

func TestS3ContentLiveModeRequiresBucket(t *testing.T) {
	s := New()
	err := s.Open(context.Background(), sdk.Config{Settings: map[string]string{
		"mode": "live",
	}})
	if err == nil || !strings.Contains(err.Error(), "bucket") {
		t.Fatalf("expected bucket error, got %v", err)
	}
}

func TestS3ContentExportModeStillWorks(t *testing.T) {
	s := New()
	if err := s.Open(context.Background(), sdk.Config{Settings: map[string]string{"export_path": "testdata/s3.json"}}); err != nil {
		t.Fatal(err)
	}
	refs, next, err := s.List(context.Background(), "")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(refs) != 1 || refs[0].DocID != "acme-knowledge/docs/handbook.md" || next != "" {
		t.Fatalf("List refs/next = %+v/%q", refs, next)
	}
	if _, err := s.DeltaList(context.Background(), ""); err == nil {
		t.Fatal("expected DeltaList to fail in export mode")
	}
}

func TestS3ContentLiveClientUsesEnvCredentialFallbacks(t *testing.T) {
	t.Setenv(envAccessKeyID, "env-akid")
	t.Setenv(envSecretAccessKey, "env-secret")
	s := New()
	if err := s.Open(context.Background(), sdk.Config{Settings: map[string]string{
		"mode":   "live",
		"bucket": "bucket",
	}}); err != nil {
		t.Fatalf("Open: %v", err)
	}
	if s.live.creds != (awssig.Creds{AKID: "env-akid", Secret: "env-secret"}) {
		t.Fatalf("creds = %+v", s.live.creds)
	}
}

func TestS3ContentLiveFetchEncodesSpecialCharacterKeys(t *testing.T) {
	// A key with bytes Go's default path escaping leaves raw ('+', '=', '$',
	// space) must reach the wire strictly AWS-URI-encoded, or S3 answers
	// SignatureDoesNotMatch. The fake asserts the exact escaped form.
	const key = "docs/a b+c=d$e.txt"
	const wantPath = "/bucket/docs/a%20b%2Bc%3Dd%24e.txt"
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.Header.Get("Authorization"), "AWS4-HMAC-SHA256") {
			http.Error(w, "missing SigV4", http.StatusUnauthorized)
			return
		}
		if got := r.URL.EscapedPath(); got != wantPath {
			http.Error(w, "unexpected escaped path "+got, http.StatusNotFound)
			return
		}
		switch r.URL.RawQuery {
		case "acl":
			w.Header().Set("Content-Type", "application/xml")
			_, _ = w.Write([]byte(`<AccessControlPolicy><AccessControlList></AccessControlList></AccessControlPolicy>`))
		case "tagging":
			w.Header().Set("Content-Type", "application/xml")
			_, _ = w.Write([]byte(`<Tagging><TagSet></TagSet></Tagging>`))
		default:
			w.Header().Set("Content-Type", "text/plain")
			_, _ = w.Write([]byte("body"))
		}
	})
	s, cleanup := openLiveS3Source(t, handler)
	defer cleanup()

	doc, err := s.Fetch(context.Background(), key)
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if doc.Body != "body" || doc.Title != "a b+c=d$e.txt" {
		t.Fatalf("doc = %+v", doc)
	}
}
