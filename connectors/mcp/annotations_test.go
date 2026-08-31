// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package mcp

import (
	"testing"

	"github.com/olivaresai/olivares/sdk/model"
)

func ptrBool(b bool) *bool { return &b }

func TestModeFromAnnotations(t *testing.T) {
	cases := []struct {
		name string
		ann  *ToolAnnotations
		want model.AccessMode
	}{
		{"nil annotations (default not-read-only)", nil, model.ModeReadWrite},
		{"empty annotations (readOnly defaults false)", &ToolAnnotations{}, model.ModeReadWrite},
		{"readOnlyHint true", &ToolAnnotations{ReadOnlyHint: ptrBool(true)}, model.ModeRead},
		{"readOnlyHint false", &ToolAnnotations{ReadOnlyHint: ptrBool(false)}, model.ModeReadWrite},
		{"destructive only (readOnly absent)", &ToolAnnotations{DestructiveHint: ptrBool(true)}, model.ModeReadWrite},
		{"readOnly true wins over destructive", &ToolAnnotations{ReadOnlyHint: ptrBool(true), DestructiveHint: ptrBool(true)}, model.ModeRead},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := modeFromAnnotations(tc.ann); got != tc.want {
				t.Errorf("modeFromAnnotations = %q, want %q", got, tc.want)
			}
		})
	}
}
