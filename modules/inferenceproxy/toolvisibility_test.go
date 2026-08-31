// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package inferenceproxy

import "testing"

func TestRequestToolVisibilityFull(t *testing.T) {
	v := RequestToolVisibility(false, false)
	if v != ToolVisibilityFull {
		t.Errorf("no advanced-tool-use → full, got %q", v)
	}
}

func TestRequestToolVisibilityPartialProgrammatic(t *testing.T) {
	v := RequestToolVisibility(true, false)
	if v != ToolVisibilityPartial {
		t.Errorf("programmatic tool calling → partial, got %q", v)
	}
}

func TestRequestToolVisibilityPartialToolSearch(t *testing.T) {
	v := RequestToolVisibility(false, true)
	if v != ToolVisibilityPartial {
		t.Errorf("tool search → partial, got %q", v)
	}
}

func TestRequestToolVisibilityPartialBoth(t *testing.T) {
	v := RequestToolVisibility(true, true)
	if v != ToolVisibilityPartial {
		t.Errorf("both → partial, got %q", v)
	}
}
