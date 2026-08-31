// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

// Command ebpf-source ships the eBPF/Tetragon backstop connector as a standalone
// go-plugin binary. The engine launches this binary and talks to it over gRPC; the
// connector code is identical to the in-process case. This is the worked example
// of "how to ship the eBPF connector as a plugin".
package main

import (
	"github.com/olivaresai/olivares/connectors/ebpf"
	"github.com/olivaresai/olivares/sdk/plugin"
)

func main() {
	plugin.ServeSource(ebpf.New())
}
