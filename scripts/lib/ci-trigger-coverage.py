#!/usr/bin/env python3
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit
# liability: see DISCLAIMER.md at the repository root.
"""Restricted stdlib inspection of mainline-ci trigger coverage.

CIG2: every main push must be able to start the workflow so the secrets
scanner still runs on documentation-only commits. Classify remains an
internal code-job gate; it is not a substitute for the trigger.

This is not a YAML library and does not import PyYAML. It parses the
block/flow subset GitHub Actions workflows use here and returns unknown
(exit 2) for anchors, aliases, merge keys, tags, tabs, duplicate keys,
and any other unsupported shape. A grep for `paths-ignore:` would miss
quoted keys, flow mappings, and `paths:` filters and could return 0.

Exit: 0 observed coverage · 1 proven filter or secrets/classify coupling
· 2 missing, unreadable, or unsupported.
"""
from __future__ import annotations

import re
import sys

PATH_FILTER_KEYS = frozenset(("paths", "paths-ignore"))
PUSH_KNOWN_KEYS = frozenset(
    (
        "branches",
        "branches-ignore",
        "tags",
        "tags-ignore",
        "paths",
        "paths-ignore",
        "types",
    )
)


class Unsupported(Exception):
    """Shape this reader will not interpret. Maps to exit 2."""

    def __init__(self, message: str, line: int | None = None) -> None:
        self.line = line
        if line is not None:
            message = "%s (line %d)" % (message, line)
        super().__init__(message)


class Loader:
    """Indent-driven mapping/sequence reader with flow nodes and block scalars."""

    def __init__(self, text: str) -> None:
        if "\t" in text.split("\n", 1)[0][:80] and text.startswith("\t"):
            raise Unsupported("tab indentation", 1)
        if text.startswith("\ufeff"):
            text = text[1:]
        self.lines = text.splitlines()
        self.n = len(self.lines)
        self.i = 0
        # Character cursor for flow collections that may span lines.
        self._flow_line = 0
        self._flow_col = 0

    def load(self):
        self._skip_blanks()
        if self.i >= self.n:
            raise Unsupported("empty document", 1)
        if self._raw().lstrip().startswith("%"):
            raise Unsupported("YAML directive", self.i + 1)
        if self._stripped() in ("---", "..."):
            raise Unsupported("document marker", self.i + 1)
        indent = self._indent()
        node = self._parse_block(indent)
        self._skip_blanks()
        if self.i < self.n:
            raise Unsupported(
                "trailing content after the document", self.i + 1
            )
        return node

    def _raw(self) -> str:
        return self.lines[self.i]

    def _stripped(self) -> str:
        return self._raw().strip()

    def _indent(self) -> int:
        line = self._raw()
        n = 0
        for ch in line:
            if ch == "\t":
                raise Unsupported("tab indentation", self.i + 1)
            if ch != " ":
                break
            n += 1
        return n

    def _skip_blanks(self) -> None:
        while self.i < self.n:
            line = self.lines[self.i]
            if not line.strip():
                self.i += 1
                continue
            lead = 0
            for ch in line:
                if ch == "\t":
                    raise Unsupported("tab indentation", self.i + 1)
                if ch != " ":
                    break
                lead += 1
            rest = line[lead:]
            if rest.startswith("#"):
                self.i += 1
                continue
            return

    def _parse_block(self, indent: int):
        self._skip_blanks()
        if self.i >= self.n:
            return None
        if self._indent() < indent:
            return None
        content = self._raw()[self._indent() :]
        if content.startswith("- ") or content == "-":
            return self._parse_seq(indent)
        return self._parse_map(indent)

    def _parse_map(self, indent: int) -> dict:
        out: dict = {}
        while True:
            self._skip_blanks()
            if self.i >= self.n:
                return out
            cur = self._indent()
            if cur < indent:
                return out
            if cur > indent:
                raise Unsupported("inconsistent mapping indent", self.i + 1)
            content = self._raw()[cur:]
            if content.startswith("- ") or content == "-":
                raise Unsupported(
                    "sequence item where a mapping key is required", self.i + 1
                )
            key, rest = self._split_key(content, self.i + 1)
            if key in out:
                raise Unsupported("duplicate key %r" % (key,), self.i + 1)
            self.i += 1
            out[key] = self._value_after_key(indent, rest)
        return out

    def _value_after_key(self, key_indent: int, rest: str | None):
        if rest is None:
            self._skip_blanks()
            if self.i >= self.n:
                return None
            nxt = self._indent()
            if nxt <= key_indent:
                return None
            return self._parse_block(nxt)
        return self._parse_same_line_value(key_indent, rest)

    def _parse_same_line_value(self, key_indent: int, rest: str):
        token = rest.lstrip()
        if not token:
            return self._value_after_key(key_indent, None)
        if token[0] in "&*!":
            raise Unsupported(
                "unsupported YAML indicator %r" % token[0], self.i
            )
        if token.startswith("|") or token.startswith(">"):
            return self._block_scalar(key_indent, token)
        if token[0] == "[":
            return self._flow_from(self.i - 1, self._rest_col(key_indent, rest, token), seq=True)
        if token[0] == "{":
            return self._flow_from(self.i - 1, self._rest_col(key_indent, rest, token), seq=False)
        return self._plain_or_quoted(token, self.i)

    def _rest_col(self, key_indent: int, rest: str, token: str) -> int:
        # Column of token[0] on the previous line (already advanced).
        line = self.lines[self.i - 1]
        idx = line.find(token, key_indent)
        if idx < 0:
            raise Unsupported("internal flow column", self.i)
        return idx

    def _block_scalar(self, key_indent: int, token: str) -> str:
        kind = token[0]
        extra = token[1:]
        chomp = ""
        if extra[:1] in ("+", "-"):
            chomp = extra[0]
            extra = extra[1:]
        extra = extra.lstrip()
        if extra.startswith("#"):
            extra = ""
        if extra:
            raise Unsupported("block scalar header extra %r" % extra, self.i)
        collected: list[str] = []
        content_indent = None
        while self.i < self.n:
            line = self.lines[self.i]
            if not line.strip():
                collected.append("")
                self.i += 1
                continue
            lead = 0
            for ch in line:
                if ch == "\t" and content_indent is None:
                    raise Unsupported("tab indentation", self.i + 1)
                if ch != " " and ch != "\t":
                    break
                if ch == "\t":
                    raise Unsupported("tab indentation", self.i + 1)
                lead += 1
            if lead <= key_indent:
                break
            if content_indent is None:
                content_indent = lead
            if lead < content_indent:
                break
            collected.append(line[content_indent:])
            self.i += 1
        while collected and collected[-1] == "":
            collected.pop()
        text = "\n".join(collected)
        if kind == ">":
            text = re.sub(r"\n+", " ", text).strip()
        if chomp != "+":
            if text:
                text += "\n"
        return text

    def _parse_seq(self, indent: int) -> list:
        out: list = []
        while True:
            self._skip_blanks()
            if self.i >= self.n:
                return out
            cur = self._indent()
            if cur != indent:
                if cur < indent:
                    return out
                raise Unsupported("inconsistent sequence indent", self.i + 1)
            content = self._raw()[cur:]
            if not (content.startswith("- ") or content == "-"):
                return out
            rest = "" if content == "-" else content[2:]
            item_indent = cur + 2
            self.i += 1
            if not rest.strip():
                self._skip_blanks()
                if self.i >= self.n or self._indent() <= cur:
                    out.append(None)
                else:
                    out.append(self._parse_block(self._indent()))
                continue
            token = rest.lstrip()
            # Compact mapping: `- key: value` with more keys at item_indent.
            if self._looks_like_key(token):
                key, after = self._split_key(token, self.i)
                mapping = {key: None}
                if after is None:
                    self._skip_blanks()
                    if self.i < self.n and self._indent() > cur:
                        mapping[key] = self._parse_block(self._indent())
                    else:
                        mapping[key] = None
                else:
                    mapping[key] = self._parse_same_line_value(cur, after)
                # Further keys of this item.
                while True:
                    self._skip_blanks()
                    if self.i >= self.n:
                        break
                    nxt = self._indent()
                    if nxt != item_indent:
                        break
                    more = self._raw()[nxt:]
                    if more.startswith("- ") or more == "-":
                        break
                    k2, r2 = self._split_key(more, self.i + 1)
                    if k2 in mapping:
                        raise Unsupported(
                            "duplicate key %r" % (k2,), self.i + 1
                        )
                    self.i += 1
                    mapping[k2] = self._value_after_key(item_indent, r2)
                out.append(mapping)
                continue
            out.append(self._parse_same_line_value(cur, rest))
        return out

    def _looks_like_key(self, token: str) -> bool:
        if not token:
            return False
        if token[0] in "[{|&*!":
            return False
        if token[0] in ("'", '"'):
            try:
                _, rest = self._read_quoted(token, 1)
            except Unsupported:
                return False
            return rest.lstrip().startswith(":")
        # Plain key then colon. Do not treat a URL-ish scalar as a key
        # unless the colon is a YAML separator (`: ` or `:` at end).
        if re.match(r"[^:#\n]+?:\s", token) or re.match(
            r"[^:#\n]+?:$", token.split("#", 1)[0].rstrip()
        ):
            return True
        return False

    def _split_key(self, content: str, line_no: int):
        content = content.rstrip()
        if not content:
            raise Unsupported("empty mapping line", line_no)
        if content.startswith("<<"):
            raise Unsupported("merge key", line_no)
        if content.startswith("?"):
            raise Unsupported("explicit key", line_no)
        if content[0] in ("'", '"'):
            key, rest = self._read_quoted(content, line_no)
            rest = rest.lstrip()
            if not rest.startswith(":"):
                raise Unsupported("quoted key without colon", line_no)
            rest = rest[1:]
            rest = self._strip_comment(rest)
            if rest == "":
                return key, None
            return key, rest
        m = re.match(r"([^:#\n]+?):(\s.*)?$", content)
        if not m:
            # `key:` with optional comment, no space after colon.
            m2 = re.match(r"([^:#\n]+?):(?:\s*#.*)?$", content)
            if not m2:
                raise Unsupported("unreadable mapping key", line_no)
            return m2.group(1).strip(), None
        key = m.group(1).strip()
        if not key:
            raise Unsupported("empty mapping key", line_no)
        if any(ch in key for ch in "&*![]{}"):
            raise Unsupported("unsupported character in key", line_no)
        rest = m.group(2)
        if rest is None:
            return key, None
        rest = self._strip_comment(rest)
        if rest.strip() == "":
            return key, None
        return key, rest

    def _strip_comment(self, s: str) -> str:
        if " #" in s:
            s = s.split(" #", 1)[0]
        return s.rstrip()

    def _plain_or_quoted(self, token: str, line_no: int):
        token = self._strip_comment(token).strip()
        if not token:
            return None
        if token[0] in ("'", '"'):
            value, rest = self._read_quoted(token, line_no)
            rest = self._strip_comment(rest).strip()
            if rest:
                raise Unsupported(
                    "trailing content after quoted scalar", line_no
                )
            return value
        if token[0] in "&*!":
            raise Unsupported("unsupported YAML indicator", line_no)
        return token

    def _read_quoted(self, text: str, line_no: int):
        q = text[0]
        i = 1
        out = []
        if q == "'":
            while i < len(text):
                ch = text[i]
                if ch == "'":
                    if i + 1 < len(text) and text[i + 1] == "'":
                        out.append("'")
                        i += 2
                        continue
                    return "".join(out), text[i + 1 :]
                out.append(ch)
                i += 1
            raise Unsupported("unclosed single quote", line_no)
        while i < len(text):
            ch = text[i]
            if ch == "\\":
                if i + 1 >= len(text):
                    raise Unsupported("dangling escape", line_no)
                nxt = text[i + 1]
                mapping = {
                    "n": "\n",
                    "t": "\t",
                    "r": "\r",
                    "\\": "\\",
                    '"': '"',
                    "'": "'",
                    "0": "\0",
                }
                if nxt not in mapping:
                    raise Unsupported(
                        "unsupported string escape", line_no
                    )
                out.append(mapping[nxt])
                i += 2
                continue
            if ch == '"':
                return "".join(out), text[i + 1 :]
            out.append(ch)
            i += 1
        raise Unsupported("unclosed double quote", line_no)

    def _flow_from(self, line_idx: int, col: int, seq: bool):
        self._flow_line = line_idx
        self._flow_col = col
        node = self._flow_seq() if seq else self._flow_map()
        # Resume block parse after the flow node. Flow may have consumed
        # extra lines; the remainder of the ending line must be comment/empty.
        line = self.lines[self._flow_line] if self._flow_line < self.n else ""
        rest = line[self._flow_col :]
        rest = self._strip_comment(rest).strip()
        if rest:
            raise Unsupported(
                "trailing content after flow node", self._flow_line + 1
            )
        self.i = self._flow_line + 1
        return node

    def _flow_eof(self) -> bool:
        return self._flow_line >= self.n

    def _flow_ch(self) -> str:
        if self._flow_eof():
            raise Unsupported("unterminated flow collection", self.n)
        line = self.lines[self._flow_line]
        if self._flow_col >= len(line):
            return "\n"
        return line[self._flow_col]

    def _flow_adv(self) -> None:
        if self._flow_eof():
            return
        line = self.lines[self._flow_line]
        if self._flow_col >= len(line):
            self._flow_line += 1
            self._flow_col = 0
            return
        self._flow_col += 1

    def _flow_skip(self) -> None:
        while not self._flow_eof():
            ch = self._flow_ch()
            if ch in " \t\n":
                if ch == "\t" and self._flow_col == 0:
                    raise Unsupported(
                        "tab indentation", self._flow_line + 1
                    )
                self._flow_adv()
                continue
            if ch == "#":
                self._flow_line += 1
                self._flow_col = 0
                continue
            return

    def _flow_seq(self) -> list:
        if self._flow_ch() != "[":
            raise Unsupported("expected '['", self._flow_line + 1)
        self._flow_adv()
        items: list = []
        self._flow_skip()
        if self._flow_ch() == "]":
            self._flow_adv()
            return items
        while True:
            self._flow_skip()
            items.append(self._flow_node())
            self._flow_skip()
            ch = self._flow_ch()
            if ch == ",":
                self._flow_adv()
                self._flow_skip()
                if self._flow_ch() == "]":
                    raise Unsupported(
                        "trailing comma in flow sequence", self._flow_line + 1
                    )
                continue
            if ch == "]":
                self._flow_adv()
                return items
            raise Unsupported("unreadable flow sequence", self._flow_line + 1)

    def _flow_map(self) -> dict:
        if self._flow_ch() != "{":
            raise Unsupported("expected '{'", self._flow_line + 1)
        self._flow_adv()
        out: dict = {}
        self._flow_skip()
        if self._flow_ch() == "}":
            self._flow_adv()
            return out
        while True:
            self._flow_skip()
            key = self._flow_key()
            if key in out:
                raise Unsupported(
                    "duplicate key %r" % (key,), self._flow_line + 1
                )
            self._flow_skip()
            if self._flow_ch() != ":":
                raise Unsupported(
                    "flow mapping key without colon", self._flow_line + 1
                )
            self._flow_adv()
            self._flow_skip()
            out[key] = self._flow_node()
            self._flow_skip()
            ch = self._flow_ch()
            if ch == ",":
                self._flow_adv()
                self._flow_skip()
                if self._flow_ch() == "}":
                    raise Unsupported(
                        "trailing comma in flow mapping", self._flow_line + 1
                    )
                continue
            if ch == "}":
                self._flow_adv()
                return out
            raise Unsupported("unreadable flow mapping", self._flow_line + 1)

    def _flow_key(self) -> str:
        ch = self._flow_ch()
        if ch in ("'", '"'):
            return self._flow_quoted()
        if ch in "[{&*!":
            raise Unsupported("unsupported flow key", self._flow_line + 1)
        return self._flow_plain(stop_at_colon=True)

    def _flow_node(self):
        self._flow_skip()
        ch = self._flow_ch()
        if ch == "[":
            return self._flow_seq()
        if ch == "{":
            return self._flow_map()
        if ch in ("'", '"'):
            return self._flow_quoted()
        if ch in "&*!":
            raise Unsupported(
                "unsupported YAML indicator in flow", self._flow_line + 1
            )
        return self._flow_plain(stop_at_colon=False)

    def _flow_quoted(self) -> str:
        q = self._flow_ch()
        start_line = self._flow_line + 1
        buf = [q]
        self._flow_adv()
        if q == "'":
            while not self._flow_eof():
                ch = self._flow_ch()
                buf.append(ch if ch != "\n" else " ")
                self._flow_adv()
                if ch == "'":
                    if not self._flow_eof() and self._flow_ch() == "'":
                        buf.append("'")
                        self._flow_adv()
                        continue
                    value, rest = self._read_quoted("".join(buf), start_line)
                    if rest.strip():
                        raise Unsupported(
                            "trailing quoted flow content", start_line
                        )
                    return value
            raise Unsupported("unclosed single quote", start_line)
        while not self._flow_eof():
            ch = self._flow_ch()
            if ch == "\n":
                raise Unsupported("multiline double quote", start_line)
            buf.append(ch)
            self._flow_adv()
            if ch == '"' and (len(buf) < 2 or buf[-2] != "\\"):
                # Count backslashes: odd means escaped quote.
                slashes = 0
                k = len(buf) - 2
                while k >= 1 and buf[k] == "\\":
                    slashes += 1
                    k -= 1
                if slashes % 2 == 1:
                    continue
                value, rest = self._read_quoted("".join(buf), start_line)
                if rest.strip():
                    raise Unsupported(
                        "trailing quoted flow content", start_line
                    )
                return value
        raise Unsupported("unclosed double quote", start_line)

    def _flow_plain(self, stop_at_colon: bool) -> str:
        start_line = self._flow_line + 1
        chars: list[str] = []
        while not self._flow_eof():
            ch = self._flow_ch()
            if ch in "[]{},\n#":
                break
            if ch == ":" and stop_at_colon:
                break
            if ch == ":" and not stop_at_colon:
                # Colon ends a flow value when followed by comma/} /] /space.
                nxt_line, nxt_col = self._flow_line, self._flow_col + 1
                nxt = ""
                if nxt_col <= len(self.lines[nxt_line]) if nxt_line < self.n else False:
                    line = self.lines[nxt_line]
                    nxt = line[nxt_col] if nxt_col < len(line) else "\n"
                if nxt in " \t,]}#\n":
                    break
            chars.append(ch)
            self._flow_adv()
        val = "".join(chars).strip()
        if not val:
            raise Unsupported("empty flow scalar", start_line)
        return val


def load_workflow(text: str):
    return Loader(text).load()


def _as_list(value) -> list[str] | None:
    if value is None:
        return []
    if isinstance(value, str):
        return [value]
    if isinstance(value, list):
        out = []
        for item in value:
            if not isinstance(item, str):
                return None
            out.append(item)
        return out
    return None


def _event_map(on_node) -> dict | None:
    """Normalize `on` to a mapping of event name -> spec. None = unknown."""
    if on_node is None:
        return None
    if isinstance(on_node, str):
        return {on_node: None}
    if isinstance(on_node, list):
        out = {}
        for item in on_node:
            if not isinstance(item, str):
                return None
            if item in out:
                return None
            out[item] = None
        return out
    if isinstance(on_node, dict):
        return on_node
    return None


def _branch_allows_main(names: list[str]) -> bool | None:
    """True if main is definitely included; False if definitely not; None unknown.

    An exact main (or * / **) wins even if an unsupported positive glob
    precedes it. A negation stays unknown: exclusion cannot be ruled out.
    """
    saw_exact = False
    saw_unsupported_positive = False
    saw_negation = False
    for name in names:
        if name.startswith("!"):
            saw_negation = True
            continue
        if name in ("main", "refs/heads/main", "*", "**"):
            saw_exact = True
            continue
        if any(ch in name for ch in "*?["):
            saw_unsupported_positive = True
    if saw_negation:
        return None
    if saw_exact:
        return True
    if saw_unsupported_positive:
        return None
    return False


def github_glob(pattern: str, path: str) -> bool:
    """Restricted matcher for the test-facing eligible helper.

    Not a GitHub glob engine. A leading ``!`` is unknown, not a miss.
    """
    if pattern.startswith("!"):
        raise Unsupported("negated path pattern is not inspectable")
    pattern = pattern.replace("\\", "/")
    path = path.replace("\\", "/")
    if pattern.endswith("/"):
        pattern += "**"
    rx = []
    i = 0
    while i < len(pattern):
        if pattern.startswith("**/", i):
            rx.append("(?:.*/)?")
            i += 3
            continue
        if pattern[i : i + 2] == "**":
            rx.append(".*")
            i += 2
            continue
        ch = pattern[i]
        if ch == "*":
            rx.append("[^/]*")
        elif ch == "?":
            rx.append("[^/]")
        else:
            rx.append(re.escape(ch))
        i += 1
    return re.match("^" + "".join(rx) + "$", path) is not None


def push_would_run(push_spec, files: list[str], branch: str = "main") -> bool:
    """GitHub AND of branch and path filters. Raises Unsupported if opaque."""
    if push_spec is None:
        return True
    if isinstance(push_spec, str):
        raise Unsupported("push spec is a scalar, not a mapping or null")
    if not isinstance(push_spec, dict):
        raise Unsupported("push spec is not a mapping")
    branches = push_spec.get("branches")
    if branches is not None:
        names = _as_list(branches)
        if names is None:
            raise Unsupported("push.branches is not a string list")
        allow = _branch_allows_main(names) if branch == "main" else None
        if branch != "main":
            allow = branch in names or "*" in names or "**" in names
        if allow is None:
            raise Unsupported("push.branches glob is not inspectable")
        if not allow:
            return False
    ignored = push_spec.get("branches-ignore")
    if ignored is not None:
        names = _as_list(ignored)
        if names is None:
            raise Unsupported("push.branches-ignore is not a string list")
        if branch in names:
            return False
    # Validate the complete pattern set before short-circuit matching. Otherwise
    # an earlier positive match can hide an unsupported later negation.
    for key in ("paths", "paths-ignore"):
        if key in push_spec:
            patterns = _as_list(push_spec[key])
            if patterns is None:
                raise Unsupported("push.%s is not a string list" % key)
            if any(pattern.startswith("!") for pattern in patterns):
                raise Unsupported("negated path pattern is not inspectable")
    paths = push_spec.get("paths")
    paths_ignore = push_spec.get("paths-ignore")
    if paths is not None:
        pats = _as_list(paths)
        if pats is None:
            raise Unsupported("push.paths is not a string list")
        if not any(any(github_glob(p, f) for p in pats) for f in files):
            return False
    if paths_ignore is not None:
        pats = _as_list(paths_ignore)
        if pats is None:
            raise Unsupported("push.paths-ignore is not a string list")
        if files and all(any(github_glob(p, f) for p in pats) for f in files):
            return False
    return True


def extract_journal_patterns(run_text: str) -> list[str] | None:
    if not isinstance(run_text, str):
        return None
    m = re.search(r'case\s+"\$f"\s+in\b(.*?)(?:esac)', run_text, re.S)
    if not m:
        return None
    body = m.group(1)
    patterns: list[str] = []
    saw_default = False
    for raw_line in body.splitlines():
        line = raw_line.strip()
        if not line or line.startswith("#"):
            continue
        arm = re.match(r"(.+?)\)\s*;;\s*$", line)
        if not arm:
            if re.match(r"\*\)\s*journal_only=false\b", line):
                saw_default = True
            continue
        spec = arm.group(1).strip()
        if spec == "*":
            saw_default = True
            continue
        for part in spec.split("|"):
            p = part.strip()
            if p:
                patterns.append(p)
    if not saw_default:
        return None
    return patterns


def bash_glob(pattern: str, path: str) -> bool:
    """`case` glob: `*` matches any string, including slashes."""
    rx = []
    for ch in pattern:
        if ch == "*":
            rx.append(".*")
        elif ch == "?":
            rx.append(".")
        else:
            rx.append(re.escape(ch))
    return re.match("^" + "".join(rx) + "$", path) is not None


def classify_code(patterns: list[str], files: list[str]) -> bool:
    """True when classify would set code=true (not journal-only)."""
    if not files:
        return True
    for path in files:
        if not any(bash_glob(p, path) for p in patterns):
            return True
    return False


def _job_run_texts(job: dict) -> list[str]:
    steps = job.get("steps")
    if not isinstance(steps, list):
        return []
    texts = []
    for step in steps:
        if not isinstance(step, dict):
            continue
        run = step.get("run")
        if isinstance(run, str):
            texts.append(run)
    return texts


def _needs_names(needs) -> list[str] | None:
    return _as_list(needs)


def _if_mentions_classify(if_val) -> bool:
    if if_val is None:
        return False
    if not isinstance(if_val, str):
        raise Unsupported("job-level if is not a string")
    return bool(re.search(r"(^|[^A-Za-z0-9_])needs\.classify(\.|$|[^A-Za-z0-9_])", if_val))


def inspect_coverage(doc) -> tuple[int, list[str]]:
    """Return (exit_code, messages). 0 requires observed blocks."""
    notes: list[str] = []
    if not isinstance(doc, dict):
        return 2, ["workflow document is not a mapping"]
    events = _event_map(doc.get("on"))
    if events is None or "on" not in doc:
        return 2, ["cannot observe the `on` event map"]
    if "workflow_dispatch" not in events:
        return 1, ["workflow_dispatch is missing from `on`"]
    if "push" not in events:
        return 2, ["cannot observe a push event"]
    push = events["push"]
    if push is not None and not isinstance(push, dict):
        return 2, ["push event spec is not a mapping or null"]
    if isinstance(push, dict):
        for key in push:
            if key in PATH_FILTER_KEYS:
                notes.append("push path filter: %s" % key)
            elif key not in PUSH_KNOWN_KEYS:
                return 2, ["unsupported push key %r" % (key,)]
        branches = push.get("branches")
        if branches is not None:
            names = _as_list(branches)
            if names is None:
                return 2, ["push.branches is not a string list"]
            allow = _branch_allows_main(names)
            if allow is None:
                return 2, ["push.branches glob is not inspectable"]
            if not allow:
                return 1, ["main is not eligible under push.branches"]
        ignored = push.get("branches-ignore")
        if ignored is not None:
            names = _as_list(ignored)
            if names is None:
                return 2, ["push.branches-ignore is not a string list"]
            if "main" in names or "refs/heads/main" in names:
                return 1, ["main is excluded by push.branches-ignore"]
        for key in PATH_FILTER_KEYS:
            if key in push:
                return 1, notes or ["push path filter: %s" % key]
    jobs = doc.get("jobs")
    if not isinstance(jobs, dict):
        return 2, ["cannot observe jobs"]
    classify = jobs.get("classify")
    if not isinstance(classify, dict):
        return 2, ["cannot observe the classify job"]
    runs = _job_run_texts(classify)
    patterns = None
    for text in runs:
        patterns = extract_journal_patterns(text)
        if patterns is not None:
            break
    if patterns is None:
        return 2, ["cannot observe the classify `$f` case"]
    secrets = jobs.get("secrets")
    if not isinstance(secrets, dict):
        return 2, ["cannot observe the secrets job"]
    try:
        needs = _needs_names(secrets.get("needs"))
    except Unsupported as exc:
        return 2, [str(exc)]
    if needs is None:
        return 2, ["secrets.needs is not a string list"]
    if "classify" in needs:
        return 1, ["secrets needs classify"]
    try:
        if _if_mentions_classify(secrets.get("if")):
            return 1, ["secrets job-level if depends on classify"]
    except Unsupported as exc:
        return 2, [str(exc)]
    return 0, [
        "main-push eligible; no path filter; classifier observed (%d journal prefix(es)); secrets independent of classify"
        % len(patterns)
    ]


def inspect_path(path: str) -> tuple[int, list[str]]:
    try:
        with open(path, encoding="utf-8") as fh:
            text = fh.read()
    except OSError as exc:
        return 2, ["cannot read %s: %s" % (path, exc)]
    try:
        doc = load_workflow(text)
    except Unsupported as exc:
        return 2, ["unsupported or unreadable YAML: %s" % exc]
    except Exception as exc:  # noqa: BLE001 — any surprise is unknown, not green
        return 2, ["inspection failed: %s" % exc]
    return inspect_coverage(doc)


def _print(code: int, messages: list[str]) -> int:
    stream = sys.stdout if code == 0 else sys.stderr
    prefix = "check-classify-paths-parity:"
    if code == 0:
        for msg in messages:
            stream.write("%s %s\n" % (prefix, msg))
        return 0
    if code == 1:
        stream.write("%s FINDING — %s\n" % (prefix, "; ".join(messages)))
        return 1
    stream.write("%s NO HE PODIDO MIRAR: %s\n" % (prefix, "; ".join(messages)))
    return 2


def _load_path(path: str):
    try:
        with open(path, encoding="utf-8") as fh:
            text = fh.read()
    except OSError as exc:
        raise Unsupported("cannot read %s: %s" % (path, exc)) from exc
    return load_workflow(text)


def cmd_eligible(path: str, branch: str, files: list[str]) -> int:
    try:
        doc = _load_path(path)
        events = _event_map(doc.get("on"))
        if not events or "push" not in events:
            raise Unsupported("cannot observe a push event")
        ok = push_would_run(events["push"], files, branch=branch)
    except Unsupported as exc:
        sys.stderr.write(
            "check-classify-paths-parity: NO HE PODIDO MIRAR: %s\n" % exc
        )
        return 2
    sys.stdout.write("yes\n" if ok else "no\n")
    return 0


def cmd_classify(path: str, files: list[str]) -> int:
    try:
        doc = _load_path(path)
        jobs = doc.get("jobs")
        if not isinstance(jobs, dict) or not isinstance(jobs.get("classify"), dict):
            raise Unsupported("cannot observe the classify job")
        patterns = None
        for text in _job_run_texts(jobs["classify"]):
            patterns = extract_journal_patterns(text)
            if patterns is not None:
                break
        if patterns is None:
            raise Unsupported("cannot observe the classify `$f` case")
        code = classify_code(patterns, files)
    except Unsupported as exc:
        sys.stderr.write(
            "check-classify-paths-parity: NO HE PODIDO MIRAR: %s\n" % exc
        )
        return 2
    sys.stdout.write("code\n" if code else "journal\n")
    return 0


def main(argv: list[str]) -> int:
    if len(argv) < 2:
        sys.stderr.write(
            "usage: ci-trigger-coverage.py check FILE | eligible FILE BRANCH FILE... | classify FILE FILE...\n"
        )
        return 2
    cmd = argv[1]
    if cmd == "check":
        if len(argv) != 3:
            sys.stderr.write("usage: ci-trigger-coverage.py check FILE\n")
            return 2
        return _print(*inspect_path(argv[2]))
    if cmd == "eligible":
        if len(argv) < 5:
            sys.stderr.write(
                "usage: ci-trigger-coverage.py eligible FILE BRANCH FILE...\n"
            )
            return 2
        return cmd_eligible(argv[2], argv[3], argv[4:])
    if cmd == "classify":
        if len(argv) < 4:
            sys.stderr.write(
                "usage: ci-trigger-coverage.py classify FILE FILE...\n"
            )
            return 2
        return cmd_classify(argv[2], argv[3:])
    sys.stderr.write("unknown command %r\n" % cmd)
    return 2


if __name__ == "__main__":
    raise SystemExit(main(sys.argv))
