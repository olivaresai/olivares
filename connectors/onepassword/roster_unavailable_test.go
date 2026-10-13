//go:build !enterprise || !addon_ids

// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package onepassword

import (
	"github.com/olivaresai/olivares/connectors/internal/identitytest"
	"testing"
)

func TestCommunitySnapshotUnavailable(t *testing.T) {
	identitytest.AssertUnavailable(t, func() identitytest.Source { return New() })
}
