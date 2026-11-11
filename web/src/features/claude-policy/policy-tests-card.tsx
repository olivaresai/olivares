// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { Trash2 } from 'lucide-react'
import { useState } from 'react'
import { useTranslation } from 'react-i18next'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Field } from '@/components/ui/field'
import { Input } from '@/components/ui/input'
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@/components/ui/select'
import { SectionCard } from '@/features/_intel'
import type { PdpExampleRequest, PdpPolicyTest } from './types'

/**
 * PolicyTestsCard lists what every publish must keep true and turns the example request
 * on screen into a new test. The list changes here and is saved by the next publish, so
 * a test and the policy it checks are stored by one audited act.
 */
export function PolicyTestsCard({
  tests,
  example,
  onChange,
}: {
  tests: readonly PdpPolicyTest[]
  example: () => PdpExampleRequest
  onChange: (tests: PdpPolicyTest[]) => void
}) {
  const { t } = useTranslation('claudePolicy')
  const [name, setName] = useState('')
  const [expected, setExpected] = useState<PdpPolicyTest['expect']>('deny')
  const trimmed = name.trim()
  const canAdd = trimmed !== '' && !tests.some((test) => test.name === trimmed)

  return (
    <SectionCard
      title={t('pdp.policyTests.title')}
      description={t('pdp.policyTests.sub')}
    >
      <div className="flex flex-col gap-3">
        {tests.length === 0 ? (
          <p className="text-caption text-muted-foreground">
            {t('pdp.policyTests.empty')}
          </p>
        ) : (
          <ul className="flex flex-col gap-1">
            {tests.map((test) => (
              <li key={test.name} className="flex items-center gap-2">
                <span className="min-w-0 flex-1 truncate text-body">
                  {test.name}
                </span>
                <Badge variant={test.expect === 'deny' ? 'warning' : 'success'}>
                  {t(`pdp.policyTests.${test.expect}`)}
                </Badge>
                <Button
                  variant="ghost"
                  size="icon"
                  aria-label={t('pdp.policyTests.remove', { name: test.name })}
                  onClick={() =>
                    onChange(tests.filter((other) => other !== test))
                  }
                >
                  <Trash2 />
                </Button>
              </li>
            ))}
          </ul>
        )}
        <Field label={t('pdp.policyTests.name')} htmlFor="pdp-test-name">
          <Input
            id="pdp-test-name"
            value={name}
            maxLength={128}
            onChange={(e) => setName(e.target.value)}
          />
        </Field>
        <Field label={t('pdp.policyTests.expect')} htmlFor="pdp-test-expect">
          <Select
            value={expected}
            onValueChange={(v) => setExpected(v as PdpPolicyTest['expect'])}
          >
            <SelectTrigger id="pdp-test-expect">
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              <SelectItem value="deny">{t('pdp.policyTests.deny')}</SelectItem>
              <SelectItem value="allow">
                {t('pdp.policyTests.allow')}
              </SelectItem>
            </SelectContent>
          </Select>
        </Field>
        <Button
          variant="secondary"
          size="sm"
          disabled={!canAdd}
          onClick={() => {
            onChange([
              ...tests,
              { name: trimmed, request: example(), expect: expected },
            ])
            setName('')
          }}
        >
          {t('pdp.policyTests.add')}
        </Button>
      </div>
    </SectionCard>
  )
}
