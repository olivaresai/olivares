//go:build !enterprise

// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only

package main

func compliancePacksAuthorizerForBuild() func(string) error { return nil }
