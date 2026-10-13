//go:build !enterprise || !addon_ids

// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package kmip

import (
	"context"
	"testing"

	"github.com/olivaresai/olivares/connectors/internal/identitytest"
	"github.com/olivaresai/olivares/sdk"
)

func TestCommunitySnapshotUnavailable(t *testing.T) {
	identitytest.AssertUnavailable(t, func() identitytest.Source { return localKMIPSource{New()} })
}

// The common inventory contract configures provider-independent settings.
// KMIP also requires an endpoint; this labelled local stand-in is not dialed
// by Open, and the unavailable Snapshot must refuse before any transport use.
type localKMIPSource struct{ *Source }

func (s localKMIPSource) Open(ctx context.Context, cfg sdk.Config) error {
	cfg.Settings["endpoint"] = "127.0.0.1:5696"
	return s.Source.Open(ctx, cfg)
}
