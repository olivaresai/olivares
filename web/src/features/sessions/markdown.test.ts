// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { describe, expect, it } from 'vitest'
import {
  parseInline,
  parseMarkdown,
  plainText,
  safeHref,
  type Block,
} from './markdown'

it('lets a governed wiki resolve relative links throughout Markdown without widening reply links', () => {
  const resolve = (href: string) =>
    href === 'wiki/earth.md' ? '#earth' : safeHref(href)
  const linked = { kind: 'link', href: '#earth', children: ['Earth'] }
  const source =
    '# [Earth](wiki/earth.md)\n\n- **[Earth](wiki/earth.md)**\n\n> [Earth](wiki/earth.md)\n\n| Page |\n| --- |\n| [Earth](wiki/earth.md) |'
  expect(JSON.stringify(parseMarkdown(source, resolve))).toContain(
    JSON.stringify(linked),
  )
  expect(
    JSON.stringify(parseMarkdown(source, resolve)).match(/"href":"#earth"/g),
  ).toHaveLength(4)
  expect(JSON.stringify(parseMarkdown(source))).not.toContain('"kind":"link"')
})

describe('parseMarkdown — blocks (#1784)', () => {
  it('retains the source of each top-level block for copying without rebuilding Markdown', () => {
    const source = [
      '## **Heading**',
      '- one\n  - `nested`',
      '> quoted\n> **words**',
      '| A | B |\n| --- | --- |\n| 1 | 2 |',
      '---',
      '```sh\n\tmake test\n```',
      'A [link](https://example.com).',
    ]
    const copied: string[] = []
    parseMarkdown(source.join('\n\n'), undefined, (_block, markdown) =>
      copied.push(markdown),
    )
    expect(copied).toEqual(source)
  })

  it('reads the reply that was shown raw: bold lead-ins, nested items, ordered steps and code', () => {
    const blocks = parseMarkdown(
      [
        'To find them:',
        '',
        '- **Identify clients:** list who calls',
        '- Check `config.yaml`',
        '  - first',
        '  - second',
        '',
        '1. Run',
        '2. Read',
        '',
        '```sh',
        'make test',
        '```',
      ].join('\n'),
    )
    expect(blocks.map((b) => b.kind)).toEqual([
      'paragraph',
      'list',
      'list',
      'code',
    ])
    const [, bullets, steps, code] = blocks
    expect(bullets).toMatchObject({ ordered: false })
    if (bullets?.kind !== 'list') throw new Error('not a list')
    expect(bullets.items).toHaveLength(2)
    expect(bullets.items[0]![0]).toEqual({
      kind: 'paragraph',
      inline: [
        { kind: 'strong', children: ['Identify clients:'] },
        ' list who calls',
      ],
    })
    // the second item holds its own words and a nested list of two
    expect(bullets.items[1]!.map((b) => b.kind)).toEqual(['paragraph', 'list'])
    expect(steps).toMatchObject({ ordered: true, start: 1 })
    expect(code).toEqual({ kind: 'code', text: 'make test' })
  })

  it('keeps one ordered list across blank lines and honours its first number', () => {
    const [list] = parseMarkdown('3. three\n\n4. four\n\n5. five')
    expect(list).toMatchObject({ kind: 'list', ordered: true, start: 3 })
    if (list?.kind !== 'list') throw new Error('not a list')
    expect(list.items).toHaveLength(3)
  })

  it('reads headings by level, a quote around a list, and a rule', () => {
    const blocks = parseMarkdown('## Plan\n\n> - one\n> - two\n\n---\n\ndone')
    expect(blocks.map((b) => b.kind)).toEqual([
      'heading',
      'quote',
      'rule',
      'paragraph',
    ])
    expect(blocks[0]).toMatchObject({ level: 2, inline: ['Plan'] })
    const quote = blocks[1]
    if (quote?.kind !== 'quote') throw new Error('not a quote')
    expect(quote.blocks[0]).toMatchObject({ kind: 'list' })
  })

  it('keeps a fence open to the end of the reply while it is still streaming', () => {
    expect(parseMarkdown('```py\nprint(1)\nprint(2)')).toEqual([
      { kind: 'code', text: 'print(1)\nprint(2)' },
    ])
    // markers inside a fence are the code, not markdown
    expect(parseMarkdown('~~~\n- **x**\n~~~')).toEqual([
      { kind: 'code', text: '- **x**' },
    ])
    // a longer fence holds a shorter one
    expect(parseMarkdown('````md\n```\ninner\n```\n````')).toEqual([
      { kind: 'code', text: '```\ninner\n```' },
    ])
  })

  it('does not take prose that starts with backticks or tildes for a fence', () => {
    // each of these used to open a code box that swallowed the rest of the reply
    for (const line of [
      '```ls -la``` lists files',
      '```Warning: deletes data',
      '~~~important~~~',
    ]) {
      const [block, ...rest] = parseMarkdown(`${line}\nthen more words`)
      expect(block?.kind).toBe('paragraph')
      expect(rest).toEqual([])
      expect(JSON.stringify(block)).toContain('then more words')
    }
    expect(parseInline('```ls -la``` lists')).toEqual([
      { kind: 'code', text: 'ls -la' },
      ' lists',
    ])
  })

  it('keeps a fenced block, blank lines included, inside a list item', () => {
    const [list] = parseMarkdown(
      '1. run:\n\n   ```sh\n   make\n\n\n   make test\n   ```\n2. done',
    )
    if (list?.kind !== 'list') throw new Error('not a list')
    expect(list.items).toHaveLength(2)
    expect(list.items[0]!.map((b) => b.kind)).toEqual(['paragraph', 'code'])
    expect(list.items[0]![1]).toEqual({
      kind: 'code',
      text: 'make\n\n\nmake test',
    })
  })

  it('keeps the lines of a paragraph as written, indentation and breaks included', () => {
    const [block] = parseMarkdown('Traceback:\n  File "a.py", line 3\n    boom')
    expect(block).toEqual({
      kind: 'paragraph',
      inline: ['Traceback:\n  File "a.py", line 3\n    boom'],
    })
  })

  it('drops only the closing hashes of a heading', () => {
    const blocks = parseMarkdown('## Title ##\n# C# notes')
    expect(blocks).toMatchObject([
      { kind: 'heading', level: 2, inline: ['Title'] },
      { kind: 'heading', level: 1, inline: ['C# notes'] },
    ])
  })

  it('reads a table, an escaped pipe in a cell and a short row', () => {
    const [table] = parseMarkdown(
      '| a | b |\n|---|:-:|\n| 1 | `x\\|y` |\n| 2 |',
    )
    expect(table).toEqual({
      kind: 'table',
      head: [['a'], ['b']],
      rows: [[['1'], [{ kind: 'code', text: 'x|y' }]], [['2']]],
    })
  })

  it('keeps every cell of a row wider than its header, and costs a row only what its line costs', () => {
    const [table] = parseMarkdown('| a |\n|---|\n| 1 | 2 | 3 |')
    expect(table).toMatchObject({ rows: [[['1'], ['2'], ['3']]] })
    // 400 columns by 2000 one-pipe rows used to be padded to 800,000 cells
    const wide = `${'a|'.repeat(400)}\n${'-|'.repeat(400)}\n${'|\n'.repeat(2000)}`
    const [big] = parseMarkdown(wide)
    if (big?.kind !== 'table') throw new Error('not a table')
    expect(big.rows.reduce((n, row) => n + row.length, 0)).toBe(0)
  })

  it('does not take a line of prose that starts with a number for a list', () => {
    const blocks = parseMarkdown('it was written in\n2019. then it shipped')
    expect(blocks.map((b) => b.kind)).toEqual(['paragraph'])
  })

  it('reads tabs and CRLF like spaces and newlines', () => {
    const blocks = parseMarkdown('- a\r\n\t- b\r\n')
    expect(blocks).toHaveLength(1)
    const [list] = blocks
    if (list?.kind !== 'list') throw new Error('not a list')
    expect(list.items[0]!.map((b) => b.kind)).toEqual(['paragraph', 'list'])
  })
})

describe('parseInline (#1784)', () => {
  it('reads code, bold and italic, and keeps snake_case and arithmetic as written', () => {
    expect(parseInline('`a` **b** *c* _d_')).toEqual([
      { kind: 'code', text: 'a' },
      ' ',
      { kind: 'strong', children: ['b'] },
      ' ',
      { kind: 'em', children: ['c'] },
      ' ',
      { kind: 'em', children: ['d'] },
    ])
    expect(parseInline('use snake_case_name for 2 * 3 * 4')).toEqual([
      'use snake_case_name for 2 * 3 * 4',
    ])
  })

  it('nests emphasis and code inside bold, and bold inside a link label', () => {
    expect(parseInline('**see `x`**')).toEqual([
      {
        kind: 'strong',
        children: ['see ', { kind: 'code', text: 'x' }],
      },
    ])
    expect(parseInline('[**docs**](https://example.com)')).toEqual([
      {
        kind: 'link',
        href: 'https://example.com',
        children: [{ kind: 'strong', children: ['docs'] }],
      },
    ])
  })

  it('keeps an escaped marker and an unclosed backtick as the characters sent', () => {
    expect(parseInline('\\*not em\\* and \\`not code\\`')).toEqual([
      '*not em* and `not code`',
    ])
    expect(parseInline('a ``b` c')).toEqual(['a ``b` c'])
  })

  it('keeps a line break inside a paragraph as the character, for the painter to show', () => {
    expect(parseInline('one\ntwo')).toEqual(['one\ntwo'])
  })

  it('leaves a path, a glob and arithmetic as written, and still reads a*b*c', () => {
    for (const text of [
      'C:\\temp\\.hidden',
      '*.go *.ts',
      '2 * 3 * 4',
      'snake_case_name and __not_closed',
    ])
      expect(parseInline(text)).toEqual([text])
    expect(parseInline('a*b*c')).toEqual([
      'a',
      { kind: 'em', children: ['b'] },
      'c',
    ])
  })

  it('reads an address with parentheses, a title and an autolink', () => {
    expect(
      parseInline('[w](https://e.com/a_(b) "t") <https://e.com/x>'),
    ).toEqual([
      {
        kind: 'link',
        href: 'https://e.com/a_(b)',
        children: ['w'],
      },
      ' ',
      {
        kind: 'link',
        href: 'https://e.com/x',
        children: ['https://e.com/x'],
      },
    ])
  })
})

describe('safeHref — only web and mail addresses are links (#1784)', () => {
  it.each([
    ['https://example.com/a?b=1#c', true],
    ['HTTP://EXAMPLE.COM', true],
    ['mailto:a@example.com', true],
    ['javascript:alert(1)', false],
    ['JaVaScRiPt:alert(1)', false],
    ['data:text/html;base64,AAAA', false],
    ['vbscript:msgbox(1)', false],
    ['file:///etc/passwd', false],
    ['//example.com', false],
    ['/relative', false],
    ['#fragment', false],
    [' https://example.com', false],
    ['https://exa mple.com', false],
    ['https://example.com/<x>', false],
  ])('%s -> %s', (url, allowed) => {
    expect(safeHref(url) !== null).toBe(allowed)
  })

  it('keeps a refused link exactly as it was written, so no path or address is lost', () => {
    expect(parseInline('[click](javascript:alert(1))')).toEqual([
      '[click](javascript:alert(1))',
    ])
    expect(parseInline('see [main.go](src/main.go:42) now')).toEqual([
      'see ',
      '[main.go](src/main.go:42)',
      ' now',
    ])
    expect(parseInline('![chart](data:image/png;base64,AAAA)')).toEqual([
      '![chart](data:image/png;base64,AAAA)',
    ])
  })

  it('shows an image as a link to it, named by its alt text or else by its address', () => {
    expect(parseInline('![chart](https://e.com/c.png)')).toEqual([
      { kind: 'link', href: 'https://e.com/c.png', children: ['chart'] },
    ])
    expect(parseInline('![](https://e.com/c.png)')).toEqual([
      {
        kind: 'link',
        href: 'https://e.com/c.png',
        children: ['https://e.com/c.png'],
      },
    ])
  })

  it('adds the real host when a label that reads as an address names another one', () => {
    expect(
      parseInline('[https://bank.example/login](https://evil.example/x)'),
    ).toEqual([
      {
        kind: 'link',
        href: 'https://evil.example/x',
        children: ['https://bank.example/login', ' (evil.example)'],
      },
    ])
    // styling the label does not hide it
    expect(
      parseInline('[**https://bank.example**](https://evil.example)'),
    ).toEqual([
      {
        kind: 'link',
        href: 'https://evil.example',
        children: [
          { kind: 'strong', children: ['https://bank.example'] },
          ' (evil.example)',
        ],
      },
    ])
    // the same host, with or without www, a plain label and a mail link say nothing more
    for (const text of [
      '[https://example.com/docs](https://example.com/a)',
      '[www.example.com](https://example.com)',
      '[the docs](https://evil.example)',
      '[a@example.com](mailto:a@example.com)',
    ]) {
      const [node] = parseInline(text)
      if (typeof node === 'string' || node?.kind !== 'link')
        throw new Error('not a link')
      expect(node.children.join('')).not.toMatch(/\(/)
    }
  })

  it('never nests one link in another, and keeps the inner address in the words', () => {
    expect(parseInline('[a [b](https://x.y)](https://z.w)')).toEqual([
      {
        kind: 'link',
        href: 'https://z.w',
        children: ['a ', 'b', ' (https://x.y)'],
      },
    ])
    expect(parseInline('[<https://a.b>](https://c.d)')).toEqual([
      { kind: 'link', href: 'https://c.d', children: ['<https://a.b>'] },
    ])
  })
})

describe('parseMarkdown — a hostile or runaway reply stays bounded (#1784)', () => {
  const depthOf = (blocks: Block[]): number => {
    const quote = blocks.find((b) => b.kind === 'quote')
    return quote?.kind === 'quote' ? 1 + depthOf(quote.blocks) : 0
  }

  it('stops nesting at a fixed depth and paints what is deeper as words', () => {
    const blocks = parseMarkdown(`${'>'.repeat(60)} deep`)
    expect(depthOf(blocks)).toBe(9)
    expect(JSON.stringify(blocks)).toContain('deep')
    const bullets = Array.from(
      { length: 400 },
      (_, i) => `${' '.repeat(2 * i)}- item`,
    ).join('\n')
    expect(() => parseMarkdown(bullets)).not.toThrow()
  })

  it('leaves a paragraph past the size cap as the characters it was sent in', () => {
    const long = `*a ${'word '.repeat(5000)}a*`
    expect(parseInline(long)).toEqual([long])
  })

  it('leaves unmatched markers alone, however many there are', () => {
    const marks = '*a '.repeat(6000)
    expect(parseInline(marks)).toEqual([marks])
    const brackets = '['.repeat(15000)
    expect(parseInline(brackets)).toEqual([brackets])
  })

  it('reads the inputs that were quadratic in time well inside a generous budget', () => {
    const started = Date.now()
    parseMarkdown(`- a${'\n'.repeat(60_000)}  b`) // blank lines inside a list item
    parseMarkdown(`# a${' '.repeat(60_000)}b`) // a heading with a long run of spaces
    parseMarkdown(`- ${' '.repeat(60_000)}a\u2028`) // an item ending in a line separator
    parseInline(`x${' '.repeat(19_000)}y\nz`)
    parseInline('*a '.repeat(6000))
    // quadratic work took seconds each; linear takes milliseconds. Not a benchmark.
    expect(Date.now() - started).toBeLessThan(4000)
  })

  // One run of 80,000 characters took 4 to 5 s with a backtracking closing-run or fence
  // scan, and the live reply repeats it on every delta. Each input has its own test, so a
  // mutant of one scan turns only its own test red.
  const BUDGET_MS = 1500
  const takes = (run: () => void) => {
    const started = Date.now()
    run()
    return Date.now() - started
  }

  it('reads a heading with a very long run of # in its words in linear time', () => {
    const text = `# a${'#'.repeat(80_000)}b`
    expect(takes(() => parseMarkdown(text))).toBeLessThan(BUDGET_MS)
  })

  it('keeps the closing # of a heading out of its words and the others in', () => {
    expect(parseMarkdown('## Title ##')).toEqual([
      { kind: 'heading', level: 2, inline: ['Title'] },
    ])
    expect(parseMarkdown('# C#')).toEqual([
      { kind: 'heading', level: 1, inline: ['C#'] },
    ])
    expect(parseMarkdown('# ###')).toEqual([
      { kind: 'heading', level: 1, inline: ['###'] },
    ])
    // the walk back over a closing run as long as the reply allows
    expect(parseMarkdown(`# Title ${'#'.repeat(80_000)}`)).toEqual([
      { kind: 'heading', level: 1, inline: ['Title'] },
    ])
  })

  it.each(['`', '~'])(
    'reads a %s fence line followed by a very long run of spaces in linear time',
    (mark) => {
      const text = `${mark.repeat(3)} ${' '.repeat(80_000)}!`
      expect(takes(() => parseMarkdown(text))).toBeLessThan(BUDGET_MS)
    },
  )

  it('reads the same fence line as the next line of a paragraph in linear time', () => {
    const text = `a\n\`\`\` ${'\t'.repeat(80_000)}!`
    expect(takes(() => parseMarkdown(text))).toBeLessThan(BUDGET_MS)
  })

  it('still opens a fence that has trailing spaces or tabs after its language', () => {
    expect(parseMarkdown('```ts \t \nx\n```')).toEqual([
      { kind: 'code', text: 'x' },
    ])
    expect(parseMarkdown('~~~   \nx')).toEqual([{ kind: 'code', text: 'x' }])
    // blanks only after the marker, as many as the reply allows
    expect(parseMarkdown(`\`\`\` ${' '.repeat(80_000)}\nx`)).toEqual([
      { kind: 'code', text: 'x' },
    ])
  })
})

// the regression: a reply is copied as the person reads it, without the markers it was written
// in; a code block keeps its content exactly.
describe('plainText', () => {
  it('reads a reply as shown: no markers, lists and code kept', () => {
    const reply =
      'Two things matter:\n\n- **Identify clients:** list who calls the API\n- Check `README.md` for the [title](https://example.com/t)\n\n```sh\nmake test\n  --verbose\n```'
    expect(plainText(parseMarkdown(reply))).toBe(
      'Two things matter:\n\n- Identify clients: list who calls the API\n- Check README.md for the title (https://example.com/t)\n\nmake test\n  --verbose',
    )
  })
})

describe('plainText — review of the copy', () => {
  it('keeps the tabs of a code block exactly', () => {
    const reply = 'Run:\n\n```make\nbuild:\n\tgo build ./...\n```'
    const blocks = parseMarkdown(reply)
    expect(blocks.at(-1)).toEqual({
      kind: 'code',
      text: 'build:\n\tgo build ./...',
    })
    expect(plainText(blocks)).toBe('Run:\n\nbuild:\n\tgo build ./...')
  })

  it('copies a nested list without blank lines the reader never saw', () => {
    expect(plainText(parseMarkdown('- a\n  - b\n- c'))).toBe('- a\n  - b\n- c')
  })
})

// Review of #2856: a fence inside a list item keeps a tab-led line inside the item;
// the list stays whole and the code keeps its tab.
it('keeps a list whole when a fence in an item has a tab-led line', () => {
  const blocks = parseMarkdown(
    '- step:\n  ```make\n  build:\n\tgo build\n  ```\n- next',
  )
  expect(blocks).toHaveLength(1)
  const list = blocks[0] as Extract<Block, { kind: 'list' }>
  expect(list.kind).toBe('list')
  expect(list.items).toHaveLength(2)
  expect(list.items[0]![1]).toEqual({
    kind: 'code',
    text: 'build:\n\tgo build',
  })
  expect(list.items[1]).toEqual([{ kind: 'paragraph', inline: ['next'] }])
})

// Review of #2856, round 2: indentation reads a leading tab as four spaces where it is
// measured, and a fence body keeps its lines as written, wherever the fence sits.
describe('parseMarkdown — tabs in and around a fence', () => {
  it('ends a fence left open in an item with the item: a later tab-indented list nests', () => {
    const [list] = parseMarkdown('- a\n  ```\n  x\n- b\n\t- nested')
    if (list?.kind !== 'list') throw new Error('not a list')
    expect(list.items[1]!.map((b) => b.kind)).toEqual(['paragraph', 'list'])
  })

  it('keeps the tab of a line in a fence indented one to three spaces', () => {
    expect(parseMarkdown(' ```\n\tall:\n ```')).toEqual([
      { kind: 'code', text: '\tall:' },
    ])
  })

  it('keeps the tab of a line in a fence nested four spaces deep', () => {
    const [list] = parseMarkdown(
      '- a\n  - b\n    ```make\n    all:\n\tgo build\n    ```',
    )
    if (list?.kind !== 'list') throw new Error('not a list')
    const [, inner] = list.items[0]!
    if (inner?.kind !== 'list') throw new Error('not a nested list')
    expect(inner.items[0]![1]).toEqual({
      kind: 'code',
      text: 'all:\n\tgo build',
    })
  })
})

it('copies a link whose label names another host with its address once', () => {
  expect(plainText(parseMarkdown('[https://a.com](https://b.com/x)'))).toBe(
    'https://a.com (https://b.com/x)',
  )
})
