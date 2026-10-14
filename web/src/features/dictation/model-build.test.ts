// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { createHash } from 'node:crypto'
import {
  cp,
  mkdtemp,
  mkdir,
  readFile,
  rename,
  rm,
  symlink,
  writeFile,
} from 'node:fs/promises'
import { tmpdir } from 'node:os'
import { join, resolve } from 'node:path'
import { gunzipSync, gzipSync } from 'node:zlib'
import { afterAll, afterEach, beforeAll, expect, it, vi } from 'vitest'
import { loadConfigFromFile, type Plugin } from 'vite'
import model from './whisper-model.json'

let fixture: string
let generate: (...args: unknown[]) => Promise<void>

beforeAll(async () => {
  fixture = await mkdtemp(join(tmpdir(), 'dictation-build-'))
  const web = resolve(__dirname, '../../..')
  const target = join(fixture, 'src/features/dictation')
  await mkdir(target, { recursive: true })
  await cp(join(web, 'vite.config.ts'), join(fixture, 'vite.config.ts'))
  await cp(join(web, 'package.json'), join(fixture, 'package.json'))
  await cp(
    join(web, 'src/features/dictation/whisper-model.json'),
    join(target, 'whisper-model.json'),
  )
  await cp(join(web, 'src/features/dictation/model'), join(target, 'model'), {
    recursive: true,
  })
  await symlink(join(web, 'node_modules'), join(fixture, 'node_modules'), 'dir')
  generate = await loadGenerator()
})

async function loadGenerator(): Promise<typeof generate> {
  const loaded = await loadConfigFromFile(
    { command: 'build', mode: 'production' },
    join(fixture, 'vite.config.ts'),
  )
  const plugin = (loaded!.config.plugins as Plugin[]).find(
    (entry) => entry.name === 'olivares-dictation-model',
  )!
  return plugin.generateBundle as typeof generate
}

afterAll(async () => {
  if (fixture) await rm(fixture, { recursive: true, force: true })
})

async function emitModel(generator = generate): Promise<Map<string, Buffer>> {
  const assets = new Map<string, Buffer>()
  await generator.call(
    {
      error(message: string): never {
        throw new Error(message)
      },
      emitFile(asset: { fileName: string; source: Buffer }): string {
        assets.set(asset.fileName, asset.source)
        return asset.fileName
      },
    },
    {},
    {},
    false,
  )
  return assets
}

afterEach(() => vi.unstubAllGlobals())

it('emits every pinned model byte with the network unavailable', async () => {
  const fetch = vi.fn(() => Promise.reject(new Error('network disabled')))
  vi.stubGlobal('fetch', fetch)
  const assets = await emitModel()
  expect(assets.size).toBe(Object.keys(model.files).length)
  for (const [file, pin] of Object.entries(model.files)) {
    const data = assets.get(`assets/voice/${model.revision}/${file}`)!
    expect(data.length).toBe(pin.size)
    expect(createHash('sha256').update(data).digest('hex')).toBe(pin.sha256)
  }
  expect(fetch).not.toHaveBeenCalled()
})

it.each(Object.entries(model.files))(
  'rejects tampered %s without fetching a replacement',
  async (file, pin) => {
    const input = 'gzipSha256' in pin ? `${file}.gz` : file
    const path = join(fixture, 'src/features/dictation/model', input)
    const original = await readFile(path)
    const corrupt = Buffer.from(original)
    corrupt[0] ^= 1
    const fetch = vi.fn(() => Promise.reject(new Error('network disabled')))
    vi.stubGlobal('fetch', fetch)
    try {
      await writeFile(path, corrupt)
      await expect(emitModel()).rejects.toThrow(
        `${input} does not match its pinned SHA-256`,
      )
      expect(fetch).not.toHaveBeenCalled()
    } finally {
      await writeFile(path, original)
    }
  },
)

it('names a missing input and fails without fetching it', async () => {
  const path = join(
    fixture,
    'src/features/dictation/model/preprocessor_config.json',
  )
  const fetch = vi.fn(() => Promise.reject(new Error('network disabled')))
  vi.stubGlobal('fetch', fetch)
  await rename(path, `${path}.missing`)
  try {
    await expect(emitModel()).rejects.toThrow(
      'cannot read preprocessor_config.json',
    )
    expect(fetch).not.toHaveBeenCalled()
  } finally {
    await rename(`${path}.missing`, path)
  }
})

// Change only the compressed pin in the copied manifest, so the real plugin must
// still validate the original expanded model hash, size and decompression bound.
async function emitGzipInput(data: Buffer): Promise<Map<string, Buffer>> {
  const input = join(fixture, 'src/features/dictation/model/tokenizer.json.gz')
  const manifest = join(fixture, 'src/features/dictation/whisper-model.json')
  const original = await readFile(input)
  const originalManifest = await readFile(manifest, 'utf8')
  const pins = JSON.parse(originalManifest) as typeof model
  pins.files['tokenizer.json'].gzipSha256 = createHash('sha256')
    .update(data)
    .digest('hex')
  try {
    await writeFile(input, data)
    await writeFile(manifest, JSON.stringify(pins))
    return await emitModel(await loadGenerator())
  } finally {
    await writeFile(input, original)
    await writeFile(manifest, originalManifest)
  }
}

it('rejects wrong expanded bytes even when their gzip pin matches', async () => {
  const source = await readFile(
    join(fixture, 'src/features/dictation/model/tokenizer.json.gz'),
  )
  const wrong = gunzipSync(source)
  wrong[0] ^= 1
  await expect(emitGzipInput(gzipSync(wrong))).rejects.toThrow(
    'tokenizer.json does not match its pinned SHA-256',
  )
})

it.each(['invalid gzip', 'oversized expanded input'])(
  'names %s after validating its compressed pin',
  async (kind) => {
    const source = await readFile(
      join(fixture, 'src/features/dictation/model/tokenizer.json.gz'),
    )
    const data =
      kind === 'invalid gzip'
        ? Buffer.from('not a gzip archive')
        : gzipSync(Buffer.concat([gunzipSync(source), Buffer.from([0])]))
    await expect(emitGzipInput(data)).rejects.toThrow(
      'cannot decompress tokenizer.json.gz',
    )
  },
)
