// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

// Command bedrock-source ships the AWS Bedrock usage/cost + Guardrails connector
// as a standalone go-plugin binary: the engine launches it and talks to it over gRPC.
// The connector code is identical to the in-process case (rt.AddSource(bedrock.New())).
// This mirrors aws-source/openai-source so the cloud usage/cost/posture connectors share
// one deployment shape (external signed plugin or collector), keeping their dependency
// trees out of the core (ARCHITECTURE.md).
package main

import (
	"github.com/olivaresai/olivares/connectors/bedrock"
	"github.com/olivaresai/olivares/sdk/plugin"
)

func main() {
	plugin.ServeSource(bedrock.New())
}
