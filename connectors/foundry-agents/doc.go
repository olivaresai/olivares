// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

// Package foundryagents is the Olivares AI identity connector for Microsoft
// Foundry as an agent platform. It reads two current, read-only surfaces pinned
// on 2026-07-04: the Azure Resource Manager control plane for Foundry projects,
// agent applications and agent deployments, and the Foundry Agent Service data
// plane for per-project agent inventory. The ARM leg uses the stable project
// api-version 2025-06-01 and application/deployment api-version 2026-05-01
// with the https://management.azure.com/.default audience. The data-plane leg
// uses /api/projects/{projectName}/agents?api-version=v1 with the
// https://ai.azure.com/.default audience; listing agents requires the Foundry
// User role's agent data actions, so a per-project 403 is treated as an honest
// "not granted" state.
//
// Wire-shape caveat: Microsoft is migrating the REST reference to ai.azure.com
// and the learn REST pages 404; the data-plane list envelope is pinned from the
// SDK paging semantics, not from a captured wire sample. The decoder therefore
// accepts items under data or value and follows only has_more + last_id cursors.
//
// The connector is deliberately minimal-data. The Foundry data-plane agent
// object can embed the agent's system prompt at definition.instructions and
// tool payloads at definition.tools, and the API has no server-side field
// filter. Those fields, plus operator metadata, are not decoded at all and
// cannot surface in the roster. ARM authorizationPolicy and trafficRoutingPolicy
// are also omitted because their shape was not verified.
//
// Honest limits: the Foundry Control Plane fleet KPIs/assets, custom-agent
// registry, posture/compliance rollup and Defender/Purview signals are portal
// experiences with no public read API as of 2026-07-04. Azure OpenAI Assistants
// retiring 2026-08-26 and Foundry classic agents retiring 2027-03-31 are
// excluded; this package reads only the current Agent Service. Lifecycle
// start/stop/block actions are portal-only actuation and are not implemented.
//
// This package is complementary to azure-openai, which reads the same ARM
// provider for Cognitive Services accounts, model deployments, metrics and
// cost: Foundry as an inference estate. foundry-agents reads projects,
// applications and data-plane agents: Foundry as an agent platform. The
// agentIdentityBlueprint/defaultInstanceIdentity clientId/principalId fields
// are correlation anchors to the entra-agent roster, which owns the
// identity-level view; Microsoft states Foundry Agent Applications are not
// registered in the Microsoft Entra agent registry. Code is replicated rather
// than shared across these connectors to preserve the Apache/AGPL boundary and
// the repo's ruling.
package foundryagents
