// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package engine

import (
	"errors"
	"fmt"
	"testing"

	"github.com/olivaresai/olivares/core/store"
)

func TestDirectoryWriterActivationReopenBoundary(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
		want bool
	}{
		{name: "success", want: true},
		{
			name: "indeterminate commit",
			err:  fmt.Errorf("fresh verification failed: %w", ErrDirectoryWriterActivationIndeterminate),
			want: true,
		},
		{name: "known rollback", err: errors.New("commit rejected and rolled back")},
		{name: "generation conflict", err: store.ErrConflict},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := directoryWriterActivationRequiresReopen(tc.err); got != tc.want {
				t.Fatalf("directoryWriterActivationRequiresReopen(%v) = %t, want %t",
					tc.err, got, tc.want)
			}
		})
	}
}
