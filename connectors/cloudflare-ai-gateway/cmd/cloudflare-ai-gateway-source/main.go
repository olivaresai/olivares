// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package main

import (
	cfaigateway "github.com/olivaresai/olivares/connectors/cloudflare-ai-gateway"
	"github.com/olivaresai/olivares/sdk/plugin"
)

func main() {
	plugin.ServeSource(cfaigateway.New())
}
