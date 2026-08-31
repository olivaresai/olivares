// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package ebpf

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestHashKey(t *testing.T) {
	h := hashKey("exec-123")
	assert.Len(t, h, 64, "sha-256 hex is 64 chars")
	assert.Equal(t, h, hashKey("exec-123"), "deterministic")
	assert.NotEqual(t, h, hashKey("exec-124"))
	assert.NotContains(t, h, "exec", "hash does not leak the input")
}
