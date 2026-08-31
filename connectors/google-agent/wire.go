// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package googleagent

import "strings"

// The spec.identityType enum — VERIFIED-RAW 2026-06-11 against the live
// aiplatform v1 discovery document; re-verified 2026-07-05 against the same
// reasoningEngines collection in the now-titled Agent Platform API. An
// unknown/forward-compat value is treated like SERVICE_ACCOUNT/unspecified (an
// approximate NHI), never like AGENT_IDENTITY (which is the FIRM per-agent kind
// and must not be guessed).
const (
	identityTypeAgent       = "AGENT_IDENTITY"
	identityTypeSA          = "SERVICE_ACCOUNT"
	identityTypeUnspecified = "IDENTITY_TYPE_UNSPECIFIED"
)

// reasoningEnginesResponse is the reasoningEngines.list reply — VERIFIED-RAW
// 2026-06-11 against the live aiplatform v1 discovery document; re-verified
// 2026-07-05 against aiplatform v1 revision 20260627 (Agent Platform API).
type reasoningEnginesResponse struct {
	ReasoningEngines []reasoningEngine `json:"reasoningEngines"`
	NextPageToken    string            `json:"nextPageToken"`
}

// reasoningEngine is the slice of the ReasoningEngine resource the connector
// reads (never a credential). createTime is part of the verified shape and is
// decoded for fidelity, but it is not emitted: the roster attributes are the
// deliberate closed set below. agentGatewayConfig was added to this decoder on
// 2026-07-05 from the unchanged aiplatform v1 reasoningEngines collection; only
// the two gateway resource-name links are retained.
type reasoningEngine struct {
	Name        string `json:"name"` // projects/{n}/locations/{loc}/reasoningEngines/{id}
	DisplayName string `json:"displayName"`
	CreateTime  string `json:"createTime"`
	Spec        struct {
		AgentFramework string `json:"agentFramework"` // "google-adk","langchain","langgraph","ag2","llama-index","custom"
		ServiceAccount string `json:"serviceAccount"`
		IdentityType   string `json:"identityType"` // IDENTITY_TYPE_UNSPECIFIED | SERVICE_ACCOUNT | AGENT_IDENTITY
		// EffectiveIdentity is output-only: the identity the engine actually runs
		// as. For AGENT_IDENTITY it is a SCHEME-LESS SPIFFE ID (the discovery doc
		// omits the spiffe:// scheme); otherwise a service-account email.
		EffectiveIdentity string `json:"effectiveIdentity"`
		DeploymentSpec    struct {
			AgentGatewayConfig struct {
				ClientToAgentConfig struct {
					AgentGateway string `json:"agentGateway"`
				} `json:"clientToAgentConfig"`
				AgentToAnywhereConfig struct {
					AgentGateway string `json:"agentGateway"`
				} `json:"agentToAnywhereConfig"`
			} `json:"agentGatewayConfig"`
		} `json:"deploymentSpec"`
	} `json:"spec"`
}

// trustDomainOf returns the SPIFFE trust domain of a scheme-less effective
// identity: its first path component, e.g.
// agents.global.org-123456789012.system.id.goog (org parent) or
// agents.global.project-9876543210.system.id.goog (org-less project).
func trustDomainOf(effectiveIdentity string) string {
	if i := strings.IndexByte(effectiveIdentity, '/'); i >= 0 {
		return effectiveIdentity[:i]
	}
	return effectiveIdentity
}

// projectNumberOf returns the path segment after "projects/" in a scheme-less
// effective identity (the PROJECT NUMBER, distinct from the configured project
// id), or "" when absent.
func projectNumberOf(effectiveIdentity string) string {
	segs := strings.Split(effectiveIdentity, "/")
	for i, seg := range segs {
		if seg == "projects" && i+1 < len(segs) {
			return segs[i+1]
		}
	}
	return ""
}

// lastSegment returns the final path segment of a resource name (the engine id),
// used as the display-name fallback.
func lastSegment(name string) string {
	if i := strings.LastIndexByte(name, '/'); i >= 0 {
		return name[i+1:]
	}
	return name
}
