// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
/**
 * InheritanceFiltersSection shows and sets inheritance filters: on a workspace, agent group
 * or folder, rights that reach one resource class from above the node stop applying there,
 * while grants at or below the node still do. The engine checks the node, the class and that
 * the actor is an admin of the node; the form only offers the classes the engine accepts.
 */
import { QueryErrorState } from '@/components/layout/query-error-state'
import { useQuery } from '@tanstack/react-query'
import { Filter, Plus, Trash2 } from 'lucide-react'
import { useState } from 'react'
import { useTranslation } from 'react-i18next'
import { StaticTable } from '@/components/data/static-table'
import { Button } from '@/components/ui/button'
import { Combobox } from '@/components/ui/combobox'
import { ConfirmDialog } from '@/components/ui/confirm-dialog'
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
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@/components/ui/select'
import { Spinner } from '@/components/ui/spinner'
import { AAL, RequireAssurance } from '@/features/identity/assurance'
import { useAuth } from '@/lib/auth/context'
import { usePrivilegedMutation } from '@/lib/hooks/use-privileged-mutation'
import {
  consoleApi,
  consoleKeys,
  type InheritanceFilterDTO,
  type RBACCatalogDTO,
} from './api'
import { ResourceTreePicker } from './resource-tree-picker'
import { classOptionsFor, FormError, scopeLabel } from './roles-shared'

type FilterTree = InheritanceFilterDTO['scope_tree']

export function InheritanceFiltersSection({
  catalog,
  workspaces,
  agentGroups,
  canAdmin,
}: {
  catalog?: RBACCatalogDTO
  workspaces: { slug: string; name: string }[]
  agentGroups: { slug: string; name: string }[]
  canAdmin: boolean
}) {
  const { t } = useTranslation(['console', 'common'])
  const { activeTenant } = useAuth()
  const [createOpen, setCreateOpen] = useState(false)
  const [remove, setRemove] = useState<InheritanceFilterDTO | null>(null)

  const filters = useQuery({
    queryKey: consoleKeys.inheritanceFilters(activeTenant),
    queryFn: () => consoleApi.listInheritanceFilters(),
  })

  const removeMutation = usePrivilegedMutation<string, void>({
    mutationFn: (id) => consoleApi.removeInheritanceFilter(id),
    invalidateKeys: () => [consoleKeys.inheritanceFilters(activeTenant)],
    successMessage: t('console:roles.filters.removed'),
    onDone: () => setRemove(null),
  })

  const items = filters.data?.items ?? []
  const createButton = canAdmin && (
    <Button onClick={() => setCreateOpen(true)}>
      <Plus />
      {t('console:roles.filters.create')}
    </Button>
  )

  return (
    <section className="flex flex-col gap-3">
      <div className="flex items-start justify-between gap-3">
        <div>
          <h2 className="text-heading text-foreground">
            {t('console:roles.filters.title')}
          </h2>
          <p className="max-w-2xl text-body text-muted-foreground">
            {t('console:roles.filters.caption')}
          </p>
        </div>
        {createButton}
      </div>
      {filters.isLoading ? (
        <div className="flex justify-center py-8">
          <Spinner />
        </div>
      ) : filters.isError ? (
        <QueryErrorState
          error={filters.error}
          retry={() => void filters.refetch()}
        />
      ) : items.length === 0 ? (
        <EmptyState
          action={createButton || undefined}
          description={t('console:roles.filters.noneHint')}
          title={t('console:roles.filters.none')}
          icon={<Filter />}
        />
      ) : (
        <div className="overflow-x-auto rounded-lg border border-border">
          <StaticTable>
            <thead>
              <tr>
                <th>{t('console:roles.filters.node')}</th>
                <th>{t('console:roles.filters.class')}</th>
                <th>{t('console:roles.filters.createdBy')}</th>
                <th>
                  <span className="sr-only">
                    {t('console:roles.filters.actions')}
                  </span>
                </th>
              </tr>
            </thead>
            <tbody>
              {items.map((f) => (
                <tr key={f.id} className="align-top">
                  <td className="text-foreground">
                    {scopeLabel(t, f.scope_tree, f.scope_ref)}
                  </td>
                  <td className="font-mono text-caption text-foreground">
                    {f.scope_class}
                  </td>
                  <td className="font-mono text-caption text-muted-foreground">
                    {f.created_by}
                  </td>
                  <td className="text-right">
                    {canAdmin && (
                      <Button
                        variant="ghost"
                        size="sm"
                        onClick={() => {
                          removeMutation.reset()
                          setRemove(f)
                        }}
                      >
                        <Trash2 />
                        {t('console:roles.filters.remove')}
                      </Button>
                    )}
                  </td>
                </tr>
              ))}
            </tbody>
          </StaticTable>
        </div>
      )}

      <Dialog open={createOpen} onOpenChange={setCreateOpen}>
        <DialogContent className="max-h-[85vh] max-w-lg overflow-y-auto">
          {createOpen && (
            <RequireAssurance minAal={AAL.HARDWARE} action="console">
              <FilterForm
                catalog={catalog}
                workspaces={workspaces}
                agentGroups={agentGroups}
                onClose={() => setCreateOpen(false)}
              />
            </RequireAssurance>
          )}
        </DialogContent>
      </Dialog>

      <ConfirmDialog
        open={remove !== null}
        onOpenChange={(o) => !o && setRemove(null)}
        title={t('console:roles.filters.removeTitle')}
        description={t('console:roles.filters.removeBody')}
        confirmLabel={t('console:roles.filters.remove')}
        tone="danger"
        pending={removeMutation.isPending}
        onConfirm={() => remove?.id && removeMutation.mutate(remove.id)}
      >
        <FormError error={removeMutation.error} />
      </ConfirmDialog>
    </section>
  )
}

function FilterForm({
  catalog,
  workspaces,
  agentGroups,
  onClose,
}: {
  catalog?: RBACCatalogDTO
  workspaces: { slug: string; name: string }[]
  agentGroups: { slug: string; name: string }[]
  onClose: () => void
}) {
  const { t } = useTranslation(['console', 'common'])
  const { activeTenant } = useAuth()
  const [tree, setTree] = useState<FilterTree>('workspace')
  const [ref, setRef] = useState<string | null>(null)
  const [scopeClass, setScopeClass] = useState<string | null>(null)

  const mutation = usePrivilegedMutation<void, InheritanceFilterDTO>({
    mutationFn: () =>
      consoleApi.createInheritanceFilter({
        scope_tree: tree,
        scope_ref: ref ?? '',
        scope_class: scopeClass ?? '',
      }),
    invalidateKeys: () => [consoleKeys.inheritanceFilters(activeTenant)],
    successMessage: t('console:roles.filters.created'),
    onDone: onClose,
  })

  // The engine takes a scope-tree kind only, and an agent group only `agent`.
  const classOptions = classOptionsFor(tree, catalog)
  const valid = !!ref && !!scopeClass

  return (
    <>
      <DialogHeader>
        <DialogTitle>{t('console:roles.filters.formTitle')}</DialogTitle>
        <DialogDescription>
          {t('console:roles.filters.caption')}
        </DialogDescription>
      </DialogHeader>

      <div className="flex flex-col gap-4">
        <Field label={t('console:roles.filters.node')} htmlFor="f-tree">
          <Select
            value={tree}
            onValueChange={(v) => {
              setTree(v as FilterTree)
              setRef(null)
              setScopeClass(null)
            }}
          >
            <SelectTrigger
              id="f-tree"
              aria-label={t('console:roles.filters.node')}
            >
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              <SelectItem value="workspace">
                {t('console:roles.grantForm.scopeWorkspace')}
              </SelectItem>
              <SelectItem value="agent_group">
                {t('console:roles.grantForm.scopeAgentGroup')}
              </SelectItem>
              <SelectItem value="folder">
                {t('console:roles.filters.folder')}
              </SelectItem>
            </SelectContent>
          </Select>
        </Field>

        {tree === 'workspace' && (
          <Field
            label={t('console:roles.grantForm.scopeRefWorkspace')}
            htmlFor="f-ws"
            required
          >
            <Combobox
              id="f-ws"
              options={workspaces.map((w) => ({
                value: w.slug,
                label: w.name,
                keywords: [w.slug],
              }))}
              value={ref}
              onChange={setRef}
              placeholder={t('console:roles.grantForm.scopeRefPlaceholder')}
            />
          </Field>
        )}
        {tree === 'agent_group' && (
          <Field
            label={t('console:roles.grantForm.scopeRefGroup')}
            htmlFor="f-ag"
            required
          >
            <Combobox
              id="f-ag"
              options={agentGroups.map((a) => ({
                value: a.slug,
                label: a.name,
                keywords: [a.slug],
              }))}
              value={ref}
              onChange={setRef}
              placeholder={t('console:roles.grantForm.scopeRefPlaceholder')}
            />
          </Field>
        )}
        {tree === 'folder' && (
          <Field label={t('console:roles.filters.folder')} required>
            <ResourceTreePicker
              value={ref ?? ''}
              onChange={(id) => setRef(id)}
            />
          </Field>
        )}

        <Field
          label={t('console:roles.filters.class')}
          htmlFor="f-class"
          description={t('console:roles.filters.classHint')}
          required
        >
          <Select value={scopeClass ?? ''} onValueChange={setScopeClass}>
            <SelectTrigger
              id="f-class"
              aria-label={t('console:roles.filters.class')}
            >
              <SelectValue
                placeholder={t('console:roles.grantForm.scopeRefPlaceholder')}
              />
            </SelectTrigger>
            <SelectContent>
              {classOptions.map((k) => (
                <SelectItem key={k} value={k}>
                  {k}
                </SelectItem>
              ))}
            </SelectContent>
          </Select>
        </Field>

        <FormError error={mutation.error} />
      </div>

      <DialogFooter>
        <Button
          variant="secondary"
          onClick={onClose}
          disabled={mutation.isPending}
        >
          {t('common:actions.cancel')}
        </Button>
        <Button
          variant="primary"
          onClick={() => mutation.mutate()}
          disabled={!valid || mutation.isPending}
        >
          {mutation.isPending && <Spinner size="sm" aria-hidden />}
          {t('console:roles.filters.submit')}
        </Button>
      </DialogFooter>
    </>
  )
}
