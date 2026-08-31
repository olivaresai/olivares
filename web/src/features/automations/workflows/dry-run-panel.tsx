// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { useMutation } from '@tanstack/react-query'
import { Clipboard, FlaskConical } from 'lucide-react'
import { useState } from 'react'
import { useTranslation } from 'react-i18next'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { EmptyState } from '@/components/ui/empty-state'
import {
  Sheet,
  SheetContent,
  SheetDescription,
  SheetHeader,
  SheetTitle,
} from '@/components/ui/sheet'
import { Spinner } from '@/components/ui/spinner'
import { toast } from '@/components/ui/toaster'
import { workflowsApi } from './api'

export function DryRunPanel({ workflowId }: { workflowId: string }) {
  const { t } = useTranslation('automations-workflows')
  const [open, setOpen] = useState(false)
  const dryRun = useMutation({
    mutationFn: () => workflowsApi.dryRun(workflowId),
  })

  function generate() {
    setOpen(true)
    dryRun.mutate()
  }

  return (
    <>
      <Button variant="secondary" size="sm" onClick={generate}>
        <FlaskConical aria-hidden />
        {t('dryRun.button')}
      </Button>
      <Sheet open={open} onOpenChange={setOpen}>
        <SheetContent className="overflow-y-auto sm:max-w-xl">
          <SheetHeader>
            <SheetTitle>{t('dryRun.title')}</SheetTitle>
            <SheetDescription>{t('dryRun.description')}</SheetDescription>
          </SheetHeader>
          {dryRun.isPending ? (
            <div
              className="flex flex-1 items-center justify-center"
              role="status"
            >
              <Spinner />
              <span className="sr-only">{t('editor.loading')}</span>
            </div>
          ) : dryRun.isError ? (
            <div
              role="alert"
              className="rounded-md border border-danger-line bg-danger-soft p-3 text-sm text-danger"
            >
              {t('dryRun.failed')}
            </div>
          ) : dryRun.data ? (
            <div className="space-y-5">
              <section className="space-y-2">
                <h3 className="text-sm font-medium text-foreground">
                  {t('dryRun.planHash')}
                </h3>
                <div className="flex items-center gap-2 rounded-md border border-border bg-surface p-2">
                  <code className="min-w-0 flex-1 truncate font-mono text-xs">
                    {truncateHash(dryRun.data.plan_hash)}
                  </code>
                  <Button
                    size="icon-sm"
                    variant="ghost"
                    aria-label={t('dryRun.copyHash')}
                    onClick={() => {
                      void navigator.clipboard.writeText(dryRun.data.plan_hash)
                      toast.success(t('dryRun.copied'))
                    }}
                  >
                    <Clipboard aria-hidden />
                  </Button>
                </div>
              </section>
              <section className="space-y-2">
                <h3 className="text-sm font-medium text-foreground">
                  {t('dryRun.requires')}
                </h3>
                <div className="flex flex-wrap gap-1.5">
                  {dryRun.data.requires.map((requirement) => (
                    <Badge key={requirement} variant="info">
                      {requirement}
                    </Badge>
                  ))}
                </div>
              </section>
              {dryRun.data.steps.length === 0 ? (
                <EmptyState title={t('dryRun.empty')} />
              ) : (
                <ol className="space-y-3">
                  {dryRun.data.steps.map((step) => (
                    <li
                      key={step.ref}
                      className="rounded-lg border border-border bg-surface p-3"
                    >
                      <div className="flex flex-wrap items-center gap-2">
                        <Badge variant="accent">
                          {t('dryRun.order', { order: step.order })}
                        </Badge>
                        <code className="font-mono text-xs">{step.ref}</code>
                        <Badge variant="outline">
                          {t(`kind.${step.kind}`)}
                        </Badge>
                      </div>
                      <p className="mt-2 text-sm text-foreground">
                        {step.action}
                      </p>
                      <p className="mt-1 text-xs text-muted-foreground">
                        {step.depends_on.length > 0
                          ? t('dryRun.dependencies', {
                              value: step.depends_on.join(', '),
                            })
                          : t('dryRun.noDependencies')}
                      </p>
                      {step.requires.length > 0 ? (
                        <div className="mt-2 flex flex-wrap gap-1">
                          {step.requires.map((requirement) => (
                            <Badge key={requirement} variant="info">
                              {requirement}
                            </Badge>
                          ))}
                        </div>
                      ) : null}
                      {step.warning ? (
                        <p className="mt-2 rounded-md border border-warning-line bg-warning-soft p-2 text-xs text-warning">
                          {step.warning}
                        </p>
                      ) : null}
                    </li>
                  ))}
                </ol>
              )}
            </div>
          ) : null}
        </SheetContent>
      </Sheet>
    </>
  )
}

function truncateHash(hash: string): string {
  return hash.length > 24 ? `${hash.slice(0, 12)}…${hash.slice(-12)}` : hash
}
