// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

// Command awskms-source ships the AWS KMS / Secrets Manager audit connector
// as a standalone go-plugin binary: the engine launches it and
// talks to it over gRPC (AutoMTLS). The connector code is identical to the
// in-process case (rt.AddSource(awskms.New())).
package main

import (
	"github.com/olivaresai/olivares/connectors/awskms"
	"github.com/olivaresai/olivares/sdk/plugin"
)

func main() {
	plugin.ServeSource(awskms.New())
}
