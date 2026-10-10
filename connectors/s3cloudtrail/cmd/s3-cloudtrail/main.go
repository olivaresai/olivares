// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

// Command s3-cloudtrail ships the AWS CloudTrail (S3) source connector as a standalone
// go-plugin binary. The engine runs this connector in-process, so the release does not
// build this binary; build it to run the connector in its own process as an external
// plugin, over gRPC with AutoMTLS.
package main

import (
	"github.com/olivaresai/olivares/connectors/s3cloudtrail"
	"github.com/olivaresai/olivares/sdk/plugin"
)

func main() {
	plugin.ServeSource(s3cloudtrail.New())
}
