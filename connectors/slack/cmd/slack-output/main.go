// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

// Command slack-output ships the Slack output connector as a standalone
// go-plugin binary: the engine launches it and talks to it over gRPC. The
// connector code is identical to the in-process case (rt.AddOutput(slack.New())).
package main

import (
	conn "github.com/olivaresai/olivares/connectors/slack"
	"github.com/olivaresai/olivares/sdk/plugin"
)

func main() {
	plugin.ServeOutput(conn.New())
}
