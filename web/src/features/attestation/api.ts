// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// Supply-chain attestation endpoint wrapper + query keys — the live read
// EXISTS since. Two provenance classes:
//
//  • MEASURED — `binary` reads GET /v1/m/observability/attestation
//    (modules/observability attestation.go): facts the RUNNING process can prove
//    about itself (ldflags version/commit, debug.ReadBuildInfo, crypto/fips140
//    mode, a stream-SHA256 of its own executable) plus the MEASURED release state:
//    published only when this binary carries both an orderable main.version stamp
//    and an embedded OTA anchor, not_published (with the reason) otherwise, and
//    signature_status not_verified in either case — a running process cannot check
//    its own detached signature. Always 200 where mounted.
//    The verdict travels with `release.provenance`, which says both facts are
//    link-time values chosen by whoever linked the binary: SELF-DECLARED, never an
//    attestation that a release was published (measured 2026-08-14 — a plain
//    `go build -ldflags` reaches published:true). The view renders that note
//    verbatim beside the badge.
//  • DECLARED — the release-verification CONTRACT (what ships with a release, the
//    verify commands, the SLAs) stays in ./attestation.data.ts: the engine cannot
//    observe repository or CI state, so serving that table over HTTP would add no
//    truth. The view labels it as declared reference, as before.
//
// The view PRESENTS both; it NEVER re-verifies cryptographically in the browser
// (no cosign / slsa-verifier / fetch-and-verify — ARCHITECTURE.md).
import { http } from '@/lib/api'
import type { RunningBinaryAttestation } from './types'

/** The observability module's route namespace (the attestation read lives there —
 *  it is measured truth about the running engine, not a compliance artifact). */
const BASE = '/v1/m/observability'

export const attestationApi = {
  /** Measured attestation of the RUNNING binary + honest release/pipeline state. */
  binary: () => http.get<RunningBinaryAttestation>(`${BASE}/attestation`),
}

/** Tenant-scoped query keys (query.ts contract: tenant id first). */
export const attestationKeys = {
  all: (tenant: string | null) => ['attestation', tenant] as const,
  binary: (tenant: string | null) => ['attestation', tenant, 'binary'] as const,
  release: (tenant: string | null, version: string) =>
    ['attestation', tenant, 'release', version] as const,
}
