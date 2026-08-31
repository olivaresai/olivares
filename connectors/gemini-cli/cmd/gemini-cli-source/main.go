// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

// Command gemini-cli-source ships the Gemini CLI governance connector as a standalone
// go-plugin binary: the engine launches it and talks to it over gRPC. The connector code
// is identical to the in-process case (rt.AddSource(geminicli.New())).
package main

import (
	geminicli "github.com/olivaresai/olivares/connectors/gemini-cli"
	"github.com/olivaresai/olivares/sdk/plugin"
)

func main() {
	plugin.ServeSource(geminicli.New())
}
