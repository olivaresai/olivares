// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package api

import "google.golang.org/grpc"

// Test-only seams for the black-box api_test package.

// SwapRouteDeprecationsForTest replaces the package deprecation table and
// returns a restore func. Swap BEFORE building a Server: New indexes the table
// once (and an empty index keeps the middleware out of the chain entirely).
func SwapRouteDeprecationsForTest(list []RouteDeprecation) (restore func()) {
	old := routeDeprecations
	routeDeprecations = list
	return func() { routeDeprecations = old }
}

// RouteDeprecationsForTest exposes the real (production) deprecation table so
// the policy tests can hold every entry to the published windows.
func RouteDeprecationsForTest() []RouteDeprecation { return routeDeprecations }

// CanonicalRoutePatternForTest exposes the chi-pattern→spec-path mapping so the
// route-matching test compares routes exactly as the middleware will.
func CanonicalRoutePatternForTest(p string) string { return canonicalRoutePattern(p) }

// MinSupportWindowMonthsForTest exposes the tier windows so the policy test
// enforces the SAME declaration the docs commit to (no drifting copy).
func MinSupportWindowMonthsForTest(t StabilityTier) int { return t.minSupportWindowMonths() }

// LeaderGateStreamInterceptorForTest exposes the HA leader-routing stream
// interceptor so a test can drive a long-lived stream across a demotion without
// standing up a real collector (stage-2).
func (s *Server) LeaderGateStreamInterceptorForTest() grpc.StreamServerInterceptor {
	return s.grpcLeaderGateStreamInterceptor
}

// StatusForTest exposes the single REST/gRPC error mapper so the black-box tests can hold
// it to its contract directly. Going through a handler would test one route's plumbing;
// the property that matters is the MAPPING, which every route and both protocols share.
func StatusForTest(err error) (int, string) { return statusFor(err) }
