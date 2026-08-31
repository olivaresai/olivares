// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package sops

import "time"

// SetClock injects a deterministic clock for tests (the now field is unexported,
// so the external _test package reaches it through this test-only helper). It is
// compiled only under `go test`.
func SetClock(s *Source, now func() time.Time) { s.now = now }
