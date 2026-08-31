// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package main

import (
	cfmcpportals "github.com/olivaresai/olivares/connectors/cloudflare-mcp-portals"
	"github.com/olivaresai/olivares/sdk/plugin"
)

func main() {
	plugin.ServeSource(cfmcpportals.New())
}
