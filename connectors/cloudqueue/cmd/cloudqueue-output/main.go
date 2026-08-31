// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

// Command cloudqueue-output ships the managed-cloud-message-bus CloudEvents egress
// connector as a standalone go-plugin binary. The connector code is identical to the
// in-process case (rt.AddOutput(cloudqueue.NewOutput())).
package main

import (
	"github.com/olivaresai/olivares/connectors/cloudqueue"
	"github.com/olivaresai/olivares/sdk/plugin"
)

func main() {
	plugin.ServeOutput(cloudqueue.NewOutput())
}
