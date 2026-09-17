// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package api

import "net/http"

// The published contract of the HC1 host-tools READ.
//
// It exists for the same reason its launch-readiness sibling does: without a typed
// response the beta reflector publishes the generic `{"type":"object"}` envelope, and
// a console that has to re-derive "which states exist", "which origins exist" or
// "which of these codes means an incomplete observation" from prose ends up
// re-implementing the server's vocabulary in TypeScript. The enums below ARE the
// server's vocabulary, so the generated client types them and the browser cannot
// invent a sixth state or read an unknown as an absence.
//
// ⛔ THE ENUMS ARE CLOSED ON PURPOSE AND MUST TRACK modules/sessions/host_tools.go.
// A state or group code that skipped this file would be published as an
// unconstrained string and silently accepted by every generated client.
// TestLaunchHostToolsPublishedContract (modules/sessions) asserts the agreement
// against the REAL registered beta document and the REAL DTO.
//
// This publishes the existing behavior; it changes no handler and no HTTP shape.
// core/api imports nothing from modules/sessions: the vocabulary is restated here
// and the module's own test is what keeps the two from drifting.

func sessionsHostToolsRoute(r moduleRoute) bool {
	return r.ns == "sessions" && r.method == http.MethodGet &&
		r.pattern == "/provider-profiles/{ref}/host-tools"
}

// sessionsHostToolsResponses publishes every status this read can answer.
//
// The 200 is an observation of what THIS node can see, never an authorization and
// never an installation: an adapter that could not inspect is a 200 whose state is
// unknown, not a 503. The 503 means that no result was published because the
// profile store was unavailable or the request was canceled
// mid-observation and the partial observation was discarded rather than published.
func sessionsHostToolsResponses() map[string]any {
	return oaObj(
		"200", oaJSONRespSchema(
			"The official provider CLI candidates this node observed for the profile, counted into closed groups. It is a dated observation and grants nothing: it starts no launch, installs nothing and registers nothing. Cache-Control: no-store is returned on every answer, so a client must not cache it.",
			sessionsHostToolsSchema()),
		"400", oaJSONResp("malformed profile reference, or a non-empty query string: this route declares no query parameters and does not ignore one"),
		"401", oaJSONResp("unauthenticated"),
		"403", oaJSONResp("the caller lacks sessions:profile:read; nothing was inspected"),
		"404", oaJSONResp("no such profile in this tenant (a profile of another tenant is indistinguishable from an absent one)"),
		"409", oaJSONRespSchema(
			"The profile changed during the observation, so it straddled two states; it was discarded and must be read again. The body carries the same ratified stable identifier profile_changed the readiness sibling uses, so a client detects this conflict by code and never by matching the message text.",
			sessionsLaunchReadinessConflictSchema()),
		"429", oaJSONResp("rate limited"),
		"503", oaJSONResp("the profile store is unavailable, or the request was canceled before the observation completed; no observation result was published"),
	)
}

// sessionsHostToolsStateSchema is the five-value observation vocabulary.
//
// unknown and none_observed are DIFFERENT answers and the enum keeps them that way:
// none_observed is "this node looked and found no candidate", unknown is "this node
// could not complete the look". Neither is proof that the program is absent from the
// host, and neither is proof it is absent anywhere else.
func sessionsHostToolsStateSchema() map[string]any {
	return oaObj("type", "string",
		"description", "observed means candidates were counted and groups is non-empty. none_observed means the observation completed and found no candidate; it is not proof of absence elsewhere on the host or in another environment. unsupported_driver means this runtime has no observer for the profile's driver. unknown is an INCOMPLETE observation — the adapter could not answer — and is never rewritten as an absence. not_checked_in_this_environment means the node is non-local or has no persistent execution-environment identity, so no observer was called at all. In every state other than observed, groups is an empty array.",
		"enum", oaEnum("observed", "none_observed", "unsupported_driver", "unknown", "not_checked_in_this_environment"))
}

// sessionsHostToolsGroupSchema is ONE counted group. Every field is required: a
// group that omitted a dimension would let a client guess it.
//
// It carries a COUNT and four closed codes, and deliberately no raw path, note,
// argv, environment value, credential or OS error string: the console renders
// localized copy from the codes, and an observation of the host filesystem is not a
// place to publish its contents.
func sessionsHostToolsGroupSchema() map[string]any {
	return sessionsClosureClosedObject(oaObj(
		"origin", oaObj("type", "string",
			"description", "Where the candidate was found. managed is an installation this deployment administers; vendor-default is the provider's own default location; path is a PATH lookup; named is an explicitly configured program; unknown is a candidate whose origin the observer could not classify.",
			"enum", oaEnum("managed", "vendor-default", "path", "named", "unknown")),
		"match", oaObj("type", "string",
			"description", "How the candidate relates to the installation evidence. registered and manifest-corroborated describe matching evidence; unregistered-observed means a candidate was found without that evidence; unverified means verification could not be completed; damaged means an integrity or installation check failed; unknown means the observer could not classify it. These observations do not authorize a launch.",
			"enum", oaEnum("registered", "manifest-corroborated", "unregistered-observed", "unverified", "damaged", "unknown")),
		"executable", oaObj("type", "boolean",
			"description", "Whether the observed file has an execute permission bit set. This metadata check does not prove that the current user can execute the file or that a launch will succeed."),
		"configured", oaObj("type", "string",
			"description", "Whether the candidate is the program this profile is effectively configured to run. same and different are both observations; unknown means the comparison could not be made.",
			"enum", oaEnum("same", "different", "unknown")),
		"count", oaObj("type", "integer", "minimum", 1,
			"description", "How many candidates fell into this group. A group is only published when it counted at least one candidate."),
	), "origin", "match", "executable", "configured", "count")
}

// sessionsHostToolsSchema is the closed 200 body: all eight fields required.
func sessionsHostToolsSchema() map[string]any {
	return sessionsClosureClosedObject(oaObj(
		"profile_ref", oaObj("type", "string"),
		"profile_version", sessionsInt64Schema(),
		"driver", oaObj("type", "string",
			"description", "The profile's normalized driver key. The set is open; observation support depends on the node's wired observer."),
		"environment_ref", oaObj("type", "string",
			"description", "The execution environment that administers this profile."),
		"evaluated_environment_ref", oaObj("type", "string",
			"description", "The execution environment that answered. ALWAYS present, and empty when this node has no persistent execution-environment identity — the case state reports as not_checked_in_this_environment. An empty string is the answer, not a missing field."),
		"observed_at", sessionsTimestampSchema(),
		"state", sessionsHostToolsStateSchema(),
		"groups", oaObj("type", "array",
			"description", "The counted groups, sorted. Non-empty only when state is observed; an empty array in every other state. A group counts candidates and never names them: this read publishes no path and no program argument.",
			"items", sessionsHostToolsGroupSchema()),
	),
		"profile_ref", "profile_version", "driver", "environment_ref",
		"evaluated_environment_ref", "observed_at", "state", "groups",
	)
}
