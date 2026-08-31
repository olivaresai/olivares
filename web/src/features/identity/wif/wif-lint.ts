// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// WIF graph linter (ANT2-08) — the differential value of the WIF view. Pure,
// language-agnostic, unit-tested logic over the Workload-Identity-Federation
// objects. It DETECTS and HIGHLIGHTS; it never corrects, and it never creates/edits
// objects (writes are Console-only). The rules:
//   1. CEL over-broad — a Federation Rule whose match is too permissive ("CEL
//      conditions are security boundaries"), including an empty / bare-"*" / prefix-"*"
//      subject_prefix even when another axis narrows (stricter per-axis).
//   2. key-shadow footgun — ANTHROPIC_API_KEY / AUTH_TOKEN present shadows WIF in
//      the precedence; the org is "keyless" in name only → recommend `ant auth status`.
//   3. token-lifetime — token_lifetime_seconds outside the supported band 60–86400.
//   4. scope-over-broad — org-wide scope (org:admin / org:manage_tunnels) where a
//      workspace scope would do, or an unrecognised scope.
//   5. jwks-insecure — inline JWKS (does not rotate) — a hygiene signal.
//   6. drift — declared-vs-actual reconciliation: a live rule the operator never
//      declared (ungoverned), a declared rule that no longer exists live (stale), or an
//      orphan rule/issuer. Only fires on a reconciled graph (org:admin OAuth configured).
//
// The linter reads only metadata the ingest already exposes; it never sees key
// material (ca_cert is a presence boolean, never a PEM).
import type {
  WifGraphData,
  WifIssuer,
  WifLintFinding,
  WifRule,
  WifServiceAccount,
} from '../types'

/** Supported OAuth scopes for a federation rule (wif-reference). */
export const KNOWN_WIF_SCOPES = [
  'workspace:developer',
  'workspace:inference',
  'org:manage_tunnels',
  'org:admin',
] as const

/** Org-wide scopes that grant beyond a single workspace. */
const ORG_WIDE_SCOPES = new Set<string>(['org:manage_tunnels', 'org:admin'])

/** token_lifetime_seconds supported band (wif-reference). */
export const MIN_TOKEN_LIFETIME = 60
export const MAX_TOKEN_LIFETIME = 86_400
/** Soft ceiling: tokens at/above 12h approach the 24h max — least-privilege flag. */
export const LONG_TOKEN_LIFETIME = 43_200

/** Env vars whose mere presence shadows federation (footgun.go). */
export const SHADOW_ENV_VARS = [
  'ANTHROPIC_API_KEY',
  'ANTHROPIC_AUTH_TOKEN',
] as const

/** Effective scope of a rule ("" ⇒ workspace:developer, per federation.go). */
export function ruleScope(rule: Pick<WifRule, 'oauth_scope'>): string {
  return rule.oauth_scope && rule.oauth_scope.length > 0
    ? rule.oauth_scope
    : 'workspace:developer'
}

/** A CEL condition is a tautology (matches everything) → not a real boundary. */
function isTautologicalCel(cel: string): boolean {
  const c = cel.trim().toLowerCase().replace(/\s+/g, '')
  return c === '' || c === 'true' || c === '1==1' || c === 'true==true'
}

/** A rule has NO narrowing match criteria → it accepts any token from the issuer. */
function hasNoNarrowing(rule: WifRule): boolean {
  const noSubject = !rule.subject_prefix || rule.subject_prefix.trim() === ''
  const noAudience = !rule.audience || rule.audience.trim() === ''
  const noClaims = !rule.claims || Object.keys(rule.claims).length === 0
  const noCel = !rule.cel_condition || isTautologicalCel(rule.cel_condition)
  return noSubject && noAudience && noClaims && noCel
}

/** Classify a subject_prefix's breadth: "" (no constraint), "*" (matches everything), a
 *  prefix wildcard "foo*" (a whole class of subjects), or specific (not broad). */
function subjectBreadth(prefix?: string): { broad: boolean; kind: string } {
  const p = (prefix ?? '').trim()
  if (p === '') return { broad: true, kind: 'empty' }
  if (p === '*') return { broad: true, kind: 'wildcard' }
  if (p.endsWith('*')) return { broad: true, kind: 'prefix' }
  return { broad: false, kind: '' }
}

/** Rule 1 — CEL / match over-broad. Flags a rule with no narrowing at all or a
 *  tautological CEL, and (stricter per-axis) an empty / bare-"*" / prefix-"*"
 *  subject_prefix EVEN when another axis narrows — an over-broad subject is a breadth
 *  signal worth review. A bare/empty subject is a warning; a prefix wildcard is info. */
export function lintCelOverBroad(rules: WifRule[]): WifLintFinding[] {
  const out: WifLintFinding[] = []
  for (const rule of rules) {
    if (hasNoNarrowing(rule)) {
      out.push({
        rule: 'cel-over-broad',
        severity: 'warning',
        subjectRef: rule.rule_id,
        meta: {
          reason: 'no-match-criteria',
          serviceAccount: rule.service_account_id,
        },
      })
      continue
    }
    if (rule.cel_condition && isTautologicalCel(rule.cel_condition)) {
      out.push({
        rule: 'cel-over-broad',
        severity: 'warning',
        subjectRef: rule.rule_id,
        meta: { reason: 'tautological-cel', cel: rule.cel_condition },
      })
    }
    const { broad, kind } = subjectBreadth(rule.subject_prefix)
    if (broad) {
      out.push({
        rule: 'cel-over-broad',
        severity: kind === 'prefix' ? 'info' : 'warning',
        subjectRef: rule.rule_id,
        meta: { reason: 'over-broad-subject', subject: kind },
      })
    }
  }
  return out
}

/** Rule 2 — static key shadows WIF (the footgun). `keyShadow` comes from the REAL
 *  footgun Finding (kind=governance, subject_kind=anthropic.federation); it is NOT
 *  invented here. Only fires when federation is actually in use. */
export function lintKeyShadow(data: WifGraphData): WifLintFinding[] {
  const present = data.key_shadow?.present === true
  const federationInUse =
    data.rules.length > 0 || data.service_accounts.length > 0
  if (!present || !federationInUse) return []
  return [
    {
      rule: 'key-shadow',
      severity: 'error',
      subjectRef: data.key_shadow?.var ?? SHADOW_ENV_VARS[0],
      // recommendation is rendered by the view as `ant auth status`.
      meta: { var: data.key_shadow?.var ?? SHADOW_ENV_VARS[0] },
    },
  ]
}

/** Rule 3 — token lifetime out of band / long-lived. */
export function lintTokenLifetime(rules: WifRule[]): WifLintFinding[] {
  const out: WifLintFinding[] = []
  for (const rule of rules) {
    const ttl = rule.token_lifetime_seconds
    if (ttl === undefined || ttl === 0) continue // undeclared → backend defaults
    if (ttl < MIN_TOKEN_LIFETIME || ttl > MAX_TOKEN_LIFETIME) {
      out.push({
        rule: 'token-lifetime',
        severity: 'warning',
        subjectRef: rule.rule_id,
        meta: {
          lifetime: ttl,
          min: MIN_TOKEN_LIFETIME,
          max: MAX_TOKEN_LIFETIME,
          reason: 'out-of-band',
        },
      })
    } else if (ttl >= LONG_TOKEN_LIFETIME) {
      out.push({
        rule: 'token-lifetime',
        severity: 'info',
        subjectRef: rule.rule_id,
        meta: { lifetime: ttl, max: MAX_TOKEN_LIFETIME, reason: 'long-lived' },
      })
    }
  }
  return out
}

/** Rule 4 — scope over-broad / unrecognised. Lints both rules and service accounts. */
export function lintScope(
  rules: WifRule[],
  serviceAccounts: WifServiceAccount[] = [],
): WifLintFinding[] {
  const out: WifLintFinding[] = []
  // A live oauth_scope may be space-separated multi-scope; check each token so an
  // "org:admin workspace:developer" rule still flags the org-wide token.
  const check = (subjectRef: string, scopeValue: string) => {
    for (const scope of scopeValue.split(/\s+/).filter(Boolean)) {
      if (ORG_WIDE_SCOPES.has(scope)) {
        out.push({
          rule: 'scope-over-broad',
          severity: 'warning',
          subjectRef,
          meta: { scope, reason: 'org-wide' },
        })
      } else if (
        !KNOWN_WIF_SCOPES.includes(scope as (typeof KNOWN_WIF_SCOPES)[number])
      ) {
        out.push({
          rule: 'scope-over-broad',
          severity: 'info',
          subjectRef,
          meta: { scope, reason: 'unrecognised' },
        })
      }
    }
  }
  for (const rule of rules) check(rule.rule_id, ruleScope(rule))
  for (const sa of serviceAccounts) {
    if (sa.oauth_scope) check(sa.id, sa.oauth_scope)
  }
  return out
}

/** Rule 5 — inline JWKS does not rotate. */
export function lintJwks(rules: WifRule[]): WifLintFinding[] {
  return rules
    .filter((r) => r.jwks_mode === 'inline')
    .map((r) => ({
      rule: 'jwks-insecure' as const,
      severity: 'info' as const,
      subjectRef: r.rule_id,
      meta: { mode: 'inline' },
    }))
}

/** Rule 6 — declared-vs-actual drift. Reads the per-object `source` provenance
 *  the backend set during reconciliation, plus structural orphan resolution within the
 *  graph. It ONLY fires on a reconciled graph (data.reconciliation.reconciled), so a
 *  declared-only graph (no org:admin token) never reports spurious drift:
 *    - a live-only rule the operator never declared (ungoverned/shadow path) → error;
 *    - a declared rule that no longer exists live (stale governance) → warning;
 *    - a rule referencing an issuer/service-account absent from the graph (orphan) → warning;
 *    - a live issuer referenced by no rule (config debt) → info. */
export function lintDrift(data: WifGraphData): WifLintFinding[] {
  if (!data.reconciliation?.reconciled) return []
  const out: WifLintFinding[] = []
  const issuerIds = new Set(data.issuers.map((i: WifIssuer) => i.id))
  const saIds = new Set(
    data.service_accounts.map((s: WifServiceAccount) => s.id),
  )
  const referencedIssuers = new Set<string>()

  for (const rule of data.rules) {
    if (rule.issuer_id) referencedIssuers.add(rule.issuer_id)
    if (rule.source === 'live') {
      out.push({
        rule: 'drift',
        severity: 'error',
        subjectRef: rule.rule_id,
        meta: { reason: 'undeclared-rule', serviceAccount: rule.service_account_id },
      })
    } else if (rule.source === 'declared') {
      out.push({
        rule: 'drift',
        severity: 'warning',
        subjectRef: rule.rule_id,
        meta: { reason: 'declared-not-live' },
      })
    }
    const missingIssuer = !!rule.issuer_id && !issuerIds.has(rule.issuer_id)
    const missingSa =
      !!rule.service_account_id && !saIds.has(rule.service_account_id)
    if (missingIssuer || missingSa) {
      out.push({
        rule: 'drift',
        severity: 'warning',
        subjectRef: rule.rule_id,
        meta: { reason: 'orphan-rule' },
      })
    }
  }
  for (const iss of data.issuers) {
    if (iss.source === 'declared') continue // a declared-only issuer is covered by its rule's drift
    if (!referencedIssuers.has(iss.id)) {
      out.push({
        rule: 'drift',
        severity: 'info',
        subjectRef: iss.id,
        meta: { reason: 'orphan-issuer' },
      })
    }
  }
  return out
}

/** Run every WIF lint rule and return all findings (worst severity first). */
export function lintWifGraph(data: WifGraphData): WifLintFinding[] {
  const findings = [
    ...lintKeyShadow(data),
    ...lintCelOverBroad(data.rules),
    ...lintScope(data.rules, data.service_accounts),
    ...lintTokenLifetime(data.rules),
    ...lintJwks(data.rules),
    ...lintDrift(data),
  ]
  const rank: Record<WifLintFinding['severity'], number> = {
    error: 0,
    warning: 1,
    info: 2,
  }
  return findings.sort((a, b) => rank[a.severity] - rank[b.severity])
}

/** Count findings by severity (for the lint summary badge). */
export function lintSummary(findings: WifLintFinding[]): {
  error: number
  warning: number
  info: number
} {
  return {
    error: findings.filter((f) => f.severity === 'error').length,
    warning: findings.filter((f) => f.severity === 'warning').length,
    info: findings.filter((f) => f.severity === 'info').length,
  }
}
