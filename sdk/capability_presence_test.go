// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package sdk_test

import (
	"testing"

	"github.com/olivaresai/olivares/sdk"
)

type switchable bool

func (s switchable) CapabilityEnabled() bool { return bool(s) }

func TestCapabilityEnabledFollowsTheCapabilityItself(t *testing.T) {
	for _, tc := range []struct {
		name       string
		capability any
		want       bool
	}{
		{"absent", nil, false},
		{"plain capability", struct{}{}, true},
		{"switched off", switchable(false), false},
		{"switched on", switchable(true), true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := sdk.CapabilityEnabled(tc.capability); got != tc.want {
				t.Fatalf("CapabilityEnabled(%v) = %v, want %v", tc.capability, got, tc.want)
			}
		})
	}
}
