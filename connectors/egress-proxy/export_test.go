// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package egressproxy

import "time"

// SetClockForTest injects a deterministic clock into a Source. It exists only for the
// external (_test) package's timestamp-fallback test; it is compiled only into the
// test binary (the file name ends in _test.go), so it never ships.
func SetClockForTest(s *Source, now func() time.Time) { s.now = now }
