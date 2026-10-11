// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

// Command cowork-analytics-source serves the Claude Cowork engagement connector
// (Enterprise Analytics API) as a standalone go-plugin binary. The engine runs this
// connector in-process, so the release does not build this binary; build it to run the
// connector in its own process as an external plugin, over gRPC with AutoMTLS. It
// imports only the Apache SDK, never the engine.
package main

import (
	coworkanalytics "github.com/olivaresai/olivares/connectors/cowork-analytics"
	"github.com/olivaresai/olivares/sdk/plugin"
)

func main() { plugin.ServeSource(coworkanalytics.New()) }
