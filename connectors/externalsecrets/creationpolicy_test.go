// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package externalsecrets_test

import (
	"context"

	"github.com/olivaresai/olivares/sdk/model"
)

type cpSink struct{ edges []model.EdgeObservation }

func (c *cpSink) Emit(_ context.Context, obs model.Observation) error {
	if e, ok := obs.(model.EdgeObservation); ok {
		c.edges = append(c.edges, e)
	}
	return nil
}
