// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"fmt"
	"time"

	"github.com/olivaresai/olivares/modules/sessions"
)

func main() {
	defer func() {
		fmt.Printf("panic=%v\nafter=%s\n", recover(), sessions.CheckedRuntimeInputCeiling())
	}()
	fmt.Printf("before=%s\n", sessions.CheckedRuntimeInputCeiling())
	sessions.SetTestCheckedRuntimeInputCeiling(time.Second)
}
