// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

// Command aws-source ships the AWS connector (IAM + CloudTrail inventory and the
// Bedrock Guardrails safety-posture reads) as a standalone go-plugin binary. The
// engine runs this connector in-process, so the release does not build this binary;
// build it to run the connector in its own process as an external plugin, over gRPC
// with AutoMTLS. The connector code is identical to the in-process case
// (rt.AddSource(aws.New())). This mirrors openai-source so the cloud cost/posture
// connectors share one standalone shape: an external signed plugin.
package main

import (
	"github.com/olivaresai/olivares/connectors/aws"
	"github.com/olivaresai/olivares/sdk/plugin"
)

func main() {
	plugin.ServeSource(aws.New())
}
