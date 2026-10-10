// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

//go:build !enterprise

package main

import (
	"github.com/olivaresai/olivares/core/runtime/sandboxrt"
	"github.com/olivaresai/olivares/modules/redteam"
)

func newRedteamOptions(*sandboxrt.Engine) []redteam.Option { return nil }
