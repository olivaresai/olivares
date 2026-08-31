// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

// Command nats-source ships the NATS JetStream observer as a standalone go-plugin
// binary: the engine launches it out-of-process (CB-1 transport B, AutoMTLS) so the
// connector runs identically in-process (rt.AddSource(nats.New())) or over gRPC. The
// NATS wire client is hand-rolled over the standard library — no third-party NATS
// dependency links into the core.
package main

import (
	"github.com/olivaresai/olivares/connectors/nats"
	"github.com/olivaresai/olivares/sdk/plugin"
)

func main() {
	plugin.ServeSource(nats.New())
}
