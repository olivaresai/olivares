// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// Zod's object parser has a JIT fast path that compiles a validator with `new
// Function`. To decide whether it may use it, Zod PROBES by calling `new Function("")`
// inside a try/catch (zod/v4/core/util.ts, `allowsEval`). Under this app's CSP the
// probe cannot succeed — `script-src` grants no 'unsafe-eval' and
// `require-trusted-types-for 'script'` governs string-to-code — so Zod already runs
// the interpreted path everywhere. The probe's THROW is caught, but the browser still
// reports the attempt: one `securitypolicyviolation` (effectiveDirective
// `require-trusted-types-for`, sample `Function|…`) and one red console error on
// EVERY page load, including /setup — the first screen a customer is ever shown.
//
// `jitless: true` tells Zod what the CSP already decided, so it skips the probe
// (the library documents this exact case in that function). Nothing is disabled that
// was working: the fast path was unreachable before this file existed. What changes
// is that a security product stops opening with a security error it cannot act on.
//
// This module is imported FIRST in main.tsx and is side-effect only. Order is load-
// bearing: `allowsEval` is memoised on first access, and that access happens when a
// module builds a `z.object(...)` at import time. Configure after that and the probe
// has already run.
import { config } from 'zod'

config({ jitless: true })
