// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

// Command claude-source serves the Claude Code cooperative-telemetry connector
// (OTLP + PostToolUse/PreToolUse hooks) as a standalone go-plugin binary, so the
// engine can run it OUT-OF-PROCESS (CB-1 transport B) and its OTLP dependency tree
// never links into the core. The single control-plane artifact embeds this binary
// (cmd/olivares/firstparty) and launches it as an isolated subprocess; the
// exact same connector also runs in-process or behind a collector. It imports only
// the Apache SDK, never the engine.
package main

import (
	"github.com/olivaresai/olivares/connectors/claude"
	"github.com/olivaresai/olivares/sdk/plugin"
)

func main() { plugin.ServeSource(claude.New()) }
