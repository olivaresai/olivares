// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

// Command azure-openai-source ships the Azure OpenAI / AI Foundry connector as a
// standalone go-plugin binary: the engine launches it and talks to it over gRPC. The
// connector code is identical to the in-process case (rt.AddSource(azureopenai.New())).
package main

import (
	azureopenai "github.com/olivaresai/olivares/connectors/azure-openai"
	"github.com/olivaresai/olivares/sdk/plugin"
)

func main() {
	plugin.ServeSource(azureopenai.New())
}
