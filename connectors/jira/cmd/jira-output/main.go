// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

// Command jira-output ships the Jira/JSM output connector as a standalone go-plugin
// binary: the engine launches it and talks to it over gRPC. The connector code is
// identical to the in-process case (rt.AddOutput(jira.New())).
package main

import (
	"github.com/olivaresai/olivares/connectors/jira"
	"github.com/olivaresai/olivares/sdk/plugin"
)

func main() {
	plugin.ServeOutput(jira.New())
}
