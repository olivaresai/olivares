// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

/**
 * looksLikeCredential is a client-side mirror of the engine's inline-credential
 * guard (which 400s a config/spec/endpoint that embeds a secret). It catches
 * basic-auth userinfo (user:pass@host) and token/secret/password=… assignments so
 * the operator is warned BEFORE the round-trip. The backend remains authoritative —
 * this is a courtesy that keeps secrets out of reference fields, never the gate.
 * Used by the capabilities / deploy / catalog config editors (docs/SECURITY-HARDENING.md).
 */
const CREDENTIAL_RE =
  /(:\/\/[^/@\s:]+:[^/@\s]+@)|\b(token|secret|password|passwd|api_key|apikey|access_key|client_secret)\s*=/i

export function looksLikeCredential(v: string | undefined | null): boolean {
  return !!v && CREDENTIAL_RE.test(v)
}
