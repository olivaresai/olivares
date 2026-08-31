// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package license_test

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/core/license"
)

// The seam has one job: an enterprise gate raises a refusal, and every open-core mapper
// recognizes it however deep it was wrapped. If errors.Is stops working here, three
// surfaces silently go back to answering 500.
func TestAddonRefusalSurvivesWrapping(t *testing.T) {
	err := license.AddonRequired("compliance-packs", "compliance.depth.export")

	if !errors.Is(err, license.ErrAddonRequiresLicense) {
		t.Fatal("a refusal must match the sentinel the mappers switch on")
	}
	wrapped := fmt.Errorf("module: %w", fmt.Errorf("handler: %w", err))
	if !errors.Is(wrapped, license.ErrAddonRequiresLicense) {
		t.Fatal("a doubly-wrapped refusal must still match — handlers wrap freely")
	}

	addon, op, ok := license.AddonRefusal(wrapped)
	if !ok || addon != "compliance-packs" || op != "compliance.depth.export" {
		t.Fatalf("AddonRefusal(wrapped) = (%q,%q,%v), want the add-on and operation back", addon, op, ok)
	}
	if !strings.Contains(err.Error(), "compliance-packs") || !strings.Contains(err.Error(), "compliance.depth.export") {
		t.Fatalf("the message must name both the add-on and the operation, got %q", err.Error())
	}
}

// A refusal with no subject still matches, so a gate that cannot name the operation
// degrades to a stable 403 rather than falling through to 500.
func TestBareAddonRefusalStillMatches(t *testing.T) {
	addon, op, ok := license.AddonRefusal(license.ErrAddonRequiresLicense)
	if !ok || addon != "" || op != "" {
		t.Fatalf("AddonRefusal(sentinel) = (%q,%q,%v), want ok with no subject", addon, op, ok)
	}
	if _, _, ok := license.AddonRefusal(errors.New("something else")); ok {
		t.Fatal("an unrelated error must NOT be reported as an entitlement refusal")
	}
	if _, _, ok := license.AddonRefusal(nil); ok {
		t.Fatal("nil must not be reported as an entitlement refusal")
	}
}
