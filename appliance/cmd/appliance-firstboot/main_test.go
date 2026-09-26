// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"errors"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/appliance/layer/base"
)

func TestReconcile_ExitsByOutcomeAndNamesTheUnitToRunNext(t *testing.T) {
	cases := []struct {
		name string
		err  error
		code int
		says string
	}{
		{"reconciled", nil, 0, "sudo systemctl start olivares-appliance-firstboot.service"},
		{"refused: the product may have started", base.Refuse("the product may have started"), 1, "cannot reconcile: refused"},
		{"refused: another record schema", &base.SchemaError{Found: "olivares-appliance-firstboot/v9"}, 1, "v9"},
		{"pending: no carrier", base.Wait("no answers carrier is present"), 2, "cannot reconcile: pending"},
		{"the record cannot be written", errors.New("disk full"), 2, "disk full"},
	}
	for _, tc := range cases {
		code, line := reconcileOutcome(base.Record{}, tc.err)
		if code != tc.code || !strings.Contains(line, tc.says) {
			t.Fatalf("%s: exit %d %q, want %d and %q", tc.name, code, line, tc.code, tc.says)
		}
	}
	if code, line := reconcileOutcome(base.Record{}, nil); code != 0 || strings.Contains(line, "appliance-firstboot apply") {
		t.Fatalf("the next step runs outside the unit: %q", line)
	}
}
