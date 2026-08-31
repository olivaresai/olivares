// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// Red-case battery for check-i18n-namespaces.mjs. A gate is only worth its runtime if
// it has been SEEN to fail: this builds throwaway module trees under TMPDIR and asserts
// the scan reports exactly the shapes it is supposed to — and stays silent on the ones
// it is not. Run it with `node scripts/check-i18n-namespaces.mjs --self-test`
// (task lint:i18n-namespaces-selftest); it touches nothing outside its temp dir.
//
// The cases below are the real ones, reduced: the leaf component rendered by a foreign
// lazy chunk (the first-boot wizard's step-up panel), the deep import that bypasses the
// barrel holding the registration (_intel/notices), the shared component translating out
// of a feature's namespace (sigma-graph → accessMap), and the namespace that does not
// exist at all (finops's phantom `periods`).

import fs from 'node:fs'
import os from 'node:os'
import path from 'node:path'

function tree(files) {
  const dir = fs.mkdtempSync(
    path.join(process.env.TMPDIR || os.tmpdir(), 'i18n-ns-selftest-'),
  )
  for (const [name, body] of Object.entries(files)) {
    const full = path.join(dir, name)
    fs.mkdirSync(path.dirname(full), { recursive: true })
    fs.writeFileSync(full, body)
  }
  return dir
}

/** `<file>:<namespace>` for every problem, sorted — the shape assertions compare this. */
function pairs(result) {
  return [...new Set(result.problems.map((p) => `${p.file}:${p.namespace}`))].sort()
}

const REGISTER = "import { registerTranslations } from '@/lib/i18n'\nregisterTranslations('feature', {})\n"

const CASES = [
  {
    expect: ['src/feature/panel.tsx:feature'],
    files: {
      'src/main.tsx': "import './app'\n",
      'src/app.tsx': "const V = lazy(() => import('./wizard/view'))\n",
      'src/wizard/view.tsx': "import { Panel } from '@/feature/panel'\n",
      'src/feature/panel.tsx': "const { t } = useTranslation('feature')\n",
      'src/feature/i18n/index.ts': REGISTER,
    },
    name: 'a leaf rendered by a FOREIGN lazy chunk whose graph never registers it',
  },
  {
    expect: [],
    files: {
      'src/main.tsx': "import './app'\n",
      'src/app.tsx': "const V = lazy(() => import('./wizard/view'))\n",
      'src/wizard/view.tsx': "import { Panel } from '@/feature/panel'\n",
      // The fix under test: the module that translates registers its own namespace.
      'src/feature/panel.tsx':
        "import './i18n'\nconst { t } = useTranslation('feature')\n",
      'src/feature/i18n/index.ts': REGISTER,
    },
    name: 'the same tree, once the translating module registers its own namespace',
  },
  {
    expect: ['src/feature/notices.tsx:feature'],
    files: {
      'src/main.tsx': "import './app'\n",
      'src/app.tsx': "const V = lazy(() => import('./other/view'))\n",
      // The deep import bypasses the barrel that holds registerTranslations.
      'src/other/view.tsx': "import { Notice } from '@/feature/notices'\n",
      'src/feature/index.ts': REGISTER + "export { Notice } from './notices'\n",
      'src/feature/notices.tsx': "const { t } = useTranslation('feature')\n",
    },
    name: 'a DEEP import that bypasses the barrel carrying the registration',
  },
  {
    expect: ['src/shared/graph.tsx:feature'],
    files: {
      'src/main.tsx': "import { Graph } from './shared/graph'\n",
      // A shared component translating out of a feature namespace: broken EVERYWHERE
      // except inside that feature, so the root chunk itself is a problem.
      'src/shared/graph.tsx': "const { t } = useTranslation('feature')\n",
      'src/feature/i18n/index.ts': REGISTER,
    },
    name: 'a SHARED component translating out of a feature namespace',
  },
  {
    expect: ['src/feature/view.tsx:periods'],
    files: {
      'src/main.tsx': "import './feature/view'\n",
      'src/feature/view.tsx':
        "import './i18n'\nconst { t } = useTranslation(['feature', 'periods'])\n",
      'src/feature/i18n/index.ts': REGISTER,
    },
    name: 'a namespace nobody registers anywhere (a phantom in the useTranslation list)',
  },
  {
    expect: [],
    files: {
      // Foundation namespaces come with lib/i18n's own init — never a finding.
      'src/main.tsx':
        "const { t } = useTranslation(['common', 'nav', 'errors'])\nconst x = t('settings:a.b')\n",
    },
    name: 'the foundation namespaces, which lib/i18n registers at init',
  },
  {
    expect: [],
    files: {
      // A namespace named only inside a comment or a template literal is not a use:
      // the scanner must read code, not prose.
      'src/main.tsx':
        "// useTranslation('ghost') in a comment\nconst s = `useTranslation('spectre')`\n",
    },
    name: 'a namespace named only in a comment or a template literal',
  },
]

export function selfTest(scan) {
  let failures = 0
  for (const testCase of CASES) {
    const root = tree(testCase.files)
    let got
    try {
      got = pairs(
        scan({
          entry: path.join(root, 'src', 'main.tsx'),
          root,
          src: path.join(root, 'src'),
        }),
      )
    } finally {
      fs.rmSync(root, { force: true, recursive: true })
    }
    const want = [...testCase.expect].sort()
    const ok = JSON.stringify(got) === JSON.stringify(want)
    if (!ok) failures++
    console.log(`  ${ok ? '✓' : '✗'} ${testCase.name}`)
    if (!ok) {
      console.error(`      want: ${JSON.stringify(want)}`)
      console.error(`      got:  ${JSON.stringify(got)}`)
    }
  }
  if (failures) {
    console.error(
      `✗ i18n namespace gate SELF-TEST FAILED — ${failures} of ${CASES.length} case(s)`,
    )
    return false
  }
  console.log(
    `✓ i18n namespace gate self-test OK — ${CASES.length} case(s), ` +
      `${CASES.filter((c) => c.expect.length).length} of them RED by construction`,
  )
  return true
}
