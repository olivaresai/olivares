// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"context"
	"testing"

	"github.com/olivaresai/olivares/connectors/s3archive"
	"github.com/olivaresai/olivares/core/audit"
)

// fakeS3Capable is an s3archivePutter that ALSO exposes the post-hoc capabilities, so the
// s3ArchiveSinkAdapter's ArchiveLister/LegalHoldSetter forwarding is exercised through the
// real seam (not bypassed). It returns CONNECTOR-RELATIVE keys, exactly as the connector now
// does after stripping its prefix.
type fakeS3Capable struct {
	versions  []s3archive.ObjectVersion
	holdCalls int
}

func (f *fakeS3Capable) Put(context.Context, string, []byte, s3archive.PutOptions) (s3archive.Receipt, error) {
	return s3archive.Receipt{}, nil
}
func (f *fakeS3Capable) ListObjectVersions(context.Context, string) ([]s3archive.ObjectVersion, error) {
	return f.versions, nil
}
func (f *fakeS3Capable) SetObjectLegalHold(_ context.Context, key, ver string, _ bool) (s3archive.Receipt, error) {
	f.holdCalls++
	return s3archive.Receipt{Bucket: "b", Key: key, VersionID: ver, LockVerified: true}, nil
}

func TestS3AdapterListSegmentsFiltersToTenantBodies(t *testing.T) {
	f := &fakeS3Capable{versions: []s3archive.ObjectVersion{
		{Key: "acme/seg-000000000001-000000000010.jsonl", VersionID: "v1", IsLatest: true},
		{Key: "acme/seg-000000000001-000000000010.jsonl.manifest.json", VersionID: "v2"}, // manifest, excluded
		{Key: "acme/keys.json", VersionID: "v3"},                                         // keys.json, excluded
		{Key: "other/seg-000000000001-000000000010.jsonl", VersionID: "v4"},              // other tenant, excluded
	}}
	a := s3ArchiveSinkAdapter{out: f}

	refs, err := a.ListSegments(context.Background(), "acme")
	if err != nil {
		t.Fatalf("ListSegments: %v", err)
	}
	if len(refs) != 1 || refs[0].FromSeq != 1 || refs[0].ToSeq != 10 || refs[0].VersionID != "v1" {
		t.Fatalf("want exactly the acme segment body, got %+v", refs)
	}

	// The adapter also satisfies LegalHoldSetter and forwards to the connector.
	rec, err := a.SetObjectLegalHold(context.Background(), refs[0].Key, refs[0].VersionID, true)
	if err != nil || !rec.LockVerified || f.holdCalls != 1 {
		t.Fatalf("SetObjectLegalHold forward failed: rec=%+v err=%v calls=%d", rec, err, f.holdCalls)
	}

	// Compile-time: the adapter is an ArchiveLister + LegalHoldSetter.
	var _ audit.ArchiveLister = a
	var _ audit.LegalHoldSetter = a
}
