// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// Model evidence tab — the cross-model AIBOM SEAL ledger (GET /aiboms). A seal is the
// durable, append-only, ledger-anchored tamper-evidence for an owned model's AIBOM (the
// live generate in the Owned-models drawer is NOT evidence until sealed). This tab is a
// read-only inventory: seals created from the drawer land here. `content_hash` is the
// sha256 of the CANONICAL BOM (serialNumber + timestamp excluded); `ledger_seq`/
// `ledger_hash` anchor the audit-chain head OBSERVED BEFORE the seal was stored.
//
// ⛔ Y NO MÁS QUE ESO: decir «el precinto es el SIGUIENTE evento de la cadena» sería afirmar
// de más. `handleSealAIBOM` lee `Audit().Head()` antes de llamar a `Append`, y el cerrojo por
// tenant se toma DENTRO de `Append`: dos precintos concurrentes pueden observar la misma
// cabeza y serializarse luego como N+1 y N+2. Lo devolvió el contraste externo. Lo que el
// dato sostiene es «anclado a la cabeza observada», que ya es la propiedad útil.
//
// Nothing here is editable or deletable (append-only by design).
import { useMemo, useState } from 'react'
import { useQuery } from '@tanstack/react-query'
import { useTranslation } from 'react-i18next'
import { DataTable, type TableColumn } from '@/components/data/data-table'
import { KvList, KvRow } from '@/components/ui/kv'
import { ScrollArea } from '@/components/ui/scroll-area'
import { EmptyState } from '@/components/ui/empty-state'
import {
  Sheet,
  SheetContent,
  SheetDescription,
  SheetHeader,
  SheetTitle,
} from '@/components/ui/sheet'
import { ListTruncationBadge, HashChip, SectionCard } from '@/features/_intel'
import { RelTimeLabel } from '@/features/shared'
import { useAuth } from '@/lib/auth/context'
import { EVIDENCE_PAGE, modelsApi, modelsKeys } from '@/features/models/api'
import type { AibomSeal } from '@/features/models/types'

export function ModelEvidenceTab() {
  const { t } = useTranslation(['model-ops', 'common'])
  const { activeTenant } = useAuth()
  const [selected, setSelected] = useState<AibomSeal | null>(null)

  // ⛔ EL TECHO SE PIDE Y EL RECORTE SE DICE. Este ledger es APPEND-ONLY —`handleSealAIBOM`
  //    hace `repo.Create` en cada precinto— y sin `limit` el repositorio genérico pagina a 100
  //    por `id ASC`, que en UUIDv7 es orden de creación. O sea que la pestaña enseñaba los CIEN
  //    PRIMEROS precintos del tenant y callaba: en un ledger de evidencia, lo que se pierde por
  //    ese lado es lo RECIENTE, que es justo lo que un auditor viene a ver.
  const params = { limit: EVIDENCE_PAGE }
  const query = useQuery({
    queryKey: modelsKeys.aibomSeals(activeTenant, undefined, params),
    queryFn: () => modelsApi.aibomSeals(params),
  })

  const columns = useMemo<TableColumn<AibomSeal>[]>(
    () => [
      {
        accessorKey: 'owned_ref',
        header: t('evidenceLedger.columns.ownedRef'),
        cell: ({ row }) => (
          <span className="font-mono text-xs">{row.original.owned_ref}</span>
        ),
      },
      {
        accessorKey: 'content_hash',
        header: t('evidenceLedger.columns.contentHash'),
        cell: ({ row }) => (
          <HashChip hash={`${row.original.content_hash}`} head={12} tail={10} />
        ),
      },
      {
        accessorKey: 'component_count',
        header: t('evidenceLedger.columns.components'),
        cell: ({ row }) => (
          <span className="tabular-nums">{row.original.component_count}</span>
        ),
      },
      {
        accessorKey: 'spec_version',
        header: t('evidenceLedger.columns.spec'),
        cell: ({ row }) => (
          <span className="font-mono text-xs text-muted-foreground">
            CycloneDX {row.original.spec_version}
          </span>
        ),
      },
      {
        accessorKey: 'ledger_seq',
        header: t('evidenceLedger.columns.ledgerSeq'),
        cell: ({ row }) => (
          <span className="tabular-nums text-muted-foreground">
            {row.original.ledger_seq}
          </span>
        ),
      },
      {
        accessorKey: 'generated_at',
        header: t('evidenceLedger.columns.sealed'),
        cell: ({ row }) =>
          row.original.generated_at ? (
            <RelTimeLabel ts={row.original.generated_at} />
          ) : (
            <span className="text-muted-foreground">—</span>
          ),
      },
    ],
    [t],
  )

  return (
    <SectionCard
      title={t('evidenceLedger.title')}
      description={t('evidenceLedger.description')}
      noPadding
    >
      {/* Fuera del bloque de datos a propósito: si un refetch falla, el aviso no debe quedarse
          flotando sobre una tabla que ya sólo enseña el error. */}
      <ListTruncationBadge
        query={query}
        label={t('evidenceLedger.truncated', {
          n: query.data?.items?.length ?? 0,
        })}
        hint={t('evidenceLedger.truncatedHint')}
        className="px-3 pt-3"
      />

      <DataTable<AibomSeal>
        label={t('evidenceLedger.title')}
        columns={columns}
        data={query.data?.items ?? []}
        isLoading={query.isLoading}
        error={query.error}
        onRetry={() => void query.refetch()}
        getRowId={(r) => r.id}
        onRowClick={(r) => setSelected(r)}
        searchable
        searchPlaceholder={t('evidenceLedger.search')}
        empty={
          <EmptyState
            title={t('empty.modelEvidence.title')}
            description={t('empty.modelEvidence.description')}
          />
        }
      />

      <SealDrawer seal={selected} onClose={() => setSelected(null)} />
    </SectionCard>
  )
}

/** Read-only detail for one seal — the full tamper-evidence record. Shared by the ledger
 *  tab and (later) the drawer's post-seal receipt. */
export function SealDetail({ seal }: { seal: AibomSeal }) {
  const { t } = useTranslation('model-ops')
  return (
    <div className="flex flex-col gap-3">
      {/* What a seal IS — a content-hash commitment, not the archived document. */}
      <p className="rounded-md border border-border bg-muted/50 px-3 py-2 text-[11px] text-muted-foreground">
        {t('evidenceLedger.sealMeaning')}
      </p>
      <KvList>
        <KvRow label={t('evidenceLedger.columns.ownedRef')} mono>
          {seal.owned_ref}
        </KvRow>
        <KvRow label={t('evidenceLedger.serialNumber')} align="start">
          <HashChip hash={seal.serial_number} head={16} tail={8} />
        </KvRow>
        <KvRow label={t('evidenceLedger.columns.contentHash')} align="start">
          <HashChip hash={seal.content_hash} head={16} tail={12} />
        </KvRow>
        <KvRow label={t('evidenceLedger.columns.spec')}>
          CycloneDX {seal.spec_version}
        </KvRow>
        <KvRow label={t('evidenceLedger.columns.components')}>
          {seal.component_count}
        </KvRow>
        <KvRow label={t('evidenceLedger.columns.ledgerSeq')}>
          {seal.ledger_seq === 0
            ? t('evidenceLedger.noPriorHead')
            : seal.ledger_seq}
        </KvRow>
        {seal.ledger_hash && (
          <KvRow label={t('evidenceLedger.ledgerHash')} align="start">
            <HashChip hash={seal.ledger_hash} head={16} tail={12} />
          </KvRow>
        )}
        {seal.generated_by && (
          <KvRow label={t('evidenceLedger.generatedBy')} mono>
            {seal.generated_by}
          </KvRow>
        )}
        {seal.generated_at && (
          <KvRow label={t('evidenceLedger.columns.sealed')}>
            <RelTimeLabel ts={seal.generated_at} />
          </KvRow>
        )}
        {seal.scope_note && (
          <KvRow label={t('evidenceLedger.scopeNote')} align="start">
            {seal.scope_note}
          </KvRow>
        )}
      </KvList>
    </div>
  )
}

function SealDrawer({
  seal,
  onClose,
}: {
  seal: AibomSeal | null
  onClose: () => void
}) {
  const { t } = useTranslation('model-ops')
  return (
    <Sheet open={!!seal} onOpenChange={(o) => !o && onClose()}>
      <SheetContent className="flex w-full flex-col gap-0 sm:max-w-lg">
        {seal && (
          <>
            <SheetHeader>
              <SheetTitle className="font-mono text-sm">
                {seal.owned_ref}
              </SheetTitle>
              <SheetDescription>
                {t('evidenceLedger.drawerSubtitle')}
              </SheetDescription>
            </SheetHeader>
            <ScrollArea className="flex-1">
              <div className="flex flex-col gap-4 px-1 py-3">
                <SealDetail seal={seal} />
              </div>
            </ScrollArea>
          </>
        )}
      </SheetContent>
    </Sheet>
  )
}
