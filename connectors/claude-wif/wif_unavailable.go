//go:build !enterprise || !addon_ids

// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package claudewif

import (
	"context"
	"errors"

	"github.com/olivaresai/olivares/connectors/modelprovider"
)

var errFederationUnavailable = errors.New("Workload Identity Federation requires Business")

// Exchanger preserves the direct API when federation is unavailable.
type Exchanger struct{}

// NewExchanger returns a refusing exchanger without retaining credentials or transport.
func NewExchanger(string, modelprovider.Doer) *Exchanger { return &Exchanger{} }

func (*Exchanger) Exchange(context.Context, string, ExchangeParams) (MintedToken, error) {
	return MintedToken{}, errFederationUnavailable
}
