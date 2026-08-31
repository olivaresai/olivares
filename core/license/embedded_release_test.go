// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

//go:build release

package license_test

import (
	"testing"

	"github.com/olivaresai/olivares/core/license"
)

// TestReleaseBuildHasNoDevKey covers the release build (-tags release) with NO key
// injected — the default for `go test -tags release`. The dev keypair is
// physically absent and there is no verification anchor: the engine verifies
// nothing and reports "none", never a panic and never a block. This is the test
// `task test:release` runs so the release-only file cannot silently rot (the
// tag-less default gate never compiles it).
func TestReleaseBuildHasNoDevKey(t *testing.T) {
	if license.HasDevKey {
		t.Fatal("HasDevKey = true in a release build, want false")
	}
	if license.DevPrivateKey() != nil {
		t.Fatal("DevPrivateKey() non-nil in a release build, want nil")
	}
	if license.DefaultPublicKey() != nil {
		t.Fatal("DefaultPublicKey() non-nil in a release build with no key injected, want nil")
	}
	if got := license.KeyOrigin(); got != "none" {
		t.Fatalf("KeyOrigin = %q, want none in a release build with no key injected", got)
	}
	if got := license.KeyFingerprint(); got != "" {
		t.Fatalf("KeyFingerprint = %q, want empty in a release build with no key", got)
	}
}
