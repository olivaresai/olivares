#!/usr/bin/env python3
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
#
# check-md-links.py — every relative markdown link, image and HTML src in a tree must
# resolve to a file or directory that EXISTS **inside** that same tree.
#
# Born as the export leak gate's missing leg: the curated public export drops internal
# paths on purpose, and a shipped document that links to a dropped path renders as a 404
# exactly where the public repo tries to prove a claim. Resolving each reference against
# the tree needs no list of internal names: whatever the curation dropped, or whatever
# was simply mistyped, fails the same way a visitor would see it fail.
#
# Hardened after a second-opinion audit measured four green escapes in the first
# version: uppercase/spaced/unquoted HTML attributes, srcset comma handling, targets
# that resolve OUTSIDE the scanned root via ../, and fence handling that only knew
# three-backtick fences. Containment is now enforced with realpath/commonpath (symlink
# traversal included), HTML attributes are matched case-insensitively with optional
# whitespace and unquoted values, srcset skips data: URLs and tokenizes per candidate,
# and fences track their marker character and length per CommonMark's close rule.
#
# Known, stated limits (line-oriented scanner, no full CommonMark parser is available
# offline): multiline links, multiline headings, and headings nested in
# blockquotes/lists are not parsed.
# Route-space fragments and fragments in non-Markdown assets remain the owning
# renderer's responsibility; this scanner checks repository Markdown pages.
#
# Scope rules:
#   * Only .md/.mdx files are scanned; --skip PREFIX excludes subtrees whose links live
#     in another address space (docs-site/src/content is ROUTE-space and gated by the
#     site's own build-time link check).
#   * External schemes and template expressions are out of scope. Local Markdown
#     fragments must match a heading or an explicit HTML id/name in the target page.
#   * A leading-/ target resolves against the scanned root (GitHub repo-relative form).
#
# Output: one "file:line: target [reason]" per unresolved reference on stdout; exit 1
# if any reference fails. --self-test builds
# red and green fixture trees and exits non-zero if any fixture misbehaves.
import html
import os
import re
import signal
import string
import subprocess
import sys
import tempfile
import unicodedata
from typing import Iterator
from urllib.parse import unquote

# stdout may feed a head/tail; SIGPIPE must terminate quietly, never traceback (this
# repo has shipped that lesson twice)
signal.signal(signal.SIGPIPE, signal.SIG_DFL)

MD_EXT = (".md", ".mdx")
INLINE = re.compile(r"!?\[[^\]]*\]\(\s*<?([^)\s>]+)>?(?:\s+\"[^\"]*\")?\s*\)")
REFLABEL = re.compile(r"^ {0,3}\[(?P<label>(?:\\.|[^\[\]\\]){1,999})\]:[ \t]*")


def reference_definition(line: str) -> tuple[str, str] | None:
    """Validate a single-line GFM definition before it affects links or headings."""
    label = REFLABEL.match(line)
    if not label or not label.group("label").strip():
        return None
    rest = line[label.end():]
    if not rest:
        return None
    angle = rest.startswith("<")
    start = 1 if angle else 0
    i = start
    depth = 0
    while i < len(rest):
        char = rest[i]
        if char == "\\" and i + 1 < len(rest) and rest[i + 1] in string.punctuation:
            i += 2
            continue
        if angle and char == ">":
            break
        if ((angle and char in "<>\r\n") or
                (not angle and (ord(char) <= 32 or ord(char) == 127))):
            if not angle and char in " \t":
                break
            return None
        if not angle:
            if char == "(":
                depth += 1
            elif char == ")":
                depth -= 1
                if depth < 0:
                    return None
        i += 1
    if (angle and (i == len(rest) or rest[i] != ">")) or depth or (not angle and i == 0):
        return None
    target = rest[start:i]
    tail = rest[i + 1:] if angle else rest[i:]
    if tail and tail.strip(" \t"):
        if tail[0] not in " \t":
            return None
        title = tail.strip(" \t")
        if title[0] not in "\"'(":
            return None
        closing = ")" if title[0] == "(" else title[0]
        j = 1
        while j < len(title):
            if (title[j] == "\\" and j + 1 < len(title) and
                    title[j + 1] in string.punctuation):
                j += 2
                continue
            if title[j] == closing:
                if title[j + 1:].strip(" \t"):
                    return None
                break
            if closing == ")" and title[j] == "(":
                return None
            j += 1
        else:
            return None
    target = html.unescape(re.sub(r"\\([" + re.escape(string.punctuation) + r"])", r"\1", target))
    return label.group("label"), target


# HTML attributes markdown renders — case-insensitive names, optional whitespace around
# '=', quoted or unquoted values (unquoted ends at whitespace or '>').
HTML_SRC = re.compile(r"\b(?:src|href)\s*=\s*(?:\"([^\"]*)\"|'([^']*)'|([^\s\"'>]+))", re.I)
HTML_SRCSET = re.compile(r"\bsrcset\s*=\s*(?:\"([^\"]*)\"|'([^']*)'|([^\s\"'>]+))", re.I)
FENCE = re.compile(r"^\s{0,3}(`{3,}|~{3,})")

SKIP_PREFIXES = ("http://", "https://", "mailto:", "data:", "tel:", "ftp:", "{", "$")


def srcset_candidates(value):
    """Per-candidate URLs of a srcset value. data: URLs may carry commas, so candidates
    are split on commas (whitespace-tolerant) — the practical form of the HTML candidate
    grammar for the path URLs this tree uses; a whole-value data: URL is skipped before
    the split, and a data: candidate mixed into a multi-candidate srcset is a stated
    limit with no occurrence here."""
    out = []
    for cand in re.split(r"\s*,\s*", value.strip()):
        cand = cand.strip()
        if not cand:
            continue
        url = cand.split()[0]
        out.append(url)
    return out


# ⛔ UN ATRIBUTO SÓLO EXISTE DENTRO DE UNA ETIQUETA, y hasta 2026-08-26 esto no lo exigía.
#    `HTML_SRC` lleva `\b(?:src|href)\s*=`, y `\b` casa tras una barra: la prosa
#    «Tus cinco dan `web/src=0`» de un buzón producía un hallazgo con destino `0`. Medido en
#    el árbol: ~21 falsos positivos de esta forma, todos en `sessions/`, y ninguno era un
#    enlace. Restringirlo al interior de `<...>` NO pierde las formas que la auditoría del
#    2026-08-04 midió como escapes (mayúsculas, espacios, valores sin comillas): esas siguen
#    siendo atributos y siguen viviendo dentro de la etiqueta.
TAG = re.compile(r"<[^<>]*>")
AUTOLINK = re.compile(
    r"<(?P<autolink>[A-Za-z][A-Za-z0-9+.-]{1,31}:[^\x00-\x20<>]*|"
    r"[A-Za-z0-9.!#$%&'*+/=?^_`{|}~-]+@[A-Za-z0-9](?:[A-Za-z0-9-]{0,61}[A-Za-z0-9])?"
    r"(?:\.[A-Za-z0-9](?:[A-Za-z0-9-]{0,61}[A-Za-z0-9])?)*)>")
# Complete HTML tags (GFM type 7), unlike autolinks or arbitrary angle-bracket text.
HTML_TAG = re.compile(
    r"</[A-Za-z][A-Za-z0-9-]*[ \t]*>|<[A-Za-z][A-Za-z0-9-]*"
    r"(?:[ \t]+[A-Za-z_:][A-Za-z0-9_.:-]*(?:[ \t]*=[ \t]*"
    r"(?:[^ \t\n\r\f\"'=<>`]+|'[^']*'|\"[^\"]*\"))?)*[ \t]*/?>")
HTML_BLOCK_TAG = re.compile(
    r"^ {0,3}</?(?:address|article|aside|base|basefont|blockquote|body|caption|center|col|"
    r"colgroup|dd|details|dialog|dir|div|dl|dt|fieldset|figcaption|figure|footer|form|frame|"
    r"frameset|h[1-6]|head|header|hr|html|iframe|legend|li|link|main|menu|menuitem|nav|"
    r"noframes|ol|optgroup|option|p|param|section|source|summary|table|tbody|td|tfoot|th|"
    r"thead|title|tr|track|ul)(?=[ \t\n\r\f]|/?>|$)", re.I)


# ⛔ UN ENLACE DENTRO DE COMILLAS INVERTIDAS NO ES UN ENLACE. El guion honraba las CERCAS de
#    bloque y no los tramos EN LÍNEA, así que la prosa que MUESTRA la forma de un enlace se
#    contaba como enlace. Los dos casos medidos en el árbol el 2026-08-26 eran exactamente eso:
#      ESTADO-PROYECTO.md:3460   «las rutas relativas de imágenes (`![](../../../../assets/…`)»
#      docs/launch/reddit-pack.md:11  «The `![...](./assets/*.png)` references below are TODO»
#    Ambos documentan la forma; ninguno enlaza. Se retiran los tramos en línea ANTES de buscar,
#    con la regla de cierre de CommonMark (una serie de N backticks cierra con otra de N).
CODESPAN = re.compile(r"(?P<ticks>`+)(?!`)(?:(?!(?<!`)(?P=ticks)(?!`)).)*?"
                      r"(?<!`)(?P=ticks)(?!`)", re.S)
# Consume unmatched runs whole so a suffix cannot open a shorter code span.
HEADING_LITERAL = re.compile(r"\\(?P<escape>[" + re.escape(string.punctuation) + r"])|" +
                             CODESPAN.pattern + r"|`+", re.S)


def strip_codespans(line):
    return HEADING_LITERAL.sub(lambda m: " " if m.group("ticks") else m.group(0), line)


def protect_literal_lt(line: str) -> str:
    """Literal '<' cannot open an HTML tag or comment."""
    return HEADING_LITERAL.sub(lambda m: m.group(0).replace("<", "\0literal-lt\0"), line)


def targets_in(line: str, allow_definition: bool = True) -> list[str]:
    definition = reference_definition(line) if allow_definition else None
    tags = TAG.finditer(protect_literal_lt(line))
    line = strip_codespans(line)
    out = []
    for m in INLINE.finditer(line):
        out.append(m.group(1))
    if definition:
        out.append(definition[1])
    for tag in tags:
        span = tag.group(0)
        for m in HTML_SRC.finditer(span):
            out.append(next(g for g in m.groups() if g is not None))
        for m in HTML_SRCSET.finditer(span):
            value = next(g for g in m.groups() if g is not None)
            if value.startswith("data:"):
                continue
            out.extend(srcset_candidates(value))
    return out


# ⛔ LA RAÍZ DEL GATE NO PUEDE SER UN `os.walk`, y es una medida: esta caja tiene **26 363**
#    markdown bajo `.claude/worktrees/` frente a **4 417** trackeados en TODO el repositorio.
#    Recorriéndolo entero, un fast-lint haría seis veces el trabajo del repositorio sobre
#    ficheros que no son del repositorio — y su duración y su verde dependerían de qué
#    worktrees efímeros haya en la caja ese día. Un control cuyo resultado depende del entorno
#    no es un control. Excluir `.claude` a mano tampoco vale: ese directorio sólo está ignorado
#    por `.git/info/exclude`, que es LOCAL y no viaja en el clon.
#
#    Por eso hay DOS modos y ninguno sobra: `--tracked` para un repositorio (determinista,
#    `git ls-files`) y el `os.walk` para un ÁRBOL SUELTO — que es el caso para el que este
#    guion nació: el export curado no es un repositorio git.
def tracked_md(root):
    out = subprocess.run(["git", "-C", root, "ls-files", "-z", "*.md", "*.mdx"],
                         capture_output=True, text=True)
    if out.returncode != 0:
        raise SystemExit(f"check-md-links: COULD NOT CHECK: git ls-files failed in {root}")
    return [r for r in out.stdout.split("\0") if r]


def _candidates(root, files):
    """(dirpath, path, rel) de cada markdown a escanear. `files` = lista relativa ya elegida."""
    if files is not None:
        for rel in files:
            path = os.path.join(root, rel)
            yield os.path.dirname(path), path, rel
        return
    for dirpath, dirs, names in os.walk(root):
        dirs[:] = [d for d in dirs if d not in (".git", "node_modules")]
        for name in names:
            if not name.endswith(MD_EXT):
                continue
            path = os.path.join(dirpath, name)
            yield dirpath, path, os.path.relpath(path, root).replace(os.sep, "/")


THEMATIC_BREAK = re.compile(r" {0,3}(?:(?:\*[ \t]*){3,}|(?:_[ \t]*){3,}|(?:-[ \t]*){3,})")
LIST_ITEM = re.compile(r"^ {0,3}(?P<marker>[-+*]|(?P<number>[0-9]{1,9})[.)])(?:[ \t]+|$)")


def list_item(line: str, paragraph: bool = False) -> re.Match[str] | None:
    """Thematic breaks win; only nonempty items starting at 1 interrupt paragraphs."""
    item = LIST_ITEM.match(line)
    if (not item or THEMATIC_BREAK.fullmatch(line) or
            (paragraph and (not line[item.end():].strip() or
                            (item.group("number") and int(item.group("number")) != 1)))):
        return None
    return item


def paragraph_line(line: str, paragraph: bool = False) -> bool:
    """Paragraph candidates for setext headings and non-interrupting HTML blocks."""
    # Indented code cannot interrupt an existing paragraph, including a lazy one.
    return bool(re.match(r"^ *\S" if paragraph else r"^ {0,3}\S", line.expandtabs(4)) and
                not re.match(r"^ {0,3}#{1,6}(?:[ \t]|$)", line) and
                not re.fullmatch(r" {0,3}(?:=+|-+)[ \t]*", line) and
                (not reference_definition(line) or paragraph) and
                not re.match(r"^ {0,3}>", line) and not list_item(line, paragraph) and
                not THEMATIC_BREAK.fullmatch(line))


def html_block_end(line: str, paragraph: bool) -> tuple[str, re.Pattern[str]] | None:
    """GFM's seven HTML block starts and their terminating line patterns."""
    if re.match(r"^ {0,3}<(?:script|pre|style)(?=[ \t\n\r\f>]|$)", line, re.I):
        return "element", re.compile(r"</(?:script|pre|style)>", re.I)
    for kind, start, end in (("comment", r"<!--", r"-->"),
                             ("opaque", r"<\?", r"\?>"),
                             ("opaque", r"<![A-Z]", r">"),
                             ("opaque", r"<!\[CDATA\[", r"\]\]>")):
        if re.match(r"^ {0,3}" + start, line):
            return kind, re.compile(end)
    if HTML_BLOCK_TAG.match(line) or (not paragraph and
            re.fullmatch(r" {0,3}(?:" + HTML_TAG.pattern + r")[ \t]*", line)):
        return "element", re.compile(r"^[ \t]*$")
    return None


def strip_comments(line: str, comment: bool) -> tuple[str, bool]:
    """Keep text beside comments, including comments spanning lines."""
    parts = []
    while line:
        if comment:
            _, end, line = line.partition("-->")
            if not end:
                break
            comment = False
        else:
            text, start, line = line.partition("<!--")
            parts.append(text)
            comment = bool(start)
    return "".join(parts), comment


def visible_lines(lines: list[str], markdown_only: bool = False,
                  definitions_only: bool = False) -> Iterator[tuple[int, str]]:
    """Visible lines; raw HTML exposes only tags, or is omitted for Markdown parsing."""
    fence = None
    comment = False
    raw_html = None
    paragraph = False
    context = ()
    frontmatter = bool(lines and lines[0] == "---" and
                       any(line in ("---", "...") for line in lines[1:]))
    for i, line in enumerate(lines, 1):
        if frontmatter:
            if i > 1 and line in ("---", "..."):
                frontmatter = False
            continue
        # Parse block boundaries inside containers too: their definitions are
        # document-wide, but code, comments and raw HTML remain examples.
        leading = re.match(r"^[ \t]*", line).group(0)
        line = leading.expandtabs(4) + line[len(leading):]
        containers = []
        for kind, width in context:
            if kind == "quote":
                quote = re.match(r"^ {0,3}>[ \t]?", line)
                if not quote:
                    break
                line = line[quote.end():]
            elif line.strip():
                if not line.startswith(" " * width):
                    break
                line = line[width:]
            containers.append((kind, width))
        item = list_item(line)
        sibling = (len(containers) < len(context) and item is not None and
                   context[len(containers)][0] == "list" + item.group("marker")[-1])
        # Missing markers can be lazy paragraph continuations, not new blocks.
        # Definitions cannot interrupt that paragraph; real block starts can.
        lazy = (tuple(containers) != context and paragraph and not sibling and
                paragraph_line(line, True) and not FENCE.match(line) and
                html_block_end(line, True) is None)
        if lazy:
            containers = list(context)
        elif tuple(containers) != context:
            fence = raw_html = None
            comment = paragraph = False
        new_item = False
        while not (lazy or fence is not None or raw_html is not None or comment):
            quote = re.match(r"^ {0,3}>[ \t]?", line)
            if quote:
                containers.append(("quote", 0))
                line = line[quote.end():]
                paragraph = False
                continue
            item = list_item(line, paragraph)
            if not item:
                break
            # More than four padding spaces starts indented code after the marker.
            padding = len(item.group(0)) - len(item.group(0).rstrip(" \t"))
            width = item.end() - (padding - 1 if padding > 4 else 0) + (not padding)
            containers.append(("list" + item.group("marker")[-1], width))
            line = line[width:]
            new_item = True
            paragraph = False
        current_context = tuple(containers)
        if current_context != context or new_item:
            fence = raw_html = None
            comment = paragraph = False
        context = current_context
        nested = bool(containers)
        if fence is not None:
            fm = FENCE.match(line)
            if (fm and fm.group(1)[0] == fence[0] and
                    len(fm.group(1)) >= fence[1] and not line[fm.end():].strip()):
                fence = None
            continue
        if raw_html is None and not comment:
            raw_html = html_block_end(line, paragraph)
        if raw_html is not None:
            kind, terminator = raw_html
            end = terminator.search(line)
            # Raw HTML cannot start a fence or create a Markdown heading. Keep
            # attributes visible to the existing path and HTML-anchor checks.
            if not markdown_only and kind == "element":
                line, comment = strip_comments(line, comment)
                # Backticks and Markdown links are raw text in an HTML block.
                yield i, " ".join(TAG.findall(line))
            if end:
                raw_html = None
            paragraph = False
            continue
        # Code and escaped '<' cannot start a comment; escaped backticks can.
        line = protect_literal_lt(line)
        line, comment = strip_comments(line, comment)
        line = line.replace("\0literal-lt\0", "<")
        fm = FENCE.match(line)
        if fm:
            marker = fm.group(1)
            fence = (marker[0], len(marker))
            paragraph = False
            continue
        definition = reference_definition(line) if not paragraph else None
        paragraph = paragraph_line(line, paragraph)
        if definitions_only:
            if definition:
                yield i, line
        elif not (markdown_only and nested):
            yield i, line


def reference_label(label: str) -> str:
    return " ".join(label.split()).casefold()


def strip_heading_markup(text: str, references: set[str], literal_sources: list[str]) -> str:
    # Hide non-visible attributes/destinations while retaining their punctuation
    # boundaries. Otherwise their underscores can pair with visible heading text.
    text = re.sub(r"!?\[([^\]]*)\]\([^)]*\)", lambda m: "[\0" + m.group(1) + "\0]", text)

    def reference(match: re.Match[str]) -> str:
        label = re.sub(r"`\0(\d+)\0`", lambda m: literal_sources[int(m.group(1))],
                       match.group(2) or match.group(1))
        if reference_label(label) in references:
            return "[\0" + match.group(1) + "\0]"
        return match.group(0)

    text = re.sub(r"!?\[([^\]]*)\]\[([^\]]*)\]", reference, text)
    text = TAG.sub("<\0>", text)
    # GFM underscores cannot open/close inside words. Boundary punctuation
    # includes ASCII symbols and Unicode punctuation. Inspect source boundaries
    # before stripping adjacent links, HTML or star emphasis.
    runs = []
    for run in re.finditer(r"_+", text):
        before = text[run.start() - 1] if run.start() else " "
        after = text[run.end()] if run.end() < len(text) else " "
        before_boundary = (before.isspace() or before in string.punctuation or
                           unicodedata.category(before)[0] == "P")
        after_boundary = (after.isspace() or after in string.punctuation or
                          unicodedata.category(after)[0] == "P")
        runs.append((run, not after.isspace() and before_boundary,
                     not before.isspace() and after_boundary))
    removed = set()
    remaining = [[run.start(), run.end()] for run, _, _ in runs]
    inactive = set()
    for i, (_, closer_can_open, can_close) in enumerate(runs):
        if not can_close:
            continue
        while remaining[i][0] < remaining[i][1]:
            closing = remaining[i][1] - remaining[i][0]
            for j in range(i - 1, -1, -1):
                _, can_open, opener_can_close = runs[j]
                opening = remaining[j][1] - remaining[j][0]
                if j in inactive or not can_open or not opening:
                    continue
                if ((opener_can_close or closer_can_open) and (opening + closing) % 3 == 0
                        and (opening % 3 or closing % 3)):
                    continue
                break
            else:
                break
            count = 2 if min(opening, closing) >= 2 else 1
            removed.update(range(remaining[j][1] - count, remaining[j][1]))
            removed.update(range(remaining[i][0], remaining[i][0] + count))
            remaining[j][1] -= count
            remaining[i][0] += count
            inactive.update(range(j + 1, i))
    text = "".join(c for i, c in enumerate(text) if i not in removed)
    text = text.replace("[\0", "").replace("\0]", "")
    text = TAG.sub("", text)
    return re.sub(r"(\*+)(.+?)\1", r"\2", text)


def page_anchors(lines: list[str]) -> set[str]:
    """GitHub heading slugs (including duplicate suffixes) and explicit HTML anchors."""
    anchors = set()
    seen = set()
    previous = ""
    previous_line = 0
    markdown = list(visible_lines(lines, markdown_only=True))
    references = {reference_label(reference_definition(line)[0]) for _, line in
                  visible_lines(lines, markdown_only=True, definitions_only=True)}
    for _, line in visible_lines(lines):
        for tag in TAG.finditer(protect_literal_lt(line)):
            for attr in re.finditer(r"\s(id|name)\s*=\s*(?:\"([^\"]*)\"|'([^']*)'|([^\s\"'>]+))",
                                    tag.group(0), re.I):
                if attr.group(1).lower() == "id" or re.match(r"<a(?:\s|>)", tag.group(0), re.I):
                    anchors.add(html.unescape(next(g for g in attr.groups()[1:] if g is not None)))
    for i, line in markdown:
        # Omitted blocks break the paragraph required by a setext underline.
        if i != previous_line + 1:
            previous = ""
        previous_line = i
        heading = re.match(r"^ {0,3}#{1,6}(?:[ \t]+(.*)|$)", line)
        setext = re.fullmatch(r" {0,3}(?:=+|-+)[ \t]*", line)
        if heading or (setext and previous):
            text = (heading.group(1) or "") if heading else previous
            if heading:
                text = re.sub(r"[ \t]+#+[ \t]*$", "", text)
            # Escapes and code are literal, including entities inside code. Match
            # them together so escaped backticks cannot start a code span.
            literal_text = []
            literal_sources = []

            def protect_literal(match: re.Match[str]) -> str:
                value = match.group("autolink") or match.group("escape")
                if value is None:
                    if match.group("ticks") is None:
                        return match.group(0)
                    ticks = len(match.group("ticks"))
                    value = match.group(0)[ticks:-ticks]
                    # GFM removes one paired padding space, except for all-space code.
                    if value.startswith(" ") and value.endswith(" ") and value.strip(" "):
                        value = value[1:-1]
                literal_text.append(value)
                literal_sources.append(match.group(0))
                return f"`\0{len(literal_text) - 1}\0`"

            text = strip_heading_markup(re.sub(AUTOLINK.pattern + "|" + HEADING_LITERAL.pattern,
                                              protect_literal, text), references, literal_sources)
            text = html.unescape(text).strip()
            text = re.sub(r"`\0(\d+)\0`", lambda m: literal_text[int(m.group(1))], text).lower()
            # Same Unicode categories as check-docs-anchors.mjs; each space is
            # replaced separately, so an em dash between spaces leaves '--'.
            base = "".join(c for c in text if c in " _-" or
                           unicodedata.category(c)[0] in "LNM").replace(" ", "-")
            slug = base
            n = 0
            while slug in seen:
                n += 1
                slug = f"{base}-{n}"
            seen.add(slug)
            anchors.add(slug)
        # Setext content must be paragraph text, never an indented code block,
        # reference definition, list item, blockquote or thematic break.
        previous = line.strip() if paragraph_line(line, bool(previous)) else ""
    return anchors


def unresolved(root, skips, files=None, routes=False):
    root_real = os.path.realpath(root)
    findings = []
    anchor_cache = {}
    for dirpath, path, rel in _candidates(root, files):
        if any(rel == s or rel.startswith(s.rstrip("/") + "/") for s in skips):
            continue
        try:
            with open(path, encoding="utf-8", errors="replace") as fh:
                lines = fh.read().splitlines()
        except OSError as exc:
            findings.append(f"{rel}:0: UNREADABLE ({exc})")
            continue
        definitions = {i for i, _ in visible_lines(lines, definitions_only=True)}
        for i, line in visible_lines(lines):
            for target in targets_in(line, allow_definition=i in definitions):
                if target.startswith(SKIP_PREFIXES):
                    continue
                bare, _, fragment = target.partition("#")
                bare = unquote(bare.split("?", 1)[0])
                if not bare:
                    cand = path
                elif bare.startswith("/"):
                    # `routes`: en un sitio con enrutado propio (Starlight, Astro) un
                    # destino absoluto es una RUTA que resuelve en el build, no un
                    # fichero. Medido: 7 670 de los 8 854 hallazgos del árbol son de esa
                    # forma. Sin este modo el gate es inservible sobre el 87 % de lo que
                    # ve; con él, los relativos del MISMO fichero se siguen exigiendo.
                    if routes:
                        continue
                    cand = os.path.join(root, bare.lstrip("/"))
                else:
                    cand = os.path.join(dirpath, bare)
                if not os.path.exists(cand):
                    findings.append(f"{rel}:{i}: {target}")
                    continue
                # containment: the resolved target (symlinks included) must live
                # INSIDE the scanned tree — ../RELEASE-VERSION exists in the full source tree
                # but not in the export a visitor holds.
                cand_real = os.path.realpath(cand)
                if os.path.commonpath([root_real, cand_real]) != root_real:
                    findings.append(f"{rel}:{i}: {target} [escapes the scanned tree]")
                    continue
                if fragment and cand.endswith(MD_EXT):
                    if cand_real not in anchor_cache:
                        try:
                            with open(cand, encoding="utf-8", errors="replace") as fh:
                                anchor_cache[cand_real] = page_anchors(fh.read().splitlines())
                        except OSError as exc:
                            findings.append(f"{rel}:{i}: {target} [unreadable target: {exc}]")
                            continue
                    if unquote(fragment) not in anchor_cache[cand_real]:
                        findings.append(f"{rel}:{i}: {target} [missing anchor]")
    return findings


def self_test():
    failures = []
    with tempfile.TemporaryDirectory() as td:
        td_real = os.path.realpath(td)
        outside = tempfile.mkdtemp()
        os.makedirs(os.path.join(td, "docs"))
        os.makedirs(os.path.join(td, "assets"))
        open(os.path.join(td, "assets", "ok.png"), "w").close()
        open(os.path.join(td, "docs", "real.md"), "w").close()
        open(os.path.join(outside, "secret.md"), "w").close()
        os.symlink(os.path.join(outside, "secret.md"), os.path.join(td, "docs", "sneaky.md"))
        with open(os.path.join(td, "README.md"), "w", encoding="utf-8") as fh:
            fh.write(
                "[good](docs/real.md) [gooddir](docs/) [anchor](#x) [ext](https://x.y) <a id=x></a>\n"
                "![img](assets/ok.png)\n"
                '<img src="assets/ok.png"> <source srcset="assets/ok.png 2x, assets/ok.png">\n'
                "[rooted](/docs/real.md)\n"
                '<img src="data:image/png;base64,AAAA,BBBB">\n'
                "```\n[fenced](nowhere/at-all.md)\n```\n"
                "~~~\n[tilde-fenced](nowhere/either.md)\n~~~\n"
                "````\n```\n[inner-not-a-close](still/fenced.md)\n````\n"
                "[dead](gone/dead-script.sh)\n\n"
                "[deadimg]: gone/definition.png\n"
                '<IMG SRC=gone/upper.png>\n'
                "<img src = 'gone/spaced.png'>\n"
                "<source srcset=gone/one.png,gone/two.png>\n\n"
                "[deadrooted](/gone/rooted.md)\n"
                "[escape](../%s)\n"
                "[sneaky-link](docs/sneaky.md)\n" % os.path.basename(outside)
            )
        # Repository anchors must resolve in the page they target (#575).
        with tempfile.TemporaryDirectory() as anchors:
            with open(os.path.join(anchors, "LICENSING.md"), "w", encoding="utf-8") as fh:
                fh.write("## What is open, what is commercial, what is planned — by area\n"
                         "[bad](#what-is-open-what-is-commercial-what-is-planned-by-area)\n"
                         "[good](#what-is-open-what-is-commercial-what-is-planned--by-area)\n")
            with open(os.path.join(anchors, "README.md"), "w", encoding="utf-8") as fh:
                fh.write("## Install\n")
            with open(os.path.join(anchors, "guide.md"), "w", encoding="utf-8") as fh:
                fh.write("[bad](README.md#quickstart)\n[good](README.md#install)\n")
            anchor_findings = unresolved(anchors, [])
            expected = {
                "LICENSING.md:2: #what-is-open-what-is-commercial-what-is-planned-by-area [missing anchor]",
                "guide.md:1: README.md#quickstart [missing anchor]",
            }
            if set(anchor_findings) != expected:
                failures.append(f"anchor fixtures: expected {expected}, got {anchor_findings}")
            if main([anchors]) != 1:
                failures.append("missing anchors must make the CLI exit 1")

            forms_fixture = os.path.join(os.path.dirname(os.path.realpath(__file__)),
                                         "testdata", "md-links", "anchor-forms.txt")
            with open(forms_fixture, encoding="utf-8") as fixture, \
                    open(os.path.join(anchors, "forms.md"), "w", encoding="utf-8") as fh:
                fh.write(fixture.read())
            form_findings = unresolved(anchors, [], files=["forms.md"])
            bad_fragments = {"#commented", "#fenced", "#tilde-fenced", "#fake", "#field"}
            if len(form_findings) != 5 or not all(
                    any(f": {fragment} [missing anchor]" in f for f in form_findings)
                    for fragment in bad_fragments):
                failures.append(f"anchor syntax fixtures: {form_findings}")
            # Every heading has a live fragment and a plausible but nonexistent one.
            with open(os.path.join(anchors, "reference.md"), "w", encoding="utf-8") as fh:
                fh.write("# Reference\n")
            with open(os.path.join(anchors, "reference space.md"), "w", encoding="utf-8") as fh:
                fh.write("# Reference\n")
            heading_cases = (
                (r"\_literal\_", "_literal_", "literal"),
                (r"\_literal\_ and _emphasis_", "_literal_-and-emphasis", "literal-and-emphasis"),
                (r"\__literal__", "_literal_", "literal"),
                (r"\`_literal_\`", "literal", "_literal_"),
                (r"\[literal](reference.md)", "literalreferencemd", "literal"),
                (r"\<span>", "span", "span-missing"),
                (r"\&amp; and &amp;", "amp-and-", "-and-"),
                (r"\\_emphasis_", "emphasis", "_emphasis_"),
                ("`&amp;`", "amp", "amp-missing"),
                ("`&#95;` and &#95;", "95-and-_", "_-and-_"),
                ("`&amp;` and &amp;", "amp-and-", "-and-"),
                ("Use ` x ` now", "use-x-now", "use--x--now"),
                ("Use `  x  ` now", "use--x--now", "use-x-now"),
                ("Use `   ` now", "use-----now", "use-now"),
                ("Use ` x` now", "use--x-now", "use-x-now"),
                ("Use `x ` now", "use-x--now", "use-x-now"),
                ("`  x  `", "-x-", "x"),
                (r"`\_literal\_`", "_literal_", "literal"),
                ("`<!--`", "--", "lt--"),
                (r"\<!--literal-->", "--literal--", "literal"),
                (r"\<!--", "--", "lt--"),
                (r"Use \`<!--hidden-->\` now", "use--now", "use---hidden---now"),
                ("foo_bar_baz", "foo_bar_baz", "foobarbaz"),
                ("foo__bar__baz", "foo__bar__baz", "foobarbaz"),
                ("olivares_agent_identity_binding (Resource)",
                 "olivares_agent_identity_binding-resource", "olivaresagentidentity_binding-resource"),
                ("_foo_bar_baz_", "foo_bar_baz", "_foo_bar_baz_"),
                ("__foo_bar_baz__", "foo_bar_baz", "__foo_bar_baz__"),
                ("(_foo_) and __bar__", "foo-and-bar", "_foo_-and-__bar__"),
                ("`<span>`", "span", "span-missing"),
                ("Activation: `olivares enterprise enable <preset>` (buying turns something ON)",
                 "activation-olivares-enterprise-enable-preset-buying-turns-something-on",
                 "activation-olivares-enterprise-enable--buying-turns-something-on"),
                ("<span>Actual HTML</span> and `<span>`", "actual-html-and-span", "actual-html-and-"),
                ("`[literal](missing.md)`", "literalmissingmd", "literal"),
                ("`_literal_` and _emphasis_", "_literal_-and-emphasis", "literal-and-emphasis"),
                ("[API `v1`](reference.md)", "api-v1", "api-v1referencemd"),
                ("[`API`][ref]", "api", "apiref"),
                ("![`API`](reference.md)", "api", "apireferencemd"),
                ("_API `v1`_", "api-v1", "_api-v1_"),
                ("___foo__", "_foo", "___foo__"),
                ("__foo___", "foo_", "__foo___"),
                ("___foo__ bar_", "foo-bar", "_foo-bar_"),
                ("`a`_foo_", "afoo", "a_foo_"),
                ("_foo_`a`", "fooa", "_foo_a"),
                ("_a!___?b__", "ab", "a_b"),
                ("__a!___?b_", "ab", "a_b"),
                ("*foo*_bar_", "foobar", "foo_bar_"),
                ("**foo**_bar_", "foobar", "foo_bar_"),
                ("[foo](reference.md)_bar_", "foobar", "foo_bar_"),
                ("_foo_<span>bar</span>", "foobar", "_foo_bar"),
                ("_foo_[bar](reference.md)", "foobar", "_foobar"),
                ("_foo <span title='x_'>bar</span>", "_foo-bar", "foo-bar"),
                ("<span title='_x'>foo</span> bar_", "foo-bar_", "foo-bar"),
                ("_foo [bar](reference.md?x=x_)", "_foo-bar", "foo-bar"),
                ("[foo](reference.md?x=_target) bar_", "foo-bar_", "foo-bar"),
                ("[ foo ](reference.md)", "foo", "-foo-"),
                ("`` x ```", "-x-", "x"),
                ("Use ``` x `` now", "use--x--now", "use-x-now"),
                ("Use ` x `` now", "use--x--now", "use-x-now"),
                ("Use `` x ` now", "use--x--now", "use-x-now"),
                ("Use `` x `` now", "use-x-now", "use--x--now"),
                ("Use `` x ``` y `` now", "use-x--y-now", "use--x--y--now"),
                (r"Use \`` x ` now", "use-x-now", "use--x--now"),
                ("Use `` x ``` and ` y ` now", "use--x--and-y-now", "use-x-and-y-now"),
            )
            for heading, live, missing in heading_cases:
                for style in (f"# {heading}\n", f"{heading}\n===\n"):
                    definitions = "[ref]: #api\n" if "[ref]" in heading else ""
                    if page_anchors((style + definitions).splitlines()) != {live}:
                        failures.append(f"heading slug fixture {style!r}: expected {live!r}")
                    with open(os.path.join(anchors, "heading.md"), "w", encoding="utf-8") as fh:
                        fh.write(style + f"[live](#{live}) [missing](#{missing})\n\n" + definitions)
                    got_heading = unresolved(anchors, [], files=["heading.md"])
                    if (len(got_heading) != 1 or
                            not got_heading[0].endswith(f": #{missing} [missing anchor]")):
                        failures.append(f"heading fixture {style!r}: {got_heading}")
            # References retain undefined labels; autolinks retain their visible text.
            reference_cases = (
                ("[Foo][missing]", "", "foomissing", "foo"),
                ("[Foo][ref]", "[REF]: reference.md\n", "foo", "fooref"),
                ("[Foo][ref]", "[ref]:reference.md 'title'\n", "foo", "fooref"),
                ("[Foo][ref]", "[ref]: reference.md (title)\n", "foo", "fooref"),
                ("[Foo][ref]", "[ref]: <reference space.md>\n", "foo", "fooref"),
                (r"[Foo][ref\!]", "[ref\\!]: reference.md\n", "foo", "fooref"),
                ("[Foo][ref!]", "[ref\\!]: reference.md\n", "fooref", "foo"),
                ("[Foo][two  words]", "[Two Words]: reference.md\n", "foo", "footwo--words"),
                ("[Foo][ẞ]", "[ss]: reference.md\n", "foo", "fooß"),
                ("![Foo][ref]", "[ref]: reference.md\n", "foo", "fooref"),
                ("[Foo][ref]", "```\n[ref]: reference.md\n```\n", "fooref", "foo"),
                ("[Foo][ref]", "<!--\n[ref]: reference.md\n-->\n", "fooref", "foo"),
                ("[Foo][ref]", "<div>\n[ref]: reference.md\n</div>\n\n", "fooref", "foo"),
                ("[Foo][ref]", "    [ref]: reference.md\n", "fooref", "foo"),
                ("[Foo][ref]", "Paragraph\n[ref]: reference.md\n", "fooref", "foo"),
                ("[Foo][ref]", "[ref]: https://example.com/foo(bar\n", "fooref", "foo"),
                ("[Foo][ref]", "[ref]: <unclosed\n", "fooref", "foo"),
                ("[Foo][ref]", "[ref]: reference.md)\n", "fooref", "foo"),
                ("[Foo][ref]", "[ref]: <bad<target>\n", "fooref", "foo"),
                ("[Foo][ref]", '[ref]: reference.md "unclosed\n', "fooref", "foo"),
                ("[Foo][ref]", "[ref]: reference.md (nested(title))\n", "fooref", "foo"),
                ("[Foo][ref]", '[ref]: reference.md "title" extra\n', "fooref", "foo"),
                ("[Foo][ref]", '[ref]: reference.md "a\\"b"\n', "foo", "fooref"),
                ("[Foo][ref]", "[ref]: reference.md 'a\\'b'\n", "foo", "fooref"),
                ("[Foo][ref]", "[ref]: reference.md (a\\)b)\n", "foo", "fooref"),
                ("[Foo][ref]", "[ref]: reference.md (a\\(b)\n", "foo", "fooref"),
                ("[Foo][ref]", "[ref]: https://example.com/foo(bar)\n", "foo", "fooref"),
                ("[Foo][ref]", "[ref]: https://example.com/foo\\(bar\n", "foo", "fooref"),
                ("[Foo][ref]", "> [ref]: reference.md\n", "foo", "fooref"),
                ("[Foo][ref]", "- [ref]: reference.md\n", "foo", "fooref"),
                ("[Foo][ref]", "1. [ref]: reference.md\n", "foo", "fooref"),
                ("[Foo][ref]", "> - [ref]: reference.md\n", "foo", "fooref"),
                ("[Foo][ref]", "- Item\n\n  [ref]: reference.md\n", "foo", "fooref"),
                ("[Foo][ref]", "> ```\n> [ref]: reference.md\n> ```\n", "fooref", "foo"),
                ("[Foo][ref]", "- ```\n  [ref]: reference.md\n  ```\n", "fooref", "foo"),
                ("[Foo][ref]", ">     [ref]: reference.md\n", "fooref", "foo"),
                ("[Foo][ref]", "-     [ref]: reference.md\n", "fooref", "foo"),
                ("[Foo][ref]", "> <!--\n> [ref]: reference.md\n> -->\n", "fooref", "foo"),
                ("[Foo][ref]", "- <div>\n  [ref]: reference.md\n  </div>\n", "fooref", "foo"),
                ("[Foo][ref]", "Paragraph\n> [ref]: reference.md\n", "foo", "fooref"),
                ("[Foo][ref]", "Paragraph\n- [ref]: reference.md\n", "foo", "fooref"),
                ("[Foo][ref]", "> Paragraph\n> [ref]: reference.md\n", "fooref", "foo"),
                ("[Foo][ref]", "- Paragraph\n  [ref]: reference.md\n", "fooref", "foo"),
                ("[Foo][ref]", "```\n- [ref]: reference.md\n```\n", "fooref", "foo"),
                ("[Foo][ref]", "```\n> [ref]: reference.md\n```\n", "fooref", "foo"),
                ("[Foo][ref]", "<!--\n- [ref]: reference.md\n-->\n", "fooref", "foo"),
                ("[Foo][ref]", "<div>\n- [ref]: reference.md\n</div>\n\n", "fooref", "foo"),
                ("[Foo][ref]", "- Item\n\n  ```\n  > [ref]: reference.md\n  ```\n", "fooref", "foo"),
                ("[Foo][ref]", "- > [ref]: reference.md\n", "foo", "fooref"),
                ("[Foo][ref]", "> - > [ref]: reference.md\n", "foo", "fooref"),
                ("[Foo][ref]", "- > ```\n  > [ref]: reference.md\n  > ```\n", "fooref", "foo"),
                ("[Foo][ref]", "- > <!--\n  > [ref]: reference.md\n  > -->\n", "fooref", "foo"),
                ("[Foo][ref]", "> - Item\n>\n>   [ref]: reference.md\n", "foo", "fooref"),
                ("[Foo][ref]", "1. Item\n2. [ref]: reference.md\n", "foo", "fooref"),
                ("[Foo][ref]", "1) Item\n2) [ref]: reference.md\n", "foo", "fooref"),
                ("[Foo][ref]", "- Item\n-\n  [ref]: reference.md\n", "foo", "fooref"),
                ("[Foo][ref]", "> > Paragraph\n> [ref]: reference.md\n", "fooref", "foo"),
                ("[Foo][ref]", "- > Paragraph\n  [ref]: reference.md\n", "fooref", "foo"),
                ("[Foo][ref]", "[ref]: https://example.com/foo(bar(baz(qux)))\n", "foo", "fooref"),
                ("[Foo][ref]", "[ref]: https://example.com/foo(bar(baz)\n", "fooref", "foo"),
                ("[Foo][ref]", "[ref]: https://example.com/foo<bar\n", "foo", "fooref"),
                ("[Foo][ref]", "[ref]: https://example.com/foo>bar\n", "foo", "fooref"),
                ("[Foo][ref]", '[ref]: reference.md "`code` title"\n', "foo", "fooref"),
                ("[Foo][ref]", "[ref]: <>\n", "foo", "fooref"),
                ("Contact <team@example.com>", "", "contact-teamexamplecom", "contact"),
                ("See <https://example.com>", "", "see-httpsexamplecom", "see"),
                ("See <irc://example.com> <span>chat</span>", "", "see-ircexamplecom-chat", "see--chat"),
                ("Contact <a_b@example.com> _today_", "", "contact-a_bexamplecom-today", "contact--today"),
                ("See <https://example.com/a_b> _today_", "", "see-httpsexamplecoma_b-today", "see--today"),
            )
            for heading, definitions, live, missing in reference_cases:
                for style in (f"# {heading}\n", f"{heading}\n===\n", f"{heading}\n---\n"):
                    text = style + f"[live](#{live}) [missing](#{missing})\n\n" + definitions
                    if page_anchors(text.splitlines()) != {live}:
                        failures.append(f"reference/autolink slug fixture {style!r}")
                    with open(os.path.join(anchors, "references.md"), "w", encoding="utf-8") as fh:
                        fh.write(text)
                    got_references = unresolved(anchors, [], files=["references.md"])
                    if (len(got_references) != 1 or
                            not got_references[0].endswith(f": #{missing} [missing anchor]")):
                        failures.append(f"reference/autolink fixture {style!r}: {got_references}")
            # Thematic breaks and non-interrupting markers do not open lists.
            starts = ("- - -", "* * *", "_ _ _", "- ---", "* ***",
                      "Paragraph\n0. Item", "Paragraph\n2. Item",
                      "Paragraph\n2) Item", "Paragraph\n02. Item",
                      "Paragraph\n-", "Paragraph\n*", "Paragraph\n+",
                      "Paragraph\n1.", "Paragraph\n1)", "Paragraph\n١. Item")
            for start in starts:
                for heading in ("# Live\n", "Live\n===\n", "Live\n---\n"):
                    boundary = "\n" if start.startswith("Paragraph") and not heading.startswith("#") else ""
                    text = start + "\n" + boundary + "".join("  " + part + "\n" for part in
                                                                  heading.splitlines())
                    text += "\n[live](#live) [missing](#ghost)\n"
                    with open(os.path.join(anchors, "list-start.md"), "w", encoding="utf-8") as fh:
                        fh.write(text)
                    got_start = unresolved(anchors, [], files=["list-start.md"])
                    expected_start = [f"list-start.md:{len(text.splitlines())}: #ghost [missing anchor]"]
                    expected_anchors = {"live", "paragraph"} if start == "Paragraph\n-" else {"live"}
                    if page_anchors(text.splitlines()) != expected_anchors or got_start != expected_start:
                        failures.append(f"false list container {start!r}/{heading!r}: {got_start}")
            # A lazy continuation remains paragraph text, including definition syntax.
            for prefix in ("", "> ", "- ", "* ", "+ ", "1. ", "1) ", "> - ", "- > ", "> > "):
                for continuation in ("[ref]: missing.md", "continued\n[ref]: missing.md",
                                     "    continued\n[ref]: missing.md", "\tcontinued\n[ref]: missing.md"):
                    for heading in ("# [Foo][ref]\n", "[Foo][ref]\n===\n", "[Foo][ref]\n---\n"):
                        text = (prefix + "Paragraph\n" + continuation + "\n\n" + heading +
                                "[live](#fooref) [missing](#foo)\n")
                        with open(os.path.join(anchors, "lazy.md"), "w", encoding="utf-8") as fh:
                            fh.write(text)
                        got_lazy = unresolved(anchors, [], files=["lazy.md"])
                        expected_lazy = [f"lazy.md:{len(text.splitlines())}: #foo [missing anchor]"]
                        if page_anchors(text.splitlines()) != {"fooref"} or got_lazy != expected_lazy:
                            failures.append(f"lazy paragraph {prefix!r}/{heading!r}: {got_lazy}")
                # A blank line closes the paragraph; the next definition is real.
                text = prefix + "Paragraph\n\n[ref]: missing.md\n\n# [Foo][ref]\n"
                with open(os.path.join(anchors, "lazy.md"), "w", encoding="utf-8") as fh:
                    fh.write(text)
                if (page_anchors(text.splitlines()) != {"foo"} or
                        unresolved(anchors, [], files=["lazy.md"]) != ["lazy.md:3: missing.md"]):
                    failures.append(f"definition after lazy paragraph boundary {prefix!r}")
            # Genuine list starts keep nested headings outside this scanner's scope.
            for start in ("- Item", "* Item", "+ Item", "0. Item", "2) Item", "-", "1.",
                          "Paragraph\n- Item", "Paragraph\n1. Item", "Paragraph\n1) Item"):
                indent = "   " if start.splitlines()[-1][0].isdigit() else "  "
                text = start + "\n" + indent + "# Ghost\n\n# Live\n[live](#live) [missing](#ghost)\n"
                if page_anchors(text.splitlines()) != {"live"}:
                    failures.append(f"genuine list container {start!r}")
            # Raw HTML never creates Markdown headings; its attributes still resolve.
            html_blocks = (
                ("<div id=custom>", "</div>", "\n"),
                ("<DIV>", "</DIV>", "\n"),
                ("<details>", "</details>", "\n"),
                ("</table>", "", "\n"),
                ("<span>", "</span>", "\n"),
                ("<pre>", "</pre>", ""),
                ("<SCRIPT>", "</SCRIPT>", ""),
                ("<style>", "</style>", ""),
                ("<?instruction", "?>", ""),
                ("<!DOCTYPE", ">", ""),
                ("<![CDATA[", "]]>", ""),
                ("<!--", "-->", ""),
            )
            for opening, closing, boundary in html_blocks:
                for ghost in ("# Ghost\n", "Ghost\n===\n", "Ghost\n---\n"):
                    text = (opening + "\n" + ghost + closing + "\n" + boundary +
                            "# Adjacent\n\n<a id=live-html href=reference.md></a>\n\n"
                            "[live](#adjacent) [html](#live-html) [missing](#ghost)\n")
                    expected_anchors = {"adjacent", "live-html"}
                    if "id=custom" in opening:
                        expected_anchors.add("custom")
                    if page_anchors(text.splitlines()) != expected_anchors:
                        failures.append(f"raw HTML heading fixture {opening!r}/{ghost!r}")
                    with open(os.path.join(anchors, "raw-html.md"), "w", encoding="utf-8") as fh:
                        fh.write(text)
                    got_raw = unresolved(anchors, [], files=["raw-html.md"])
                    if len(got_raw) != 1 or not got_raw[0].endswith(": #ghost [missing anchor]"):
                        failures.append(f"raw HTML link fixture {opening!r}/{ghost!r}: {got_raw}")
            # Opaque HTML bodies cannot create attributes or leak comment state.
            for opening, closing in (("<![CDATA[", "]]>"), ("<?instruction", "?>"),
                                     ("<!DOCTYPE", ">")):
                for body in ('<a id=ghost href=missing.md></a>', "<!--",
                             '- <a id=ghost href=missing.md></a>',
                             '> <a id=ghost href=missing.md></a>'):
                    for text in (opening + "\n" + body + "\n" + closing + "\n",
                                 opening + " " + body + " " + closing + "\n"):
                        text += ("\n# Adjacent\n\n<a id=live href=reference.md></a>\n\n"
                                 "[live](#live) [heading](#adjacent) [ghost](#ghost)\n"
                                 "[broken](missing.md)\n")
                        if page_anchors(text.splitlines()) != {"adjacent", "live"}:
                            failures.append(f"opaque HTML anchors {opening!r}/{body!r}")
                        with open(os.path.join(anchors, "opaque.md"), "w", encoding="utf-8") as fh:
                            fh.write(text)
                        got_opaque = unresolved(anchors, [], files=["opaque.md"])
                        expected_opaque = [
                            f"opaque.md:{len(text.splitlines()) - 1}: #ghost [missing anchor]",
                            f"opaque.md:{len(text.splitlines())}: missing.md",
                        ]
                        if got_opaque != expected_opaque:
                            failures.append(f"opaque HTML boundary {opening!r}/{body!r}: {got_opaque}")
            # Definition validity also controls paragraph eligibility and path checks.
            for definition, valid in (('[ref]: reference.md "a\\"b"', True),
                                      ("[ref]: reference.md 'a\\'b'", True),
                                      ("[ref]: reference.md (a\\)b)", True),
                                      ("[ref]: <unclosed", False),
                                      ("[ref]: https://example.com/foo(bar", False)):
                text = definition + "\n===\n"
                if bool(page_anchors(text.splitlines())) == valid:
                    failures.append(f"reference paragraph eligibility {definition!r}")
            for prefix in ("", "> ", "- ", "1. ", "> - "):
                for target, title in (("missing.md", '"a\\"b"'),
                                      ("missing.md", "'a\\'b'"),
                                      ("missing.md", "(a\\)b)"),
                                      ("<missing space.md>", '"title"')):
                    with open(os.path.join(anchors, "definition-path.md"), "w", encoding="utf-8") as fh:
                        fh.write(f"{prefix}[ref]: {target} {title}\n")
                    expected_target = target.strip("<>")
                    got_definition = unresolved(anchors, [], files=["definition-path.md"])
                    if got_definition != [f"definition-path.md:1: {expected_target}"]:
                        failures.append(f"reference path fixture {prefix!r}/{title!r}: {got_definition}")
            for prefix, expected in (("", set()), ("Paragraph\n", {"real"}),
                                     ("Paragraph\n[ref]: reference.md\n", {"real"})):
                text = prefix + "<span>\n# Real\n"
                if page_anchors(text.splitlines()) != expected:
                    failures.append(f"type-7 HTML paragraph boundary fixture {prefix!r}")
            text = '<div>\n```\n<a id=raw href=missing.md></a>\n</div>\n\n# Real\n'
            if page_anchors(text.splitlines()) != {"raw", "real"}:
                failures.append("HTML block fence text hid real anchors or following headings")
            with open(os.path.join(anchors, "raw-attributes.md"), "w", encoding="utf-8") as fh:
                fh.write(text)
            if unresolved(anchors, [], files=["raw-attributes.md"]) != ["raw-attributes.md:3: missing.md"]:
                failures.append("HTML block fence text hid a real href")
            text = ('<div>\n<!--\n<a id=hidden href=missing.md></a>\n-->\n'
                    '`<a id=raw href=reference.md></a>`\n[raw text](missing.md)\n'
                    '</div>\n\n# Real\n[live](#raw) [missing](#hidden)\n')
            with open(os.path.join(anchors, "raw-comments.md"), "w", encoding="utf-8") as fh:
                fh.write(text)
            if page_anchors(text.splitlines()) != {"raw", "real"}:
                failures.append("raw HTML comments or backticks changed real anchor visibility")
            if unresolved(anchors, [], files=["raw-comments.md"]) != [
                    "raw-comments.md:10: #hidden [missing anchor]"]:
                failures.append("raw HTML text/comments counted as links or hid real attributes")
            # Escaped HTML is text; paired backslashes leave a real opening tag.
            for tag in ('<a id="custom"></a>', "<A NAME='custom'></A>",
                        '<span ID=custom></span>'):
                for prefix, suffix, live in (("", "", True), ("\\", "", False),
                                             ("\\\\", "", True), ("\\\\\\", "", False),
                                             ("`", "`", False), ("\\`", "\\`", True)):
                    with open(os.path.join(anchors, "html.md"), "w", encoding="utf-8") as fh:
                        fh.write(f"# Real\n{prefix}{tag}{suffix}\n[anchor](#custom)\n")
                    got_html = unresolved(anchors, [], files=["html.md"])
                    expected_html = [] if live else ["html.md:3: #custom [missing anchor]"]
                    if got_html != expected_html:
                        failures.append(f"HTML escape fixture {prefix + tag!r}: {got_html}")
            # Apply the same literal boundary to href, src and srcset siblings.
            for tag in ('<a href="missing.md">link</a>', '<img src="missing.md">',
                        '<source srcset="missing.md 1x">'):
                for prefix, suffix, live in (("", "", True), ("\\", "", False),
                                             ("\\\\", "", True), ("\\\\\\", "", False),
                                             ("`", "`", False), ("\\`", "\\`", True)):
                    with open(os.path.join(anchors, "html-link.md"), "w", encoding="utf-8") as fh:
                        fh.write(f"# Real\n{prefix}{tag}{suffix}\n")
                    got_html_link = unresolved(anchors, [], files=["html-link.md"])
                    expected_link = ["html-link.md:2: missing.md"] if live else []
                    if got_html_link != expected_link:
                        failures.append(f"HTML link escape fixture {prefix + tag!r}: {got_html_link}")
            # Unmatched runs are prose: HTML, comments and links remain active.
            for opening, closing in ((1, 2), (2, 1), (2, 3), (3, 2), (2, 2)):
                prefix, suffix = "`" * opening, "`" * closing
                is_code = opening == closing
                with open(os.path.join(anchors, "runs.md"), "w", encoding="utf-8") as fh:
                    fh.write(f'Example {prefix} <a id="custom"></a> {suffix}\n'
                             "[anchor](#custom)\n")
                got_runs = unresolved(anchors, [], files=["runs.md"])
                expected_runs = ["runs.md:2: #custom [missing anchor]"] if is_code else []
                if got_runs != expected_runs:
                    failures.append(f"backtick HTML anchor fixture {opening}/{closing}: {got_runs}")
                for content in ('<a href="missing.md">link</a>', '<img src="missing.md">',
                                '<source srcset="missing.md 1x">', '[link](missing.md)'):
                    with open(os.path.join(anchors, "runs.md"), "w", encoding="utf-8") as fh:
                        fh.write(f"Example {prefix} {content} {suffix}\n")
                    got_runs = unresolved(anchors, [], files=["runs.md"])
                    expected_runs = [] if is_code else ["runs.md:1: missing.md"]
                    if got_runs != expected_runs:
                        failures.append(f"backtick link fixture {opening}/{closing}: {got_runs}")
                with open(os.path.join(anchors, "runs.md"), "w", encoding="utf-8") as fh:
                    fh.write(f"Example {prefix} <!-- {suffix}\n[link](missing.md)\n-->\n")
                got_runs = unresolved(anchors, [], files=["runs.md"])
                expected_runs = ["runs.md:2: missing.md"] if is_code else []
                if got_runs != expected_runs:
                    failures.append(f"backtick comment fixture {opening}/{closing}: {got_runs}")
            # Only paragraph text can become a setext heading.
            for block, missing in (("    Ghost", "ghost"), ("\tGhost", "ghost"),
                                   ("[ref]: README.md", "ref-readmemd"),
                                   ('  [ref]: README.md "title"', "ref-readmemd-title"),
                                   ("> Ghost", "ghost"), ("- Ghost", "--ghost"),
                                   ("1. Ghost", "1-ghost"), ("***", "ghost"), ("_ _ _", "_-_-_")):
                for underline in ("===", "---"):
                    text = (f"# Real\n{block}\n{underline}\n\n"
                            f"   Adjacent\n{underline}\n"
                            f"[live](#adjacent) [missing](#{missing})\n")
                    if page_anchors(text.splitlines()) != {"real", "adjacent"}:
                        failures.append(f"nonparagraph heading fixture {block!r}")
                    with open(os.path.join(anchors, "paragraph.md"), "w", encoding="utf-8") as fh:
                        fh.write(text)
                    got_paragraph = unresolved(anchors, [], files=["paragraph.md"])
                    expected_paragraph = [f"paragraph.md:7: #{missing} [missing anchor]"]
                    if got_paragraph != expected_paragraph:
                        failures.append(f"setext paragraph fixture {block!r}: {got_paragraph}")
            # Closing hashes are ATX syntax, but literal content in setext headings.
            for suffix in (" #", " ###", " ### \t"):
                for style, live, missing in ((f"# Foo{suffix}\n", "foo", "foo-"),
                                             (f"Foo{suffix}\n===\n", "foo-", "foo"),
                                             (f"Foo{suffix}\n---\n", "foo-", "foo")):
                    with open(os.path.join(anchors, "suffix.md"), "w", encoding="utf-8") as fh:
                        fh.write(style + f"[live](#{live}) [missing](#{missing})\n")
                    got_suffix = unresolved(anchors, [], files=["suffix.md"])
                    if (len(got_suffix) != 1 or
                            not got_suffix[0].endswith(f": #{missing} [missing anchor]")):
                        failures.append(f"heading suffix fixture {style!r}: {got_suffix}")
            # Setext underlines cannot reach paragraph text across a fenced block.
            for fence in ("```", "~~~", "````", "~~~~"):
                for underline in ("===", "---"):
                    text = (f"Ghost\n{fence}\nexample\n{fence}\n{underline}\n"
                            f"\nLive\n{underline}\n[live](#live) [missing](#ghost)\n")
                    with open(os.path.join(anchors, "blocks.md"), "w", encoding="utf-8") as fh:
                        fh.write(text)
                    got_blocks = unresolved(anchors, [], files=["blocks.md"])
                    if got_blocks != ["blocks.md:9: #ghost [missing anchor]"]:
                        failures.append(f"setext block boundary fixture {fence!r}: {got_blocks}")
            with open(os.path.join(anchors, "escaped-backticks.md"), "w", encoding="utf-8") as fh:
                fh.write("# Use \\`<!--\\`\n# Hidden\n[hidden](missing.md)\n-->\n"
                         "[live](#use-) [missing](#hidden)\n")
            escaped_findings = unresolved(anchors, [], files=["escaped-backticks.md"])
            if escaped_findings != ["escaped-backticks.md:5: #hidden [missing anchor]"]:
                failures.append(f"escaped backticks hid a real comment: {escaped_findings}")
            for prefix in ("```html\n<!--\n```\n", "`<!--`\n", "---\n"):
                with open(os.path.join(anchors, "literal.md"), "w", encoding="utf-8") as fh:
                    fh.write(prefix + "[bad path](missing.md) [bad anchor](README.md#missing)\n")
                literal_findings = unresolved(anchors, [], files=["literal.md"])
                if (len(literal_findings) != 2 or
                        not any(": missing.md" in f for f in literal_findings) or
                        not any(": README.md#missing [missing anchor]" in f for f in literal_findings)):
                    failures.append(f"literal syntax hid real links: {prefix!r}: {literal_findings}")
            # Even a real anchor in a target outside the root cannot pass containment.
            with open(os.path.join(outside, "secret.md"), "w", encoding="utf-8") as fh:
                fh.write("## Secret\n")
            os.symlink(os.path.join(outside, "secret.md"), os.path.join(anchors, "escape.md"))
            with open(os.path.join(anchors, "escape-link.md"), "w", encoding="utf-8") as fh:
                fh.write("[escape](escape.md#secret)\n")
            escapes = unresolved(anchors, [], files=["escape-link.md"])
            if escapes != ["escape-link.md:1: escape.md#secret [escapes the scanned tree]"]:
                failures.append(f"anchor containment fixture: {escapes}")

        got = unresolved(td, [])
        want = ["gone/dead-script.sh", "gone/definition.png", "gone/upper.png",
                "gone/spaced.png", "gone/one.png", "gone/two.png", "/gone/rooted.md",
                "escapes the scanned tree", "sneaky.md"]
        for t in want:
            if not any(t in f for f in got):
                failures.append(f"red fixture NOT caught: {t}")
        # 7 dead refs + ../escape + symlink escape = 9 findings exactly (nothing green flagged)
        if len(got) != 9:
            failures.append(f"expected exactly 9 findings, got {len(got)}: {got}")
        os.makedirs(os.path.join(td, "routes"))
        with open(os.path.join(td, "routes", "page.md"), "w") as fh:
            fh.write("[route](/reference/modules/overview/)\n")
        if unresolved(td, ["routes"]) != got:
            failures.append("--skip did not exclude the skipped subtree")

        # ── CASO · el CÓDIGO DE SALIDA tiene que poder ser 1 ──────────────────────────────
        #    Sin esto lo demás es decoración: un gate que no puede enrojecer certifica.
        if main([td]) != 1:
            failures.append("main() returned 0 despite findings")
        limpio = tempfile.mkdtemp()
        with open(os.path.join(limpio, "ok.md"), "w", encoding="utf-8") as fh:
            fh.write("[ext](https://x.y) y nada mas\n")
        if main([limpio]) != 0:
            failures.append("main() returned nonzero on a clean tree")

        # ── CASO · un ATRIBUTO fuera de una etiqueta NO es un enlace ──────────────────────
        #    `web/src=0` en prosa producia un hallazgo con destino `0`: ~21 en el arbol real.
        with open(os.path.join(limpio, "prosa.md"), "w", encoding="utf-8") as fh:
            fh.write("Tus cinco dan `web/src=0` y el href=gone/x.png de esta frase es prosa.\n")
        if main([limpio]) != 0:
            failures.append("src=/href= in prose was counted as a link")
        #    ...y dentro de una etiqueta SIGUE contando, incluso sin comillas ni en minuscula.
        with open(os.path.join(limpio, "etiqueta.md"), "w", encoding="utf-8") as fh:
            fh.write("<IMG SRC=gone/dentro.png>\n")
        if main([limpio]) != 1:
            failures.append("an attribute inside a tag was not counted")
        os.remove(os.path.join(limpio, "etiqueta.md"))

        # ── CASO · --routes: un destino ABSOLUTO es una RUTA de sitio, no un fichero ──────
        with open(os.path.join(limpio, "ruta.md"), "w", encoding="utf-8") as fh:
            fh.write("[r](/reference/modules/overview/)\n[rel](./no-existe.md)\n")
        if main([limpio]) != 1:
            failures.append("without --routes, a dead absolute destination must fail")
        if main(["--routes", limpio]) != 1:
            failures.append("--routes must not hide a dead RELATIVE destination in the same file")
        os.remove(os.path.join(limpio, "ruta.md"))
        with open(os.path.join(limpio, "solo-ruta.md"), "w", encoding="utf-8") as fh:
            fh.write("[r](/reference/modules/overview/)\n")
        if main([limpio]) != 1:
            failures.append("a nonexistent absolute destination should fail without --routes")
        if main(["--routes", limpio]) != 0:
            failures.append("--routes did not ignore an absolute destination")

        # ── CASO · --tracked no ve lo que git no ve ──────────────────────────────────────
        #    Es el acotado del gate: 26 363 markdown bajo .claude/worktrees frente a 4 417
        #    trackeados. Un muerto en un fichero IGNORADO no enrojece; en uno trackeado, si.
        repo = tempfile.mkdtemp()
        for cmd in (["init", "-q"], ["config", "user.email", "t@t"], ["config", "user.name", "t"]):
            subprocess.run(["git", "-C", repo] + cmd, check=True, capture_output=True)
        os.makedirs(os.path.join(repo, "ignorado"))
        with open(os.path.join(repo, ".gitignore"), "w") as fh:
            fh.write("ignorado/\n")
        with open(os.path.join(repo, "ignorado", "muerto.md"), "w") as fh:
            fh.write("[x](no-existe.md)\n")
        with open(os.path.join(repo, "vivo.md"), "w") as fh:
            fh.write("[ok](.gitignore)\n")
        subprocess.run(["git", "-C", repo, "add", "-A"], check=True, capture_output=True)
        subprocess.run(["git", "-C", repo, "commit", "-qm", "x"], check=True, capture_output=True)
        if main([repo]) != 1:
            failures.append("loose-tree mode should detect the dead destination in the ignored directory")
        if main(["--tracked", repo]) != 0:
            failures.append("--tracked failed on an untracked file")
        with open(os.path.join(repo, "roto.md"), "w") as fh:
            fh.write("[x](no-existe.md)\n")
        subprocess.run(["git", "-C", repo, "add", "roto.md"], check=True, capture_output=True)
        if main(["--tracked", repo]) != 1:
            failures.append("--tracked missed a dead destination in a TRACKED file")
    if failures:
        for f in failures:
            print(f"check-md-links self-test FAIL: {f}", file=sys.stderr)
        return 1
    print("check-md-links self-test OK: every dead or escaping reference red, every live one green")
    return 0


def main(argv):
    skips = []
    args = []
    tracked = False
    routes = False
    baseline = None
    it = iter(argv)
    for a in it:
        if a == "--skip":
            skips.append(next(it))
        elif a == "--tracked":
            tracked = True
        elif a == "--routes":
            routes = True
        elif a == "--baseline":
            baseline = next(it)
        elif a == "--self-test":
            return self_test()
        else:
            args.append(a)
    root = args[0] if args else "."
    files = tracked_md(root) if tracked else None
    found = unresolved(root, skips, files=files, routes=routes)
    # ── LÍNEA BASE: un TRINQUETE, no una amnistía ────────────────────────────────────────
    #    Los enlaces muertos que hoy existen son decisiones de CONTENIDO de otros dueños
    #    (capturas del material de lanzamiento que nadie ha hecho, un contrato que se cita y
    #    no existe). Congelarlos en silencio sería el defecto; la línea base los NOMBRA uno a
    #    uno con su razón, el gate rechaza cualquiera que NO esté en ella, y **rechaza también
    #    si la línea base tiene entradas que ya no ocurren**: así sólo puede encoger.
    if baseline:
        try:
            with open(baseline, encoding="utf-8") as fh:
                base = {l.split("\t", 1)[0].strip() for l in fh
                        if l.strip() and not l.startswith("#")}
        except OSError as exc:
            print(f"check-md-links: COULD NOT CHECK: {exc}", file=sys.stderr)
            return 2
        actual = {f.split(": ", 1)[0] for f in found}
        nuevos = [f for f in found if f.split(": ", 1)[0] not in base]
        rancios = sorted(base - actual)
        for f in nuevos:
            print(f"NEW {f}")
        for r in rancios:
            print(f"STALE {r} — no longer occurs; remove it from {baseline}")
        return 1 if (nuevos or rancios) else 0
    for f in found:
        print(f)
    # ⛔ HASTA 2026-08-26 ESTO ERA `return 0` INCONDICIONAL: imprimía los hallazgos y salía 0.
    #    No existía ninguna rama que devolviera no-cero. Yo mismo publiqué «cero enlaces
    #    muertos» leyendo ese 0 sobre una corrida con 8 854 hallazgos impresos delante.
    #    Y el daño real no era la cifra: la fila decía «cablear este guion como fast-lint», y
    #    cableado así habría sido un gate PERMANENTEMENTE VERDE, imposible de enrojecer,
    #    ocupando un puesto en el hook y dando confianza sin dar información.
    return 1 if found else 0


if __name__ == "__main__":
    sys.exit(main(sys.argv[1:]))
