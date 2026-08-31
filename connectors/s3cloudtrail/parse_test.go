// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package s3cloudtrail

import (
	"testing"

	"github.com/olivaresai/olivares/connectors/internal/identity"
	"github.com/olivaresai/olivares/sdk/model"
)

func boolPtr(b bool) *bool { return &b }

func TestClassifyMode(t *testing.T) {
	if classifyMode(nil) != model.ModeUnknown {
		t.Error("nil readOnly should be unknown")
	}
	if classifyMode(boolPtr(true)) != model.ModeRead {
		t.Error("readOnly=true should be read")
	}
	if classifyMode(boolPtr(false)) != model.ModeWrite {
		t.Error("readOnly=false should be write")
	}
}

func TestResolveResource(t *testing.T) {
	t.Run("prefers object resource", func(t *testing.T) {
		r := record{Resources: []resourceRef{
			{Type: "AWS::S3::Bucket", ARN: "arn:aws:s3:::b"},
			{Type: "AWS::S3::Object", ARN: "arn:aws:s3:::b/k"},
		}}
		kind, ref, ok := resolveResource(r)
		if !ok || kind != "s3.object" || ref != "arn:aws:s3:::b/k" {
			t.Errorf("got (%q,%q,%v)", kind, ref, ok)
		}
	})
	t.Run("bucket only", func(t *testing.T) {
		r := record{Resources: []resourceRef{{Type: "AWS::S3::Bucket", ARN: "arn:aws:s3:::b"}}}
		kind, ref, ok := resolveResource(r)
		if !ok || kind != "s3.bucket" || ref != "arn:aws:s3:::b" {
			t.Errorf("got (%q,%q,%v)", kind, ref, ok)
		}
	})
	t.Run("fallback to requestParameters object", func(t *testing.T) {
		r := record{RequestParameters: requestParameters{BucketName: "b", Key: "k/k2"}}
		kind, ref, ok := resolveResource(r)
		if !ok || kind != "s3.object" || ref != "arn:aws:s3:::b/k/k2" {
			t.Errorf("got (%q,%q,%v)", kind, ref, ok)
		}
	})
	t.Run("fallback to requestParameters bucket", func(t *testing.T) {
		r := record{RequestParameters: requestParameters{BucketName: "b"}}
		kind, ref, ok := resolveResource(r)
		if !ok || kind != "s3.bucket" || ref != "arn:aws:s3:::b" {
			t.Errorf("got (%q,%q,%v)", kind, ref, ok)
		}
	})
	t.Run("no resource", func(t *testing.T) {
		if _, _, ok := resolveResource(record{}); ok {
			t.Error("empty record should have no resource")
		}
	})
}

func TestResolveIdentity(t *testing.T) {
	shared := &Source{shared: identity.ParseSharedAccounts("arn:aws:iam::123456789012:role/AppRole")}
	cases := []struct {
		name     string
		ui       userIdentity
		wantRef  string
		wantConf model.Confidence
		wantOK   bool
	}{
		{
			name:     "iam user attributed",
			ui:       userIdentity{Type: "IAMUser", ARN: "arn:aws:iam::1:user/a"},
			wantRef:  "arn:aws:iam::1:user/a",
			wantConf: model.ConfidenceAttributed, wantOK: true,
		},
		{
			name: "assumed role shared -> approximate",
			ui: userIdentity{
				Type: "AssumedRole", ARN: "arn:aws:sts::123456789012:assumed-role/AppRole/s1",
				SessionContext: sessionContext{SessionIssuer: sessionIssuer{ARN: "arn:aws:iam::123456789012:role/AppRole"}},
			},
			wantRef:  "arn:aws:sts::123456789012:assumed-role/AppRole/s1",
			wantConf: model.ConfidenceApproximate, wantOK: true,
		},
		{
			name: "assumed role not shared -> attributed",
			ui: userIdentity{
				Type: "AssumedRole", ARN: "arn:aws:sts::1:assumed-role/Other/s2",
				SessionContext: sessionContext{SessionIssuer: sessionIssuer{ARN: "arn:aws:iam::1:role/Other"}},
			},
			wantRef:  "arn:aws:sts::1:assumed-role/Other/s2",
			wantConf: model.ConfidenceAttributed, wantOK: true,
		},
		{
			name:     "aws service -> approximate",
			ui:       userIdentity{Type: "AWSService", InvokedBy: "replication.s3.amazonaws.com"},
			wantRef:  "replication.s3.amazonaws.com",
			wantConf: model.ConfidenceApproximate, wantOK: true,
		},
		{
			name:     "aws account -> approximate",
			ui:       userIdentity{Type: "AWSAccount", AccountID: "999", PrincipalID: "ANON"},
			wantRef:  "ANON",
			wantConf: model.ConfidenceApproximate, wantOK: true,
		},
		{
			name:   "no identity reference",
			ui:     userIdentity{Type: "IAMUser"},
			wantOK: false,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			ref, conf, ok := shared.resolveIdentity(c.ui)
			if ok != c.wantOK {
				t.Fatalf("ok = %v, want %v", ok, c.wantOK)
			}
			if ok && (ref != c.wantRef || conf != c.wantConf) {
				t.Errorf("got (%q,%q), want (%q,%q)", ref, conf, c.wantRef, c.wantConf)
			}
		})
	}
}

func TestRecordsFromBytes(t *testing.T) {
	t.Run("records wrapper", func(t *testing.T) {
		recs := recordsFromBytes([]byte(`{"Records":[{"eventSource":"s3.amazonaws.com"},{"eventSource":"s3.amazonaws.com"}]}`))
		if len(recs) != 2 {
			t.Fatalf("got %d", len(recs))
		}
	})
	t.Run("ndjson", func(t *testing.T) {
		recs := recordsFromBytes([]byte("{\"eventSource\":\"s3.amazonaws.com\"}\n{\"eventSource\":\"s3.amazonaws.com\"}\n"))
		if len(recs) != 2 {
			t.Fatalf("got %d", len(recs))
		}
	})
	t.Run("single object", func(t *testing.T) {
		recs := recordsFromBytes([]byte(`{"eventSource":"s3.amazonaws.com","eventName":"GetObject"}`))
		if len(recs) != 1 {
			t.Fatalf("got %d", len(recs))
		}
	})
	t.Run("empty", func(t *testing.T) {
		if recs := recordsFromBytes([]byte(`{"Records":[]}`)); len(recs) != 0 {
			t.Fatalf("got %d, want 0", len(recs))
		}
	})
}

func TestFirstNonEmpty(t *testing.T) {
	if firstNonEmpty("", "", "x", "y") != "x" {
		t.Error("should return first non-empty")
	}
	if firstNonEmpty("", "") != "" {
		t.Error("all empty should be empty")
	}
}

func TestParseTime(t *testing.T) {
	if _, ok := parseTime("2026-06-03T10:23:45Z"); !ok {
		t.Error("RFC3339 should parse")
	}
	if _, ok := parseTime("nope"); ok {
		t.Error("garbage should not parse")
	}
}
