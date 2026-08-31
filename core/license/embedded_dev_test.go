// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

//go:build !release

package license_test

import (
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/license"
)

// TestEmbeddedDevKey covers the default (non-release) build: the embedded dev
// public key verifies a dev-signed license, so demos/tests work out of the box,
// and the provenance helpers report the dev origin. A release build (-tags
// release) ships no dev key; that path is covered by embedded_release_test.go.
func TestEmbeddedDevKey(t *testing.T) {
	blob, err := license.Sign(license.Claims{Licensee: "Dev", IssuedAt: time.Now()}, license.DevPrivateKey())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := license.Verify(blob, license.DefaultPublicKey()); err != nil {
		t.Fatalf("dev license against embedded key: %v", err)
	}
	if !license.HasDevKey {
		t.Fatal("HasDevKey = false in a non-release build, want true")
	}
	if got := license.KeyOrigin(); got != "dev" {
		t.Fatalf("KeyOrigin = %q, want dev in a non-release build", got)
	}
	if license.KeyFingerprint() == "" {
		t.Fatal("KeyFingerprint empty in a dev build, want the dev key fingerprint")
	}
}
