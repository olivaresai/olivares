// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

// Command ebpf-source ships the eBPF/Tetragon backstop connector as a standalone
// go-plugin binary. The engine runs this connector in-process, so the engine release
// does not embed this binary (Dockerfile.ebpf-source packages it as its own image);
// build it to run the connector in its own process as an external plugin, over gRPC
// with AutoMTLS. The connector code is identical to the in-process case. This is the
// worked example of "how to ship the eBPF connector as a plugin".
package main

import (
	"github.com/olivaresai/olivares/connectors/ebpf"
	"github.com/olivaresai/olivares/sdk/plugin"
)

func main() {
	plugin.ServeSource(ebpf.New())
}
