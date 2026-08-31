// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

// Command egress-proxy-source ships the egress-proxy verdict-log connector
// as a standalone go-plugin binary: the engine launches it and
// talks to it over gRPC (AutoMTLS). The connector code is identical to the
// in-process case (rt.AddSource(egressproxy.New())).
package main

import (
	egressproxy "github.com/olivaresai/olivares/connectors/egress-proxy"
	"github.com/olivaresai/olivares/sdk/plugin"
)

func main() {
	plugin.ServeSource(egressproxy.New())
}
