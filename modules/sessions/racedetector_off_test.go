// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

//go:build !race

package sessions

// raceDetectorEnabled reports whether this test binary was built with the race
// detector. It is a property of the build, never of the environment.
const raceDetectorEnabled = false

// raceGuardEnv is the launch environment of a test that times a launched program.
// The session guard in front of the program is this test binary, and a binary
// built with the race detector pauses a second before it exits (GORACE
// atexit_sleep_ms, 1000 by default). The engine's guard does not, so neither may
// this one when a test times the program's end.
var raceGuardEnv []EnvVar
