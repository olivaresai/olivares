// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

// Command cloudqueue-source ships the managed-cloud-message-bus topology observer as
// a standalone go-plugin binary: the engine launches it out-of-process so the
// connector's HTTP/signing code runs isolated from the core. The connector code is
// identical to the in-process case (rt.AddSource(cloudqueue.New())).
package main

import (
	"github.com/olivaresai/olivares/connectors/cloudqueue"
	"github.com/olivaresai/olivares/sdk/plugin"
)

func main() {
	plugin.ServeSource(cloudqueue.New())
}
