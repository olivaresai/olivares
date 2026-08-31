// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package ebpf

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/olivaresai/olivares/sdk/model"
)

func TestMaskToMode(t *testing.T) {
	cases := []struct {
		name string
		mask int
		want model.AccessMode
	}{
		{"empty", 0x00, model.ModeUnknown},
		{"read", mayRead, model.ModeRead},
		{"exec only is read", mayExec, model.ModeRead},
		{"write", mayWrite, model.ModeWrite},
		{"append is write", mayAppend, model.ModeWrite},
		{"read+write", mayRead | mayWrite, model.ModeReadWrite},
		{"read+exec is read", mayRead | mayExec, model.ModeRead},
		{"write+append is write", mayWrite | mayAppend, model.ModeWrite},
		{"all bits", mayRead | mayWrite | mayAppend | mayExec, model.ModeReadWrite},
		{"exec+write is readwrite", mayExec | mayWrite, model.ModeReadWrite},
		{"may_access only is unknown", 0x10, model.ModeUnknown},
		{"may_open only is unknown", 0x20, model.ModeUnknown},
		{"read with unknown bits", mayRead | 0x10, model.ModeRead},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, maskToMode(tc.mask))
			assert.True(t, maskToMode(tc.mask).Valid())
		})
	}
}
