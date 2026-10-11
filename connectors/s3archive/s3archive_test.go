// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package s3archive

import (
	"context"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/sdk"
)

func TestCommunityConnectorRefusesAllS3Operations(t *testing.T) {
	ctx := context.Background()
	out := New()
	check := func(err error) {
		t.Helper()
		if err == nil || !strings.Contains(err.Error(), "Business Regulated Operations") {
			t.Fatalf("operation = %v, want Business refusal", err)
		}
	}
	check(out.Open(ctx, sdk.Config{Settings: map[string]string{"endpoint": "https://s3.example.invalid", "region": "eu-west-1", "bucket": "ledger-archive", "access_key_id": "fixture", "secret_access_key": "fixture", "retention_days": "1"}}))
	check(out.Notify(ctx, sdk.Notification{}))
	receipt, err := out.Put(ctx, "segment", []byte("ledger"), PutOptions{})
	check(err)
	if receipt.LockVerified {
		t.Fatal("Community fabricated a verified lock")
	}
	versions, err := out.ListObjectVersions(ctx, "tenant/")
	check(err)
	if len(versions) != 0 {
		t.Fatal("Community fabricated object versions")
	}
	receipt, err = out.SetObjectLegalHold(ctx, "segment", "v1", true)
	check(err)
	if receipt.LockVerified {
		t.Fatal("Community fabricated a legal hold")
	}
	if err := out.Close(ctx); err != nil {
		t.Fatal(err)
	}
	if out.Descriptor().Name != Name {
		t.Fatal("published connector identity changed")
	}
}
