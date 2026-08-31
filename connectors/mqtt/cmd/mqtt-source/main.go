// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

// Command mqtt-source ships the MQTT 5.0 / Sparkplug B observer as a standalone
// go-plugin binary: the engine launches it out-of-process (CB-1 transport B,
// AutoMTLS). The connector code is identical to the in-process case
// (rt.AddSource(mqtt.New())). The wire client is hand-rolled stdlib-only, so the
// plugin links no third-party MQTT dependency.
package main

import (
	"github.com/olivaresai/olivares/connectors/mqtt"
	"github.com/olivaresai/olivares/sdk/plugin"
)

func main() {
	plugin.ServeSource(mqtt.New())
}
