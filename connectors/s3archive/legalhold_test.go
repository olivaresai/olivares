// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package s3archive

import (
	"context"
	"net/http"
	"strings"
	"testing"
)

func TestSetObjectLegalHoldOnVerified(t *testing.T) {
	stub := &stubDoer{resps: []stubResp{
		{status: 200, header: map[string]string{"x-amz-version-id": "ver-1"}},                                             // PUT ?legal-hold
		{status: 200, body: `<LegalHold xmlns="http://s3.amazonaws.com/doc/2006-03-01/"><Status>ON</Status></LegalHold>`}, // GET verify
	}}
	o := openOutput(t, stub, map[string]string{"prefix": "audit/"})

	rec, err := o.SetObjectLegalHold(context.Background(), "acme/seg-000000000001-000000000010.jsonl", "ver-1", true)
	if err != nil {
		t.Fatalf("SetObjectLegalHold: %v", err)
	}
	if !rec.LockVerified {
		t.Fatalf("a verified hold must report LockVerified=true: %+v", rec)
	}
	if len(stub.reqs) != 2 {
		t.Fatalf("want PUT + GET, got %d requests", len(stub.reqs))
	}
	put := stub.reqs[0]
	if put.method != http.MethodPut {
		t.Fatalf("first request must be PUT, got %s", put.method)
	}
	if !strings.Contains(put.url, "legal-hold") || !strings.Contains(put.url, "versionId=ver-1") {
		t.Fatalf("PUT url must address ?legal-hold for the version: %s", put.url)
	}
	if !strings.Contains(put.url, "audit/acme/seg-") {
		t.Fatalf("connector prefix must be applied: %s", put.url)
	}
	if !strings.Contains(string(put.body), "<Status>ON</Status>") {
		t.Fatalf("PUT body must request ON: %s", put.body)
	}
	// The Content-MD5 (write integrity) must be in the SigV4 signed-header set.
	if sh := signedHeadersOf(t, put.header.Get("Authorization")); !strings.Contains(sh, "content-md5") {
		t.Fatalf("content-md5 must be signed: %s", sh)
	}
	if stub.reqs[1].method != http.MethodGet {
		t.Fatalf("second request must be the verify GET, got %s", stub.reqs[1].method)
	}
}

func TestSetObjectLegalHoldVerifyMismatchFailsClosed(t *testing.T) {
	stub := &stubDoer{resps: []stubResp{
		{status: 200, header: map[string]string{"x-amz-version-id": "ver-1"}},
		{status: 200, body: `<LegalHold><Status>OFF</Status></LegalHold>`}, // reads back OFF
	}}
	o := openOutput(t, stub, nil)
	if _, err := o.SetObjectLegalHold(context.Background(), "acme/seg-000000000001-000000000010.jsonl", "ver-1", true); err == nil {
		t.Fatal("a hold that reads back OFF must be a fail-closed error, not a receipt")
	}
}

func TestSetObjectLegalHoldNoVerify(t *testing.T) {
	stub := &stubDoer{resps: []stubResp{{status: 200, header: map[string]string{"x-amz-version-id": "ver-1"}}}}
	o := openOutput(t, stub, map[string]string{"verify_lock": "false"})
	rec, err := o.SetObjectLegalHold(context.Background(), "acme/seg-000000000001-000000000010.jsonl", "", false)
	if err != nil {
		t.Fatalf("SetObjectLegalHold: %v", err)
	}
	if rec.LockVerified {
		t.Fatalf("verify_lock off ⇒ LockVerified must be false: %+v", rec)
	}
	if len(stub.reqs) != 1 {
		t.Fatalf("verify off ⇒ exactly one request, got %d", len(stub.reqs))
	}
	if !strings.Contains(string(stub.reqs[0].body), "<Status>OFF</Status>") {
		t.Fatalf("clearing a hold must request OFF: %s", stub.reqs[0].body)
	}
}

func TestListObjectVersionsPaginatesAndFiltersByPrefix(t *testing.T) {
	// Real S3 echoes the FULL key INCLUDING the connector prefix ("audit/").
	page1 := `<ListVersionsResult xmlns="http://s3.amazonaws.com/doc/2006-03-01/">
		<IsTruncated>true</IsTruncated>
		<NextKeyMarker>audit/acme/seg-000000000001-000000000010.jsonl</NextKeyMarker>
		<NextVersionIdMarker>ver-1</NextVersionIdMarker>
		<Version><Key>audit/acme/seg-000000000001-000000000010.jsonl</Key><VersionId>ver-1</VersionId><IsLatest>true</IsLatest></Version>
	</ListVersionsResult>`
	page2 := `<ListVersionsResult xmlns="http://s3.amazonaws.com/doc/2006-03-01/">
		<IsTruncated>false</IsTruncated>
		<Version><Key>audit/acme/seg-000000000011-000000000020.jsonl</Key><VersionId>ver-2</VersionId><IsLatest>true</IsLatest></Version>
		<Version><Key>audit/acme/seg-000000000001-000000000010.jsonl.manifest.json</Key><VersionId>ver-3</VersionId><IsLatest>true</IsLatest></Version>
	</ListVersionsResult>`
	stub := &stubDoer{resps: []stubResp{{status: 200, body: page1}, {status: 200, body: page2}}}
	o := openOutput(t, stub, map[string]string{"prefix": "audit/"})

	vers, err := o.ListObjectVersions(context.Background(), "acme/")
	if err != nil {
		t.Fatalf("ListObjectVersions: %v", err)
	}
	if len(vers) != 3 {
		t.Fatalf("want 3 versions across 2 pages, got %d: %+v", len(vers), vers)
	}
	// Keys come back CONNECTOR-RELATIVE (prefix stripped), so they round-trip through
	// ParseSegmentKey + SetObjectLegalHold (which re-adds the prefix). A returned key that
	// still carried "audit/" would make the segment-key parse drop every segment.
	if vers[0].Key != "acme/seg-000000000001-000000000010.jsonl" {
		t.Fatalf("listed key must be connector-relative (prefix stripped), got %q", vers[0].Key)
	}
	if len(stub.reqs) != 2 {
		t.Fatalf("a truncated first page must drive a second request, got %d", len(stub.reqs))
	}
	if !strings.Contains(stub.reqs[0].url, "versions=") {
		t.Fatalf("the ?versions subresource must be requested: %s", stub.reqs[0].url)
	}
	if !strings.Contains(stub.reqs[0].url, "prefix=audit%2Facme%2F") {
		t.Fatalf("the connector prefix must be prepended to the list prefix: %s", stub.reqs[0].url)
	}
	if !strings.Contains(stub.reqs[1].url, "key-marker=") || !strings.Contains(stub.reqs[1].url, "version-id-marker=ver-1") {
		t.Fatalf("the second page must carry the continuation markers: %s", stub.reqs[1].url)
	}
}
