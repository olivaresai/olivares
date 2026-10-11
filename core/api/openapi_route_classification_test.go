// SPDX-FileCopyrightText: 2026 Olivares AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package api

import "testing"

func TestAuthZENRouteClassificationPublicReason(t *testing.T) {
	want := RouteClassification{
		Class:  classProtocol,
		Reason: "OpenID AuthZEN 1.0 Authorization API at the spec's conventional paths; server.go mounts it for an external PEP and records that it is not part of the SDK/OpenAPI surface.",
	}
	routes := ClassifiedRoutes()
	for _, route := range []string{
		"GET /.well-known/authzen-configuration",
		"POST /access/v1/evaluation",
		"POST /access/v1/evaluations",
		"POST /access/v1/search/subject",
		"POST /access/v1/search/resource",
		"POST /access/v1/search/action",
		"POST /access/v1/access-review/export",
	} {
		t.Run(route, func(t *testing.T) {
			if got, ok := routes[route]; !ok || got != want {
				t.Errorf("ClassifiedRoutes()[%q] = %+v, present=%t; want %+v", route, got, ok, want)
			}
		})
	}
}
