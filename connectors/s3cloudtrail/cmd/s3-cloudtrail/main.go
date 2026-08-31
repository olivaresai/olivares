// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

// Command s3-cloudtrail ships the AWS CloudTrail (S3) source connector as a
// standalone go-plugin binary. The engine launches it and talks to it over gRPC.
package main

import (
	"github.com/olivaresai/olivares/connectors/s3cloudtrail"
	"github.com/olivaresai/olivares/sdk/plugin"
)

func main() {
	plugin.ServeSource(s3cloudtrail.New())
}
