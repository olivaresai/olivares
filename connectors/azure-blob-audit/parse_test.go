// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package azureblobaudit

import (
	"testing"

	"github.com/olivaresai/olivares/sdk/model"
)

func TestClassifyMode(t *testing.T) {
	cases := []struct {
		op, cat string
		want    model.AccessMode
	}{
		// operationName decides first (verbatim).
		{"GetBlob", "StorageRead", model.ModeRead},
		{"GetBlobProperties", "StorageRead", model.ModeRead},
		{"ListBlobs", "StorageRead", model.ModeRead},
		{"PutBlob", "StorageWrite", model.ModeWrite},
		{"PutBlock", "StorageWrite", model.ModeWrite},
		{"PutBlockList", "StorageWrite", model.ModeWrite},
		{"SetBlobMetadata", "StorageWrite", model.ModeWrite},
		{"DeleteBlob", "StorageDelete", model.ModeWrite},
		// operationName trumps a contradictory category (verbatim by op).
		{"GetBlob", "StorageWrite", model.ModeRead},
		// Unrecognized op -> category fallback.
		{"AbortCopyBlob", "StorageWrite", model.ModeWrite},
		{"SomeFutureRead", "StorageRead", model.ModeRead},
		{"SomeFutureDelete", "StorageDelete", model.ModeWrite},
		// Neither classifies -> unknown (explicit, never guessed).
		{"GetBlobServiceProperties", "StorageOther", model.ModeUnknown},
		{"", "", model.ModeUnknown},
	}
	for _, c := range cases {
		if got := classifyMode(c.op, c.cat); got != c.want {
			t.Errorf("classifyMode(%q,%q) = %q, want %q", c.op, c.cat, got, c.want)
		}
	}
}

func TestResolveResource(t *testing.T) {
	cases := []struct {
		uri               string
		wantKind, wantRef string
		wantOK            bool
	}{
		{"https://contoso.blob.core.windows.net/reports/q2/summary.parquet?sig=X", "azureblob.object", "contoso/reports/q2/summary.parquet", true},
		{"https://contoso.blob.core.windows.net/reports/q2/nested/deep.json", "azureblob.object", "contoso/reports/q2/nested/deep.json", true},
		{"https://contoso.blob.core.windows.net/reports?restype=container&comp=list", "azureblob.container", "contoso/reports", true},
		{"https://contoso.blob.core.windows.net/reports/", "azureblob.container", "contoso/reports", true},
		// Service-level request (no container) is not an emittable resource.
		{"https://contoso.blob.core.windows.net/?comp=list", "", "", false},
		{"https://contoso.blob.core.windows.net", "", "", false},
		{"", "", "", false},
	}
	for _, c := range cases {
		kind, ref, ok := resolveResource(c.uri)
		if ok != c.wantOK || kind != c.wantKind || ref != c.wantRef {
			t.Errorf("resolveResource(%q) = (%q,%q,%v), want (%q,%q,%v)", c.uri, kind, ref, ok, c.wantKind, c.wantRef, c.wantOK)
		}
	}
}

func TestParseTime(t *testing.T) {
	if ts, ok := parseTime("2026-06-03T10:23:45.1234567Z"); !ok {
		t.Fatal("expected ISO-8601 7-digit fractional Z to parse")
	} else if ts.Location() != nil && ts.UTC() != ts {
		t.Errorf("timestamp not normalized to UTC: %v", ts)
	}
	if _, ok := parseTime("not-a-time"); ok {
		t.Error("expected garbage timestamp to fail")
	}
}

// TestRequesterAppIDFallback proves the connector reads the exported nested
// location (identity.requester.appId) and tolerates the flattened Log Analytics
// field, preferring the nested one.
func TestRequesterAppIDFallback(t *testing.T) {
	nested := record{Identity: identityBlock{Requester: requesterBlock{AppID: "nested-app"}}, RequesterAppIDFlat: "flat-app"}
	if got := nested.requesterAppID(); got != "nested-app" {
		t.Errorf("requesterAppID nested = %q, want nested-app", got)
	}
	flat := record{RequesterAppIDFlat: "flat-app"}
	if got := flat.requesterAppID(); got != "flat-app" {
		t.Errorf("requesterAppID flat = %q, want flat-app", got)
	}
	nestedAuth := record{Identity: identityBlock{Type: "OAuth"}, AuthenticationTypeFlat: "AccountKey"}
	if got := nestedAuth.authType(); got != "OAuth" {
		t.Errorf("authType nested = %q, want OAuth", got)
	}
}
