// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

// Command idp-source ships the Okta / Microsoft Entra ID identity connector as a
// standalone go-plugin binary: the engine launches it and talks to it over gRPC.
// The connector code is identical to the in-process case (rt.AddSource(idp.New())).
package main

import (
	"github.com/olivaresai/olivares/connectors/idp"
	"github.com/olivaresai/olivares/sdk/plugin"
)

func main() {
	plugin.ServeSource(idp.New())
}
