// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// Attestation presentational pieces — PURE: props in, JSX out, no fetching, no
// auth. Two provenance classes: the MEASURED panels (BinaryPanel /
// ReleaseStatePanel) render the live running-binary attestation; the rest render
// the DECLARED release-verification contract. They PRESENT status; they NEVER
// re-verify cryptographically in the browser (ARCHITECTURE.md). Charts are wrapped in
// <AccessibleChart>; colours come ONLY from useChartTheme(); numbers/dates go
// through @/lib/format.
import { useMemo } from 'react'
import { useTranslation } from 'react-i18next'
import { Badge } from '@/components/ui/badge'
import { EmptyState } from '@/components/ui/empty-state'
import { AccessibleChart } from '@/components/data/accessible-chart'
import { DataTable, type TableColumn } from '@/components/data/data-table'
import { RadialGauge } from '@/components/charts'
import {
  CaveatNotice,
  ControlStatusBadge,
  HashChip,
  IntegrityBadge,
} from '@/features/_intel'
import { formatDateTime } from '@/lib/format'
import type {
  AirgapContract,
  BinaryAttestation,
  HelmChartContract,
  PatchVelocity,
  PipelineState,
  ReleaseArtifact,
  ReleaseState,
  RemediationSla,
  SbomCisaElement,
  SbomContract,
  ScorecardCheck,
  ScorecardContract,
  SlsaProvenance,
  VerificationStatus,
  VexContract,
  VexStatement,
} from './types'

// --- shared status badge -----------------------------------------------------

/** The DECLARED verification status as a badge. NOT a live cryptographic result:
 *  `declared` = the contract says it ships + verifies (not yet exercised, beta);
 *  `skip_when_absent` = verify-release.sh skips with a note if the attestation/tool is
 *  missing; `not_run` = no release exists yet. */
export function VerifyStatusBadge({ status }: { status: VerificationStatus }) {
  const { t } = useTranslation('attestation')
  const variant =
    status === 'declared'
      ? 'info'
      : status === 'skip_when_absent'
        ? 'warning'
        : 'neutral'
  return (
    <Badge variant={variant}>
      {t(`status.${status}`, { defaultValue: status })}
    </Badge>
  )
}

/** Trust mechanism (keyless / key-based / both / transitive) — both are first-class,
 *  neither canonical. */
export function TrustBadge({ mechanism }: { mechanism: string }) {
  const { t } = useTranslation('attestation')
  return (
    <Badge variant="outline">
      {t(`trust.${mechanism}`, { defaultValue: mechanism })}
    </Badge>
  )
}

// --- 1. artifact table -------------------------------------------------------

export function ArtifactTable({ artifacts }: { artifacts: ReleaseArtifact[] }) {
  const { t, i18n } = useTranslation('attestation')
  const columns = useMemo<TableColumn<ReleaseArtifact>[]>(
    () => [
      {
        accessorKey: 'name',
        header: t('artifacts.columns.artifact'),
        cell: ({ row }) => (
          <span className="font-mono text-xs break-all">
            {row.original.name}
          </span>
        ),
      },
      {
        accessorKey: 'produced_by',
        header: t('artifacts.columns.producedBy'),
        cell: ({ row }) => (
          <span className="text-xs text-muted-foreground">
            {row.original.produced_by}
          </span>
        ),
      },
      {
        accessorKey: 'signature_trust',
        header: t('artifacts.columns.signatureTrust'),
        cell: ({ row }) => (
          <span className="text-xs text-foreground">
            {row.original.signature_trust}
          </span>
        ),
      },
      {
        accessorKey: 'trust_mechanism',
        header: t('artifacts.columns.mechanism'),
        cell: ({ row }) => (
          <TrustBadge mechanism={row.original.trust_mechanism} />
        ),
      },
      {
        accessorKey: 'status',
        header: t('artifacts.columns.status'),
        cell: ({ row }) => <VerifyStatusBadge status={row.original.status} />,
      },
      {
        accessorKey: 'scp',
        header: t('artifacts.columns.control'),
        cell: ({ row }) =>
          row.original.scp ? (
            <span className="font-mono text-xs text-muted-foreground">
              {row.original.scp}
            </span>
          ) : (
            <span className="text-muted-foreground">—</span>
          ),
      },
    ],
    [t, i18n.language],
  )
  return (
    <DataTable<ReleaseArtifact>
      columns={columns}
      data={artifacts}
      getRowId={(r) => r.id}
      label={t('artifacts.title')}
      empty={
        <EmptyState
          title={t('empty.artifact.title')}
          description={t('empty.artifact.description')}
        />
      }
    />
  )
}

// --- reference row helper ----------------------------------------------------

function Row({
  label,
  children,
  mono = false,
}: {
  label: React.ReactNode
  children: React.ReactNode
  mono?: boolean
}) {
  return (
    <div className="grid grid-cols-1 gap-x-3 gap-y-0.5 py-1.5 sm:grid-cols-[minmax(0,13rem)_1fr]">
      <dt className="text-xs font-medium text-muted-foreground">{label}</dt>
      <dd
        className={
          mono
            ? 'min-w-0 font-mono text-xs break-all text-foreground'
            : 'min-w-0 text-xs text-foreground'
        }
      >
        {children}
      </dd>
    </div>
  )
}

// --- 2. SLSA provenance ------------------------------------------------------

export function SlsaPanel({ slsa }: { slsa: SlsaProvenance }) {
  const { t } = useTranslation('attestation')
  return (
    <div className="flex flex-col gap-3">
      <div className="flex flex-wrap items-center gap-2">
        <Badge variant="success" className="font-semibold">
          {t('slsa.buildLevel', { level: slsa.build_level })}
        </Badge>
        {slsa.by_construction ? (
          <ControlStatusBadge status="by_design" />
        ) : null}
        <VerifyStatusBadge status={slsa.status} />
        <span className="font-mono text-xs text-muted-foreground">
          {slsa.scp}
        </span>
      </div>
      <dl>
        <Row label={t('slsa.predicate')}>{slsa.predicate_type}</Row>
        <Row label={t('slsa.generator')} mono>
          {slsa.generator}
        </Row>
        <Row label={t('slsa.pin')}>
          <Badge variant="warning">
            {t(`slsa.pinKind.${slsa.reusable_workflow_pin}`, {
              defaultValue: slsa.reusable_workflow_pin,
            })}
          </Badge>
        </Row>
        <Row label={t('slsa.verifyCommand')} mono>
          {slsa.verify_command}
        </Row>
      </dl>
      <CaveatNotice tone="info">{t('slsa.honesty')}</CaveatNotice>
    </div>
  )
}

// --- 3. SBOM + CISA checklist ------------------------------------------------

export function SbomPanel({ sbom }: { sbom: SbomContract }) {
  const { t, i18n } = useTranslation('attestation')

  const cisaColumns = useMemo<TableColumn<SbomCisaElement>[]>(
    () => [
      {
        accessorKey: 'key',
        header: t('sbom.cisa.columns.element'),
        cell: ({ row }) => (
          <span className="text-xs font-medium text-foreground">
            {row.original.key}
          </span>
        ),
      },
      {
        accessorKey: 'enforcement',
        header: t('sbom.cisa.columns.enforcement'),
        cell: ({ row }) => (
          <Badge
            variant={row.original.enforcement === 'hard' ? 'danger' : 'warning'}
          >
            {t(`sbom.cisa.enforcement.${row.original.enforcement}`, {
              defaultValue: row.original.enforcement,
            })}
          </Badge>
        ),
      },
      {
        accessorKey: 'carried_by',
        header: t('sbom.cisa.columns.carriedBy'),
        cell: ({ row }) => (
          <span className="text-xs text-muted-foreground">
            {row.original.carried_by}
          </span>
        ),
      },
    ],
    [t, i18n.language],
  )

  return (
    <div className="flex flex-col gap-4">
      <div className="flex flex-wrap items-center gap-2">
        {sbom.formats.map((f) => (
          <Badge key={f.name} variant="outline">
            {f.name} {f.version}
          </Badge>
        ))}
        <VerifyStatusBadge status={sbom.status} />
        <span className="font-mono text-xs text-muted-foreground">
          {sbom.scp}
        </span>
      </div>
      <dl>
        {sbom.formats.map((f) =>
          f.note ? (
            <Row key={f.name} label={`${f.name} ${f.version}`}>
              {f.note}
            </Row>
          ) : null,
        )}
        <Row label={t('sbom.attestation')} mono>
          {sbom.attestation_predicate}
        </Row>
        <Row label={t('sbom.linter')} mono>
          {sbom.linter}
        </Row>
      </dl>

      <div className="flex flex-col gap-2">
        <p className="text-sm font-medium text-foreground">
          {t('sbom.cisa.title')}
        </p>
        {/* DRAFT, throughout: pre-decisional public-comment, NOT law/finalized. */}
        <CaveatNotice tone="warning">
          <span className="font-semibold text-warning">
            {t('sbom.cisa.draftTag')}
          </span>{' '}
          {t('sbom.cisa.draft')}
        </CaveatNotice>
        <DataTable<SbomCisaElement>
          columns={cisaColumns}
          data={sbom.cisa_elements}
          getRowId={(r) => r.key}
          label={t('sbom.cisa.title')}
          empty={
            <EmptyState
              title={t('empty.sbom.title')}
              description={t('empty.sbom.description')}
            />
          }
        />
      </div>
    </div>
  )
}

// --- 4. OpenVEX --------------------------------------------------------------

export function VexPanel({ vex }: { vex: VexContract }) {
  const { t, i18n } = useTranslation('attestation')
  const columns = useMemo<TableColumn<VexStatement>[]>(
    () => [
      {
        accessorKey: 'vuln_id',
        header: t('vex.columns.vuln'),
        cell: ({ row }) => (
          <span className="font-mono text-xs">{row.original.vuln_id}</span>
        ),
      },
      {
        accessorKey: 'status',
        header: t('vex.columns.status'),
        cell: ({ row }) => {
          const s = row.original.status
          const variant =
            s === 'not_affected' || s === 'fixed'
              ? 'success'
              : s === 'affected'
                ? 'danger'
                : 'warning'
          return (
            <Badge variant={variant}>
              {t(`vex.status.${s}`, { defaultValue: s })}
            </Badge>
          )
        },
      },
      {
        accessorKey: 'justification',
        header: t('vex.columns.justification'),
        cell: ({ row }) =>
          row.original.justification ? (
            <span className="font-mono text-xs text-muted-foreground">
              {row.original.justification}
            </span>
          ) : (
            <span className="text-muted-foreground">—</span>
          ),
      },
      {
        accessorKey: 'author',
        header: t('vex.columns.author'),
        cell: ({ row }) => (
          <span className="text-xs text-muted-foreground">
            {row.original.author}
          </span>
        ),
      },
    ],
    [t, i18n.language],
  )
  return (
    <div className="flex flex-col gap-3">
      <div className="flex flex-wrap items-center gap-2">
        <VerifyStatusBadge status={vex.status} />
        <span className="font-mono text-xs text-muted-foreground">
          {vex.scp}
        </span>
      </div>
      <p className="text-xs text-muted-foreground">{vex.driver}</p>
      <DataTable<VexStatement>
        columns={columns}
        data={vex.statements}
        getRowId={(r) => r.id}
        label={t('vex.title')}
        empty={
          <EmptyState
            title={t('empty.vex.title')}
            description={t('empty.vex.description')}
          />
        }
      />
      <p className="font-mono text-xs text-muted-foreground">
        {vex.attestation_predicate}
      </p>
    </div>
  )
}

// --- 5. OpenSSF Scorecard ----------------------------------------------------

export function ScorecardPanel({
  scorecard,
}: {
  scorecard: ScorecardContract
}) {
  const { t, i18n } = useTranslation('attestation')

  const checkColumns = useMemo<TableColumn<ScorecardCheck>[]>(
    () => [
      {
        accessorKey: 'name',
        header: t('scorecard.columns.check'),
        cell: ({ row }) => (
          <span className="font-mono text-xs text-foreground">
            {row.original.name}
          </span>
        ),
      },
      {
        accessorKey: 'what',
        header: t('scorecard.columns.what'),
        cell: ({ row }) => (
          <span className="text-xs text-foreground">{row.original.what}</span>
        ),
      },
      {
        accessorKey: 'evidence',
        header: t('scorecard.columns.evidence'),
        cell: ({ row }) => (
          <span className="text-xs text-muted-foreground">
            {row.original.evidence}
          </span>
        ),
      },
    ],
    [t, i18n.language],
  )

  // The gauge shows the COUNT of declared checks (a proportion of the four tracked),
  // NOT a fabricated numeric Scorecard score — that lives externally on
  // api.scorecard.dev. We present "4 of 4 checks declared", never an invented score.
  const declared = scorecard.checks.length
  const total = scorecard.checks.length
  const pct = total > 0 ? (declared / total) * 100 : 0

  return (
    <div className="flex flex-col gap-4">
      <div className="grid gap-4 lg:grid-cols-[auto_1fr]">
        <AccessibleChart<ScorecardCheck>
          title={t('scorecard.gauge.title')}
          summary={t('scorecard.gauge.summary', { declared, total })}
          columns={checkColumns}
          data={scorecard.checks}
          getRowId={(r) => r.name}
          hideTitle
          empty={
            <EmptyState
              title={t('empty.checkChart.title')}
              description={t('empty.checkChart.description')}
            />
          }
        >
          <div className="flex items-center justify-center p-2">
            <RadialGauge
              value={pct}
              tone="accent"
              label={`${declared}/${total}`}
              caption={t('scorecard.gauge.caption')}
              size={140}
            />
          </div>
        </AccessibleChart>
        <div className="flex flex-col gap-2">
          <p className="text-sm font-medium text-foreground">
            {t('scorecard.checksTitle')}
          </p>
          <DataTable<ScorecardCheck>
            columns={checkColumns}
            data={scorecard.checks}
            getRowId={(r) => r.name}
            label={t('scorecard.checksTitle')}
            empty={
              <EmptyState
                title={t('empty.scorecard.title')}
                description={t('empty.scorecard.description')}
              />
            }
          />
        </div>
      </div>
      <dl>
        <Row label={t('scorecard.schedule')}>{scorecard.schedule}</Row>
      </dl>
      {/* Honest: lives on the public GitHub mirror / api.scorecard.dev (external). */}
      <CaveatNotice tone="info">{scorecard.external_note}</CaveatNotice>
    </div>
  )
}

// --- 6. CVE remediation SLA + patch velocity ---------------------------------

export function SlaPanel({ pv }: { pv: PatchVelocity }) {
  const { t, i18n } = useTranslation('attestation')
  const columns = useMemo<TableColumn<RemediationSla>[]>(
    () => [
      {
        accessorKey: 'severity',
        header: t('sla.columns.severity'),
        cell: ({ row }) => {
          const s = row.original.severity
          const variant =
            s === 'critical'
              ? 'danger'
              : s === 'high'
                ? 'danger'
                : s === 'medium'
                  ? 'warning'
                  : 'neutral'
          return (
            <Badge
              variant={variant}
              className={
                s === 'critical' ? 'font-semibold uppercase' : undefined
              }
            >
              {t(`sla.severity.${s}`, { defaultValue: s })}
            </Badge>
          )
        },
      },
      {
        accessorKey: 'cvss_range',
        header: t('sla.columns.cvss'),
        cell: ({ row }) => (
          <span className="font-mono text-xs text-muted-foreground">
            {row.original.cvss_range}
          </span>
        ),
      },
      {
        accessorKey: 'target',
        header: t('sla.columns.target'),
        cell: ({ row }) => (
          <span className="text-xs font-medium text-foreground">
            {row.original.target}
          </span>
        ),
      },
    ],
    [t, i18n.language],
  )
  return (
    <div className="flex flex-col gap-4">
      <DataTable<RemediationSla>
        columns={columns}
        data={pv.sla}
        getRowId={(r) => r.severity}
        label={t('sla.title')}
        empty={
          <EmptyState
            title={t('empty.sla.title')}
            description={t('empty.sla.description')}
          />
        }
      />
      <CaveatNotice tone="warning">{pv.kev_rule}</CaveatNotice>

      <div className="flex flex-col gap-2">
        <div className="flex flex-wrap items-center gap-2">
          <p className="text-sm font-medium text-foreground">
            {t('sla.cadenceTitle')}
          </p>
          <VerifyStatusBadge status={pv.status} />
          <span className="font-mono text-xs text-muted-foreground">
            {pv.scp}
          </span>
        </div>
        <dl>
          <Row label={t('sla.schedule')}>{pv.schedule}</Row>
        </dl>
        <ol className="flex list-decimal flex-col gap-1 pl-5 text-xs text-muted-foreground">
          {pv.steps.map((s) => (
            <li key={s}>{s}</li>
          ))}
        </ol>
      </div>
    </div>
  )
}

// --- 7. Air-gap bundle + OCI Helm chart --------------------------------------

export function AirgapPanel({
  airgap,
  helm,
}: {
  airgap: AirgapContract
  helm: HelmChartContract
}) {
  const { t } = useTranslation('attestation')
  return (
    <div className="flex flex-col gap-5">
      <div className="flex flex-col gap-2">
        <div className="flex flex-wrap items-center gap-2">
          <p className="text-sm font-medium text-foreground">
            {t('airgap.bundleTitle')}
          </p>
          <VerifyStatusBadge status={airgap.status} />
        </div>
        <p className="text-xs text-muted-foreground">
          {t('airgap.bundleIntro')}
        </p>
        <ul className="flex list-disc flex-col gap-1 pl-5 text-xs text-muted-foreground">
          {airgap.composition.map((c) => (
            <li key={c}>{c}</li>
          ))}
        </ul>
        <dl>
          <Row label={t('airgap.buildCommand')} mono>
            {airgap.build_command}
          </Row>
          <Row label={t('airgap.mirrorCommand')} mono>
            {airgap.mirror_command}
          </Row>
        </dl>
        {/* El texto lo trae el dato declarado, no este componente. Acotado el 2026-08-18: la
            versión anterior hacía una promesa ABSOLUTA que contradice una decisión firmada — el
            contacto con el fabricante está aprobado y `olivares upgrade` sí nos alcanza. Lo que
            dice ahora es lo cierto: no hay llamadas obligatorias AL ARRANQUE, y el camino de
            verificación por clave no usa Rekor ni Fulcio.

            ⚠ Y esta nota NO repite la frase retirada, a propósito: `lint:phone-home-claims`
            busca el literal y no distingue una CITA de una afirmación, así que explicar el
            arreglo con sus palabras volvía a contar el fichero como infractor. Lo comprobé
            porque me pasó al escribir este mismo comentario. */}
        <CaveatNotice tone="info">{airgap.no_phone_home}</CaveatNotice>
      </div>

      <div className="flex flex-col gap-2">
        <div className="flex flex-wrap items-center gap-2">
          <p className="text-sm font-medium text-foreground">
            {t('airgap.chartTitle')}
          </p>
          <VerifyStatusBadge status={helm.status} />
          <span className="font-mono text-xs text-muted-foreground">
            {helm.scp}
          </span>
        </div>
        <dl>
          <Row label={t('airgap.ociCoordinate')} mono>
            {helm.oci_coordinate}
          </Row>
          <Row label={t('airgap.cosignManifest')}>{helm.cosign_manifest}</Row>
          <Row label={t('airgap.gpgProv')}>{helm.gpg_prov}</Row>
          <Row label={t('airgap.verifyCommand')} mono>
            {helm.verify_command}
          </Row>
        </dl>
      </div>
    </div>
  )
}

// --- MEASURED: the running binary (live) --------------------------------

/**
 * BinaryPanel — the measured half: facts the running process proved about itself
 * (ldflags, ReadBuildInfo, fips140, self-hash). Every value renders VERBATIM —
 * "dev"/"none"/"unknown" defaults included, and a "-dirty" version suffix is shown
 * as-is (the only dirty signal; no boolean clean/dirty claim is invented). The
 * self-hash is a fingerprint for OFFLINE comparison — never verified here.
 */
export function BinaryPanel({
  binary,
  capturedAt,
}: {
  binary: BinaryAttestation
  /** RFC3339 instant the measurement was taken. */
  capturedAt: string
}) {
  const { t, i18n } = useTranslation('attestation')
  return (
    <div className="flex flex-col gap-3">
      <div className="flex flex-wrap items-center gap-2">
        <Badge variant="neutral" className="font-mono">
          {binary.version}
        </Badge>
        {/* `measured` = read from the running process, not from a contract. */}
        <Badge variant="info">
          {t(`live.status.${binary.status}`, { defaultValue: binary.status })}
        </Badge>
        {binary.fips140.enabled ? (
          <Badge variant="success">
            {t('live.fipsOn')}
            {binary.fips140.version ? (
              <span className="font-mono">{binary.fips140.version}</span>
            ) : null}
          </Badge>
        ) : (
          <Badge variant="neutral">{t('live.fipsOff')}</Badge>
        )}
      </div>
      <dl>
        <Row label={t('live.fields.commit')} mono>
          {binary.commit}
        </Row>
        <Row label={t('live.fields.buildDate')} mono>
          {binary.build_date}
        </Row>
        <Row label={t('live.fields.goVersion')} mono>
          {binary.go_version}
        </Row>
        <Row label={t('live.fields.platform')} mono>
          {binary.os}/{binary.arch}
        </Row>
        <Row label={t('live.fields.mainModule')} mono>
          {binary.main_module.path} {binary.main_module.version}
        </Row>
        <Row label={t('live.fields.moduleSums')}>
          {binary.module_sums.sums_present
            ? t('live.sums', { n: binary.module_sums.external_deps })
            : t('live.sumsAbsent')}
          {' — '}
          {/* The go.work caveat, verbatim from the backend. */}
          {binary.module_sums.note}
        </Row>
        <Row label={t('live.fields.vcsStamp')}>
          {binary.vcs_stamp.available ? (
            t('live.vcsAvailable')
          ) : (
            <>
              <Badge variant="neutral">{t('live.vcsUnavailable')}</Badge>{' '}
              <span className="text-muted-foreground">
                {binary.vcs_stamp.reason}
              </span>
            </>
          )}
        </Row>
        <Row label={t('live.fields.selfHash')}>
          {binary.self_sha256 ? (
            <HashChip hash={binary.self_sha256} />
          ) : (
            <span className="text-muted-foreground">
              {binary.self_hash_note ?? '—'}
            </span>
          )}
        </Row>
        <Row label={t('live.fields.capturedAt')}>
          {formatDateTime(capturedAt, i18n.language)}
        </Row>
      </dl>
      <CaveatNotice tone="info">{t('live.selfHashHint')}</CaveatNotice>
    </div>
  )
}

/**
 * ReleaseStatePanel — the honest release/pipeline state of THIS binary: nothing is
 * published or verified until a v* tag fires the pipeline. The "not published" /
 * "not verified" badges are explicit states, never collapsed into an error; the
 * reasons render verbatim from the backend.
 */
export function ReleaseStatePanel({
  release,
  pipeline,
}: {
  release: ReleaseState
  pipeline: PipelineState
}) {
  const { t } = useTranslation('attestation')
  return (
    <div className="flex flex-col gap-3">
      <div className="flex flex-wrap items-center gap-2">
        {/* NOT `success`. A green chip is this design system's "verified" colour,
            and the positive state here is two link-time values the builder chose —
            reachable, measured 2026-08-14, by `go build -ldflags` alone. `info`
            keeps it a stated fact rather than a passed check. */}
        <Badge variant={release.published ? 'info' : 'warning'}>
          {release.published ? t('live.published') : t('live.notPublished')}
        </Badge>
        {/* `unavailable` (calm neutral, not a red failure): there is no signature
            to verify, and the browser never re-verifies one anyway. */}
        <IntegrityBadge unavailable reason={release.signature_reason} />
      </div>
      {/* The qualifier travels WITH the badge, in both polarities, straight from the
          backend — the same untranslated channel as signature_reason and
          pipeline.note. It is what stops the chip above from reading as proof that a
          release was published, and it needs no locale string to do it. */}
      <CaveatNotice tone="neutral">{release.provenance.note}</CaveatNotice>
      <dl>
        <Row label={t('live.fields.releaseReason')}>{release.reason}</Row>
        <Row label={t('live.fields.signature')}>
          <span className="font-mono">{release.signature_status}</span>
          {' — '}
          {release.signature_reason}
        </Row>
        <Row label={t('live.fields.verifier')}>
          {release.verifier_available ? (
            <Badge variant="success">{t('live.verifierAvailable')}</Badge>
          ) : (
            <Badge variant="neutral">{t('live.verifierUnavailable')}</Badge>
          )}
        </Row>
        <Row label={t('live.fields.transparencyLog')}>
          {/* Never a fabricated Rekor-inclusion claim — the note says why. */}
          {release.transparency_log.note}
        </Row>
        <Row label={t('live.fields.workflows')} mono>
          {pipeline.workflows.join(' · ')}
        </Row>
      </dl>
      <CaveatNotice tone="warning">
        <span className="font-semibold text-warning">
          {t(`live.pipelineStatus.${pipeline.status}`, {
            defaultValue: pipeline.status,
          })}
        </span>{' '}
        {pipeline.note}
      </CaveatNotice>
    </div>
  )
}
