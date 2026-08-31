// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

// Command cowork-analytics-source serves the Claude Cowork engagement connector
// (Enterprise Analytics API) as a standalone go-plugin binary, so the engine can
// run it OUT-OF-PROCESS. It imports only the Apache SDK, never the engine.
package main

import (
	coworkanalytics "github.com/olivaresai/olivares/connectors/cowork-analytics"
	"github.com/olivaresai/olivares/sdk/plugin"
)

func main() { plugin.ServeSource(coworkanalytics.New()) }
