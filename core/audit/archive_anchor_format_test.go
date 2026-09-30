// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package audit_test

import (
	"context"
	"crypto/ed25519"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/core/audit"
)

// anchorTestResult builds a minimal SegmentResult whose events receipt carries
// the given sink-supplied object identifiers.
func anchorTestResult(etag, versionID string) audit.SegmentResult {
	return audit.SegmentResult{
		Manifest: audit.SegmentManifest{
			Format: "olivares.audit.segment/1", Tenant: "t1",
			FromSeq: 1, ToSeq: 3, Count: 3,
			FirstHash: "aa", LastHash: "bb", EventsSHA256: "cc",
			CreatedAt: "2026-09-30T00:00:02Z",
		},
		EventsKey: "t1/seg-000000000001-000000000003.jsonl",
		EventsReceipt: audit.ArchiveReceipt{
			Location:  "azure://bucket/t1/seg-000000000001-000000000003.jsonl",
			ETag:      etag,
			VersionID: versionID,
		},
	}
}

// TestAnchorSegmentRefusesHostileObjectIdentifier is the H-04 regression test
// (26.10.x security backlog): a broken or hostile archive substrate/proxy can
// answer a Put with an echoed credential (a SAS query, a bearer token) in the
// ETag/VersionID headers, and those strings used to be stored verbatim into
// anchor metadata readable by any tenant audit:read caller. The anchor must be
// REFUSED on a value that is not a well-formed object identifier, and the
// refusal must name the field and the rule, never the value.
func TestAnchorSegmentRefusesHostileObjectIdentifier(t *testing.T) {
	ctx := context.Background()
	_, priv, _ := ed25519.GenerateKey(nil)
	signer, _ := audit.NewSigner(priv)
	st := signedStore(t, signer)
	tenant := provisionTenant(t, st)

	sasShaped := "sig=Ab9xQ2credentialmaterial%2F%3D&se=2027-01-01T00:00:00Z&sp=r&sv=2024-11-04&sr=b" +
		"&continuation=" + strings.Repeat("a", 96)
	cases := []struct {
		name      string
		etag      string
		versionID string
		field     string
		secret    string
	}{
		{"etag with whitespace", `"0x8DCC 12A34BEF001"`, "", "archive.etag", "12A34BEF001"},
		{"etag oversized sas-shaped", sasShaped, "", "archive.etag", "credentialmaterial"},
		{"etag with control byte", "\"zzqsecret\x00wjx\"", "", "archive.etag", "zzqsecret"},
		{"etag non-ascii", `"0x8DCCÄ"`, "", "archive.etag", "0x8DCC"},
		{"version_id oversized bearer-shaped", "", "Bearer " + strings.Repeat("e", 160), "archive.version_id", "Bearer"},
		{"version_id with whitespace", "", "2026-01-02 15:04:05Z", "archive.version_id", "15:04:05"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := audit.AnchorSegment(ctx, st, tenant, anchorTestResult(tc.etag, tc.versionID))
			if err == nil {
				t.Fatalf("anchor with a hostile %s must be refused", tc.field)
			}
			if !strings.Contains(err.Error(), tc.field) {
				t.Errorf("refusal must name the field %s: %v", tc.field, err)
			}
			if strings.Contains(err.Error(), tc.secret) {
				t.Errorf("refusal must never carry the refused value: %v", err)
			}
		})
	}
}

// TestSegmentAnchorDraftDocumentedLengthLimits pins the per-field limits of
// the H-04 follow-up (independent security finding 1): a documented S3 VersionID may be up to
// 1,024 bytes (AWS versioning documentation) and must keep anchoring — the
// 129- and 1024-byte fixtures are independent reviewer's — while anything past the field's
// documented contract is refused. The ETag limit is the largest documented
// form across the shipped sinks (DirSink's 64-hex SHA-256).
func TestSegmentAnchorDraftDocumentedLengthLimits(t *testing.T) {
	const azureETag = `"0x8DCC12A34BEF001"`
	t.Run("version id 129 bytes accepted (regression)", func(t *testing.T) {
		v := strings.Repeat("a", 129)
		draft, err := audit.SegmentAnchorDraft(anchorTestResult(azureETag, v))
		if err != nil {
			t.Fatalf("documented 129-byte VersionID must anchor: %v", err)
		}
		if draft.Meta["archive.version_id"] != v {
			t.Error("129-byte VersionID altered")
		}
	})
	t.Run("version id 1024 bytes accepted (regression)", func(t *testing.T) {
		v := strings.Repeat("a", 1024)
		draft, err := audit.SegmentAnchorDraft(anchorTestResult(azureETag, v))
		if err != nil {
			t.Fatalf("documented 1024-byte VersionID must anchor: %v", err)
		}
		if draft.Meta["archive.version_id"] != v {
			t.Error("1024-byte VersionID altered")
		}
	})
	t.Run("version id 1025 bytes refused", func(t *testing.T) {
		_, err := audit.SegmentAnchorDraft(anchorTestResult(azureETag, strings.Repeat("a", 1025)))
		if err == nil {
			t.Fatal("a VersionID past the documented 1,024-byte contract must be refused")
		}
	})
	t.Run("etag 64 bytes accepted (DirSink shape)", func(t *testing.T) {
		e := strings.Repeat("d", 64)
		draft, err := audit.SegmentAnchorDraft(anchorTestResult(e, ""))
		if err != nil {
			t.Fatalf("the 64-byte DirSink ETag shape must anchor: %v", err)
		}
		if draft.Meta["archive.etag"] != e {
			t.Error("64-byte ETag altered")
		}
	})
	t.Run("etag 65 bytes refused", func(t *testing.T) {
		_, err := audit.SegmentAnchorDraft(anchorTestResult(strings.Repeat("d", 65), ""))
		if err == nil {
			t.Fatal("an ETag past every documented form must be refused")
		}
	})
}

// TestSegmentAnchorDraftRefusesCredentialSeparators is the H-04 follow-up
// regression test (independent security finding 2, with independent reviewer's synthetic SAS fixture): a
// functional SAS query or URL-carried credential needs '?', '&', '%' or
// whitespace, and no documented ETag/VersionID alphabet uses them, so a value
// carrying them is refused on either field — before the charset rule the SAS
// shape was accepted as a version_id.
func TestSegmentAnchorDraftRefusesCredentialSeparators(t *testing.T) {
	// independent reviewer's synthetic SAS-shaped request credential (never a real secret).
	sas := "sv=2024-11-04&se=2027-01-01T00:00:00Z&sp=r&sr=b&sig=" + strings.Repeat("A", 43) + "%3D"
	cases := []struct {
		name      string
		etag      string
		versionID string
		secret    string
	}{
		{"sas as etag", sas, "", "sig="},
		{"sas as version_id", "", sas, "sig="},
		{"query marker as version_id", "", "abc?sig=AAAA", "sig="},
		{"fragment of a SAS value as etag", "se%3D2027", "", "2027"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := audit.SegmentAnchorDraft(anchorTestResult(tc.etag, tc.versionID))
			if err == nil {
				t.Fatal("a credential-shaped value must be refused")
			}
			if strings.Contains(err.Error(), tc.secret) {
				t.Errorf("refusal must never carry the refused value: %v", err)
			}
		})
	}
}

// TestSegmentAnchorDraftKeepsDocumentedAlphabets pins the keep-list of the
// charset rule (Root's 2026-09-30 ruling): '_', '.', '+', '/', '=', '"', 'W',
// '-' all appear in documented ETag/VersionID forms and must keep anchoring.
func TestSegmentAnchorDraftKeepsDocumentedAlphabets(t *testing.T) {
	cases := []struct {
		name      string
		etag      string
		versionID string
	}{
		{"azure weak quoted etag", `W/"0x8DCC12A34BEF001"`, ""},
		{"s3 multipart quoted etag", `"9b2cf535f27731c974343645a3985328-17"`, ""},
		{"gcs base64 etag with padding", "CLm9+anvys0CEAE=", ""},
		{"s3 documented version id", "", "3sL4kqtJlcpXroDTDmJ+rmSpXd3dIbrHY+MTRCxf3vjVBH40Nr8X8gdRQBpUMLUo"},
		{"s3 url-safe opaque version id with underscore", "", "abc_DEF-123.ghi+jkl/mno~"},
		{"azure datetime version id", "", "2006-01-02T15:04:05.0687761Z"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			draft, err := audit.SegmentAnchorDraft(anchorTestResult(tc.etag, tc.versionID))
			if err != nil {
				t.Fatalf("documented identifier shape must anchor: %v", err)
			}
			if tc.etag != "" && draft.Meta["archive.etag"] != tc.etag {
				t.Errorf("ETag altered: %v", draft.Meta["archive.etag"])
			}
			if tc.versionID != "" && draft.Meta["archive.version_id"] != tc.versionID {
				t.Errorf("VersionID altered: %v", draft.Meta["archive.version_id"])
			}
		})
	}
}

// TestSegmentAnchorDraftAcceptsWellFormedObjectIdentifiers pins the
// no-behavior-change rule: the real Azure, GCS and S3 identifier shapes keep
// flowing into the anchor meta exactly as before, and absent identifiers stay
// absent.
func TestSegmentAnchorDraftAcceptsWellFormedObjectIdentifiers(t *testing.T) {
	cases := []struct {
		name      string
		etag      string
		versionID string
	}{
		{"azure", `"0x8DCC12A34BEF001"`, "2006-01-02T15:04:05.0687761Z"},
		{"gcs", "CLm9+anvys0CEAE=", "1735123456789012"},
		{"s3", `"9b2cf535f27731c974343645a3985328"`, "3sL4kqtJlcpXroDTDmJ+rmSpXd3dIbrHY+MTRCxf3vjVBH40Nr8X8gdRQBpUMLUo"},
		{"absent", "", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			draft, err := audit.SegmentAnchorDraft(anchorTestResult(tc.etag, tc.versionID))
			if err != nil {
				t.Fatalf("well-formed identifiers must anchor: %v", err)
			}
			if tc.etag == "" {
				if _, ok := draft.Meta["archive.etag"]; ok {
					t.Error("empty ETag must stay omitted")
				}
			} else if draft.Meta["archive.etag"] != tc.etag {
				t.Errorf("archive.etag = %v, want %q", draft.Meta["archive.etag"], tc.etag)
			}
			if tc.versionID == "" {
				if _, ok := draft.Meta["archive.version_id"]; ok {
					t.Error("empty VersionID must stay omitted")
				}
			} else if draft.Meta["archive.version_id"] != tc.versionID {
				t.Errorf("archive.version_id = %v, want %q", draft.Meta["archive.version_id"], tc.versionID)
			}
			if draft.Meta["archive.from_seq"] != int64(1) || draft.Meta["archive.to_seq"] != int64(3) {
				t.Errorf("range meta disturbed: %+v", draft.Meta)
			}
		})
	}
}
