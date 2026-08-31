// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { useQuery } from '@tanstack/react-query'
import { GitBranch, Plus } from 'lucide-react'
import { useState } from 'react'
import { useTranslation } from 'react-i18next'
import { Badge } from '@/components/ui/badge'
import { StepUpRequiredState } from '@/components/layout/step-up-state'
import { Button } from '@/components/ui/button'
import {
  Card,
  CardContent,
  CardDescription,
  CardHeader,
  CardTitle,
} from '@/components/ui/card'
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from '@/components/ui/dialog'
import { EmptyState } from '@/components/ui/empty-state'
import { Field } from '@/components/ui/field'
import { Input } from '@/components/ui/input'
import { Skeleton } from '@/components/ui/skeleton'
import { Textarea } from '@/components/ui/textarea'
import { useAuth } from '@/lib/auth/context'
import { ApiError } from '@/lib/api/errors'
import { formatDateTime } from '@/lib/format'
import { usePrivilegedMutation } from '@/lib/hooks/use-privileged-mutation'
import { workflowsApi, workflowsKeys } from './api'
import { WorkflowEditor } from './editor'
import type { CreateWorkflowInput, WorkflowDetail } from './types'
import './i18n'

export function WorkflowsTab() {
  const { t, i18n } = useTranslation('automations-workflows')
  const { activeTenant, can } = useAuth()
  const [selectedId, setSelectedId] = useState<string | null>(null)
  const [createOpen, setCreateOpen] = useState(false)
  const canRead = can('orchestration:workflow:read')
  const canWrite = can('orchestration:workflow:write')
  const canAdmin = can('orchestration:workflow:admin')

  const workflows = useQuery({
    queryKey: workflowsKeys.list(activeTenant),
    queryFn: () => workflowsApi.list(),
    enabled: canRead,
  })

  if (selectedId) {
    return (
      <WorkflowEditor
        workflowId={selectedId}
        canWrite={canWrite}
        canAdmin={canAdmin}
        onBack={() => setSelectedId(null)}
      />
    )
  }

  // ⛔ LA CAPACIDAD CONSERVA SU PRECEDENCIA, y por eso este booleano se parte en dos en vez de
  // reordenarse. `!canRead` es un booleano del CLIENTE: si el operador no tiene el permiso, la
  // ceremonia no le desbloquea nada y ofrecérsela sería mandarle a dar vueltas. Sólo cuando
  // `canRead` es cierto tiene sentido preguntar si lo que falta es ASEGURAMIENTO.
  //
  // Y el `!necesitaCeremonia` de abajo no es cosmético: `isForbidden` es SÓLO el status 403
  // (lib/api/errors.ts:59-61), así que sin él un `step_up_required` seguiría entrando en
  // `forbidden` y la pantalla mostraría la acusación ADEMÁS de la ceremonia.
  //
  // Defensa en profundidad: esta ruta no está en ninguna de las cuatro familias de emisores
  // medidas (21 `requireAAL3` en core/api, dos escrituras en governance, el `requireStepUp` de
  // deploy y los retornos de core/auth). Se arregla porque el defecto es de FORMA.
  const necesitaCeremonia =
    canRead &&
    workflows.error instanceof ApiError &&
    workflows.error.isStepUpRequired
  const forbidden =
    !canRead ||
    (!necesitaCeremonia &&
      workflows.error instanceof ApiError &&
      workflows.error.isForbidden)

  return (
    <>
      <Card>
        <CardHeader className="flex-row items-start justify-between gap-4">
          <div>
            <CardTitle>{t('list.title')}</CardTitle>
            <CardDescription>{t('list.description')}</CardDescription>
          </div>
          {canWrite ? (
            <Button
              variant="primary"
              size="sm"
              onClick={() => setCreateOpen(true)}
            >
              <Plus aria-hidden />
              {t('list.new')}
            </Button>
          ) : null}
        </CardHeader>
        <CardContent>
          {necesitaCeremonia ? (
            <StepUpRequiredState
              action="generic"
              onElevated={() => void workflows.refetch()}
            />
          ) : forbidden ? (
            <p className="rounded-md border border-border bg-muted p-3 text-sm text-muted-foreground">
              {t('list.forbidden')}
            </p>
          ) : workflows.isPending ? (
            <Skeleton className="h-40 w-full" />
          ) : workflows.isError ? (
            <p role="alert" className="text-sm text-muted-foreground">
              {t('list.loadFailed')}
            </p>
          ) : (workflows.data?.items.length ?? 0) === 0 ? (
            <EmptyState
              icon={<GitBranch />}
              title={t('list.empty')}
              description={t('list.emptyHint')}
              action={
                canWrite ? (
                  <Button
                    variant="primary"
                    size="sm"
                    onClick={() => setCreateOpen(true)}
                  >
                    <Plus aria-hidden />
                    {t('list.new')}
                  </Button>
                ) : undefined
              }
            />
          ) : (
            <div className="overflow-x-auto">
              <table className="w-full text-sm">
                <thead>
                  <tr className="border-b text-left text-muted-foreground">
                    <th scope="col" className="py-2 pr-4 font-medium">
                      {t('list.name')}
                    </th>
                    <th scope="col" className="py-2 pr-4 font-medium">
                      {t('list.enabled')}
                    </th>
                    <th scope="col" className="py-2 pr-4 font-medium">
                      {t('list.steps')}
                    </th>
                    <th scope="col" className="py-2 pr-4 font-medium">
                      {t('list.version')}
                    </th>
                    <th scope="col" className="py-2 font-medium">
                      {t('list.updated')}
                    </th>
                  </tr>
                </thead>
                <tbody>
                  {workflows.data?.items.map((workflow) => (
                    <tr key={workflow.id} className="border-b last:border-0">
                      <td className="py-2 pr-4">
                        <Button
                          variant="link"
                          className="max-w-72 justify-start truncate text-left"
                          aria-label={t('list.open', { name: workflow.name })}
                          onClick={() => setSelectedId(workflow.id)}
                        >
                          {workflow.name}
                        </Button>
                      </td>
                      <td className="py-2 pr-4">
                        <Badge
                          variant={workflow.enabled ? 'success' : 'neutral'}
                        >
                          {workflow.enabled
                            ? t('list.enabledValue')
                            : t('list.disabledValue')}
                        </Badge>
                      </td>
                      <td className="py-2 pr-4 font-mono tabular-nums">
                        {workflow.step_count}
                      </td>
                      <td className="py-2 pr-4 font-mono tabular-nums">
                        {workflow.version}
                      </td>
                      <td className="py-2 text-muted-foreground">
                        {workflow.updated_at
                          ? formatDateTime(workflow.updated_at, i18n.language)
                          : t('list.neverUpdated')}
                      </td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
          )}
        </CardContent>
      </Card>

      {canWrite ? (
        <CreateWorkflowDialog
          open={createOpen}
          onOpenChange={setCreateOpen}
          onCreated={(workflow) => setSelectedId(workflow.id)}
        />
      ) : null}
    </>
  )
}

function CreateWorkflowDialog({
  open,
  onOpenChange,
  onCreated,
}: {
  open: boolean
  onOpenChange: (open: boolean) => void
  onCreated: (workflow: WorkflowDetail) => void
}) {
  const { t } = useTranslation(['automations-workflows', 'common'])
  const { activeTenant } = useAuth()
  const [name, setName] = useState('')
  const [description, setDescription] = useState('')
  const [submitted, setSubmitted] = useState(false)

  const create = usePrivilegedMutation<CreateWorkflowInput, WorkflowDetail>({
    mutationFn: workflowsApi.create,
    invalidateKeys: [workflowsKeys.list(activeTenant)],
    successMessage: t('automations-workflows:create.success'),
    onDone: (workflow) => {
      onOpenChange(false)
      setName('')
      setDescription('')
      setSubmitted(false)
      onCreated(workflow)
    },
  })

  function submit() {
    setSubmitted(true)
    const trimmedName = name.trim()
    if (!trimmedName) return
    create.mutate({
      name: trimmedName,
      description: description.trim() || undefined,
    })
  }

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>{t('automations-workflows:create.title')}</DialogTitle>
          <DialogDescription>
            {t('automations-workflows:create.description')}
          </DialogDescription>
        </DialogHeader>
        <Field
          label={t('automations-workflows:create.name')}
          required
          error={
            submitted && !name.trim()
              ? t('automations-workflows:create.nameRequired')
              : undefined
          }
        >
          <Input
            value={name}
            onChange={(event) => setName(event.target.value)}
          />
        </Field>
        <Field label={t('automations-workflows:create.details')}>
          <Textarea
            value={description}
            onChange={(event) => setDescription(event.target.value)}
          />
        </Field>
        <DialogFooter>
          <Button variant="ghost" onClick={() => onOpenChange(false)}>
            {t('common:actions.cancel')}
          </Button>
          <Button
            variant="primary"
            disabled={create.isPending}
            onClick={submit}
          >
            {t('automations-workflows:create.submit')}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}
