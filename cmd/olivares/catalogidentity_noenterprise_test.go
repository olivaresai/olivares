//go:build !enterprise || !addon_ids

// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only

package main

import "testing"

func editionRosterCatalogKinds(*testing.T) []string  { return nil }
func editionDirectoryAliasesTest() map[string]string { return nil }
