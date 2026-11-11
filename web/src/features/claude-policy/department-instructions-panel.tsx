// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// DepartmentInstructionsPanel — the `managed-instructions@<department>` surface (Business).
// A department's instructions file is not typed here: the engine renders it from what the
// department carries, the very text its sessions are given, so this panel previews that
// document and publishes it. A department that lost all its standards retracts its file the
// same way (the engine answers; nothing is decided here).
import { useMutation, useQuery } from '@tanstack/react-query'
import { FlaskConical, Send } from 'lucide-react'
import { useState } from 'react'
import { useTranslation } from 'react-i18next'
import { Button } from '@/components/ui/button'
import { Field } from '@/components/ui/field'
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@/components/ui/select'
import { Spinner } from '@/components/ui/spinner'
import { toast } from '@/components/ui/toaster'
import { SectionCard } from '@/features/_intel'
import { consoleApi, consoleKeys } from '@/features/console/api'
import { isOpenCoreSeam } from '@/lib/api/errors'
import { useAuth } from '@/lib/auth/context'
import {
  failureText,
  reportEditionRefusal,
  usePrivilegedMutation,
} from '@/lib/hooks/use-privileged-mutation'
import { claudePolicyApi, claudePolicyKeys } from './api'
import { BusinessRequiredNotice } from './components'
import type { DepartmentSurface, DryRunResult, PublishResult } from './types'

export function DepartmentInstructionsPanel() {
  const { t } = useTranslation(['claudePolicy', 'common'])
  const { activeTenant } = useAuth()
  const [department, setDepartment] = useState('')
  const [preview, setPreview] = useState<DryRunResult | null>(null)
  const surface: DepartmentSurface = `managed-instructions@${department}`

  const workspaces = useQuery({
    queryKey: consoleKeys.workspaces(activeTenant),
    queryFn: () => consoleApi.listWorkspaces(),
  })
  const versions = useQuery({
    queryKey: claudePolicyKeys.versions(activeTenant, surface),
    queryFn: () => claudePolicyApi.listVersions(surface),
    enabled: department !== '',
  })

  // An empty document asks the engine to render the department's own.
  const previewMutation = useMutation({
    mutationFn: () => claudePolicyApi.dryRun(surface, ''),
    onSuccess: setPreview,
    onError: (e) => {
      setPreview(null)
      // A 501 is said in place, below; another edition refusal as the edition line.
      if (isOpenCoreSeam(e) || reportEditionRefusal(e)) return
      toast.error(t('claudePolicy:department.previewFailed'), {
        description: failureText(e),
      })
    },
  })
  const publishMutation = usePrivilegedMutation<void, PublishResult>({
    mutationFn: () => claudePolicyApi.publish(surface, ''),
    invalidateKeys: () => [
      claudePolicyKeys.versions(activeTenant, surface),
      claudePolicyKeys.distribution(activeTenant, surface),
    ],
    successMessage: t('claudePolicy:department.published'),
    onError: isOpenCoreSeam,
  })
  // Community answers 501 for this surface: say it is Business, not "0 revisions".
  const editionError = [
    versions.error,
    previewMutation.error,
    publishMutation.error,
  ].find(isOpenCoreSeam)
  const editionGated = editionError !== undefined

  const departments = (workspaces.data?.items ?? []).filter(
    (ws) => ws.status === 'active',
  )
  const published = versions.data?.items.length ?? 0

  return (
    <SectionCard
      title={t('claudePolicy:department.title')}
      description={t('claudePolicy:department.hint')}
    >
      <div className="flex flex-col gap-4">
        <Field
          label={t('claudePolicy:department.select')}
          htmlFor="department-instructions"
        >
          <Select
            value={department}
            onValueChange={(v) => {
              setDepartment(v)
              setPreview(null)
            }}
          >
            <SelectTrigger id="department-instructions">
              <SelectValue
                placeholder={t('claudePolicy:department.selectPlaceholder')}
              />
            </SelectTrigger>
            <SelectContent>
              {departments.map((ws) => (
                <SelectItem key={ws.id} value={ws.slug}>
                  {ws.name}
                </SelectItem>
              ))}
            </SelectContent>
          </Select>
        </Field>

        {department !== '' && editionGated && (
          <BusinessRequiredNotice error={editionError} />
        )}
        {department !== '' && !editionGated && (
          <>
            <div className="flex flex-wrap items-center gap-2">
              <Button
                variant="secondary"
                onClick={() => previewMutation.mutate()}
                disabled={previewMutation.isPending}
              >
                {previewMutation.isPending ? (
                  <Spinner size="sm" aria-hidden />
                ) : (
                  <FlaskConical />
                )}
                {t('claudePolicy:department.preview')}
              </Button>
              <Button
                variant="primary"
                onClick={() => publishMutation.mutate()}
                disabled={publishMutation.isPending}
              >
                {publishMutation.isPending ? (
                  <Spinner size="sm" aria-hidden />
                ) : (
                  <Send />
                )}
                {t('claudePolicy:department.publish')}
              </Button>
              <span className="text-caption text-muted-foreground">
                {t('claudePolicy:department.revisions', { count: published })}
              </span>
            </div>

            {preview?.content !== undefined && (
              <div className="flex flex-col gap-1">
                <pre
                  aria-label={t('claudePolicy:department.fileLabel')}
                  className="max-h-96 overflow-auto rounded-md border border-border bg-surface p-3 text-caption whitespace-pre-wrap"
                >
                  {preview.content}
                </pre>
                <span className="font-mono text-caption text-muted-foreground">
                  {t('claudePolicy:department.digest', {
                    sha: preview.sha256,
                  })}
                </span>
              </div>
            )}
            {preview?.notes?.map((note) => (
              <p key={note} className="text-caption text-muted-foreground">
                {note}
              </p>
            ))}
          </>
        )}
      </div>
    </SectionCard>
  )
}
