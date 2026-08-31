// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

// Command aws-source ships the AWS connector (IAM + CloudTrail inventory and the
// Bedrock Guardrails safety-posture reads) as a standalone go-plugin binary:
// the engine launches it and talks to it over gRPC. The connector code is identical
// to the in-process case (rt.AddSource(aws.New())). This mirrors openai-source so the
// cloud cost/posture connectors share one deployment shape (external signed plugin or
// collector), keeping their dependency trees out of the core (ARCHITECTURE.md).
package main

import (
	"github.com/olivaresai/olivares/connectors/aws"
	"github.com/olivaresai/olivares/sdk/plugin"
)

func main() {
	plugin.ServeSource(aws.New())
}
