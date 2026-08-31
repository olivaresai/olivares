// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package reporting

import "github.com/olivaresai/olivares/core/cron"

// cron.go is now a thin alias over core/cron. The 5-field grammar this
// module authored moved to the engine VERBATIM — same syntax, same UTC/minute
// semantics, same day-of-month/day-of-week OR rule, and its test vectors moved
// with it as the compatibility contract.
//
// It moved because a cron expression became an ENFORCEMENT input: the
// routine-policy cadence floor and cron allowlist are enforced in
// modules/orchestration, and a governance PEP must not import an unrelated
// product module (nor carry a fourth private copy of the grammar — parser
// disagreement at a policy boundary is a permanent bypass). See core/cron.

// CronSpec is a parsed 5-field cron expression.
type CronSpec = cron.Spec

// ParseCronSpec parses and validates a 5-field cron expression.
func ParseCronSpec(spec string) (CronSpec, error) { return cron.Parse(spec) }
