//go:build !enterprise || !addon_ids

// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package foundryagents

import (
	"context"
	"errors"

	"github.com/olivaresai/olivares/connectors/identitysource"
)

var errRosterUnavailable = errors.New("directory inventory requires Business")

func (s *Source) Snapshot(context.Context) (identitysource.Graph, error) {
	return identitysource.Graph{}, errRosterUnavailable
}
