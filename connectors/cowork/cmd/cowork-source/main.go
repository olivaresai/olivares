// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

// Command cowork-source serves the Claude Cowork cooperative-telemetry connector
// (OTLP/HTTP logs) as a standalone go-plugin binary, so the engine can run it
// OUT-OF-PROCESS and its OTLP dependency tree never links into the core. The same
// connector also runs in-process. It imports only the Apache SDK, never the engine.
package main

import (
	"github.com/olivaresai/olivares/connectors/cowork"
	"github.com/olivaresai/olivares/sdk/plugin"
)

func main() { plugin.ServeSource(cowork.New()) }
