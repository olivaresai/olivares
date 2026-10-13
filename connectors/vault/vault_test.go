// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package vault

import (
	"context"
	"time"

	"github.com/olivaresai/olivares/sdk/model"
)

const testToken = "hvs.CAESTESTTOKEN-secret-value"

// captureSink records emitted observations.
type captureSink struct{ obs []model.Observation }

func (s *captureSink) Emit(_ context.Context, o model.Observation) error {
	s.obs = append(s.obs, o)
	return nil
}

func fixedClock() time.Time { return time.Date(2026, 6, 3, 12, 0, 0, 0, time.UTC) }
