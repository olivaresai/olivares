// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package vertex

// This file holds JSON wire shapes shared across the connector that do not belong to a
// single source file. Only the minimal-data fields the connector needs are mapped; the
// full upstream payload may carry more, and they are ignored. (Cloud Monitoring,
// billing-export and Model Armor shapes live next to their readers in usage.go, cost.go
// and modelarmor.go respectively.)

// publisherModel is the subset of GET /v1/publishers/{publisher}/models/{id} the catalog
// reads to enrich a declared model with its lifecycle signal. The publisher-model GET does
// NOT return per-token pricing or the cross-vendor capability flags, so only launchStage /
// versionState (the deprecation signal) are read; pricing + capabilities stay declared.
type publisherModel struct {
	Name         string `json:"name"`
	VersionID    string `json:"versionId"`
	LaunchStage  string `json:"launchStage"`  // GA|PUBLIC_PREVIEW|DEPRECATED|…
	VersionState string `json:"versionState"` // PUBLISHER_MODEL_VERSION_STATE_{STABLE,DEPRECATED,…}
}
