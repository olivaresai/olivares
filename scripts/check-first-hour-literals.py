#!/usr/bin/env python3
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
"""Fail a pull request that drops a literal the release first-hour journey reads.

The full journey runs for release candidates and nightly on main
(release-first-hour.yml). This cheap complementary check
parses the literals first-hour.spec.ts reads from accessible names, text and
labels, and looks each one up under web/src and its English locale catalogs.
The spec at the revision being judged is the source of the strings; this file
never lists them.

The spec and the console are read at the one revision given to --head. The
workflow passes the pull request's merge commit, which is what the checkout
holds and what the release journey would run on, so a stale branch cannot pass
on a spec main has since changed. A locator inside a comment is not read by the
journey and is ignored.

A locator that is not a literal is printed as not covered, never skipped: roles
and structure (combobox, radio, row) are proved only by the release-first-hour
journey. The same limit applies to every run, green included. The lookup proves
that the text exists under web/src, not that the first-hour surface renders it:
a literal that survives in another surface still passes, and locators inside
imported helpers (for example web/e2e-release/surface.ts) are not parsed.

`exact: true` counts only a line that shows the literal as a complete value: a
quoted string, a JSX text child, or the literal alone on its line. A longer
label, a prefix or a punctuation suffix is a different accessible name.

Exit 0: at least one literal was parsed and every literal was found. Exit 1: a
literal is missing, the spec has no literals, or the spec is gone. Exit 2: the
repository or revision could not be read.
"""

import argparse
import os
from pathlib import Path
import re
import subprocess
import sys

SPEC = "web/e2e-release/first-hour.spec.ts"
SRC = "web/src"
LIMIT = ("the lookup is by text under web/src, not by surface, so a literal that survives "
         "elsewhere still passes and imported helpers are not parsed; roles and structure "
         "(combobox, radio, row) are proved only by the release-first-hour journey")
# The journey runs in the console's English default (fallbackLng en), so a
# translated catalog cannot stand in for the English copy. Same locale paths as
# check-source-language.py: the displayed copy is not checked there either.
NON_ENGLISH = re.compile(
    r"(?:^|/)(?:i18n|locales)/(?:es|fr|de|it|ru|ja|zh|ko)(?:\.json|/.*\.json)$")
# Tests, fixtures and dev-only demos quote UI copy but render none of it.
TEST_PATH = re.compile(
    r"(?:^|/)(?:__tests__|tests?|fixtures|__fixtures__|mocks|__mocks__|testdata|test-utils|dev|stories)(?:/|$)"
    r"|\.(?:test|spec|stories)\.[^/]+$")
LOCATOR = re.compile(
    r"\b(getByRole|getByText|getByLabel|getByPlaceholder|getByAltText|getByTitle|getByTestId|locator|filter)\s*\(")
OPENERS = "([{"
CLOSERS = ")]}"


# Repo-selecting variables outrank -C and would point git at another repository.
GIT_LOCATION = ("GIT_DIR", "GIT_WORK_TREE", "GIT_INDEX_FILE", "GIT_OBJECT_DIRECTORY",
                "GIT_ALTERNATE_OBJECT_DIRECTORIES", "GIT_COMMON_DIR", "GIT_NAMESPACE",
                "GIT_PREFIX")


def git_call(repo, *args):
    env = {k: v for k, v in os.environ.items() if k not in GIT_LOCATION}
    env["GIT_NO_LAZY_FETCH"] = "1"
    try:
        return subprocess.run(["git", "-C", str(repo), *args], capture_output=True,
                              text=True, errors="replace", env=env)
    except OSError as error:
        return subprocess.CompletedProcess([], 2, "", f"git could not run: {error}")


def path_at(repo, rev, path):
    """True when path exists in rev; raises OSError when the tree cannot be read.

    git cat-file -e returns 128 for both an absent path and an unreadable
    object, so the tree listing is the discriminator: absent is empty, and a
    failure to read is an error, never a verdict about the console.
    """
    done = git_call(repo, "ls-tree", rev, "--", path)
    if done.returncode:
        raise OSError(done.stderr.strip() or f"git ls-tree exited {done.returncode}")
    return bool(done.stdout.strip())


def scan_call(text, start):
    """Return the text inside the '(' at start and the index just past its ')'.

    Quotes, comments, regular expressions and the three bracket pairs are
    skipped, so a multi-line call and an object argument are read whole.
    """
    depth = 0
    index = start
    quote = None
    while index < len(text):
        char = text[index]
        if quote:
            if char == "\\":
                index += 2
                continue
            if char == quote:
                quote = None
        elif char in "'\"`":
            quote = char
        elif char == "/" and index + 1 < len(text) and text[index + 1] in "/*":
            end = text.find("\n" if text[index + 1] == "/" else "*/", index + 2)
            if end < 0:
                return None, None
            index = end + (1 if text[index + 1] == "/" else 2)
            continue
        elif char == "/" and _regex_may_start(text, index):
            index = _skip_regex(text, index)
            continue
        elif char in OPENERS:
            depth += 1
        elif char in CLOSERS:
            depth -= 1
            if depth == 0:
                return text[start + 1:index], index + 1
        index += 1
    return None, None


def _regex_may_start(text, index):
    """A '/' is a regex literal after an operator or an opening bracket."""
    before = text[:index].rstrip()
    return not before or before[-1] in "(,=:[!&|?{};"


def _skip_regex(text, start):
    index = start + 1
    while index < len(text):
        if text[index] == "\\":
            index += 2
            continue
        if text[index] == "\n":
            return index
        if text[index] == "/":
            return index + 1
        index += 1
    return index


def strip_comments(text):
    """Blank out // and /* */ comments outside strings, templates and regexes.

    A commented-out locator is not read by the journey, so it must not require
    a literal. Positions are preserved (comment characters become spaces,
    newlines stay) so the line numbers are still the file's own.
    """
    out = list(text)
    index = 0
    quote = None
    while index < len(text):
        char = text[index]
        if quote:
            if char == "\\":
                index += 2
                continue
            if char == quote:
                quote = None
            index += 1
            continue
        if char in "'\"`":
            quote = char
            index += 1
            continue
        if char == "/" and index + 1 < len(text) and text[index + 1] == "/":
            end = text.find("\n", index)
            end = len(text) if end < 0 else end
            for position in range(index, end):
                out[position] = " "
            index = end
            continue
        if char == "/" and index + 1 < len(text) and text[index + 1] == "*":
            end = text.find("*/", index + 2)
            end = len(text) if end < 0 else end + 2
            for position in range(index, end):
                if out[position] != "\n":
                    out[position] = " "
            index = end
            continue
        if char == "/" and _regex_may_start(text, index):
            index = _skip_regex(text, index)
            continue
        index += 1
    return "".join(out)


def split_top_level(text):
    """Split on commas outside strings, regular expressions and brackets."""
    parts = []
    start = 0
    depth = 0
    index = 0
    quote = None
    while index < len(text):
        char = text[index]
        if quote:
            if char == "\\":
                index += 2
                continue
            if char == quote:
                quote = None
        elif char in "'\"`":
            quote = char
        elif char == "/" and _regex_may_start(text, index):
            index = _skip_regex(text, index)
            continue
        elif char in OPENERS:
            depth += 1
        elif char in CLOSERS:
            depth -= 1
        elif char == "," and depth == 0:
            parts.append(text[start:index])
            start = index + 1
        index += 1
    parts.append(text[start:])
    return [part.strip() for part in parts]


def classify(value):
    """Return (kind, text) for one argument or property value.

    A value that starts with a string but continues ('Add ' + kind, or a call
    on the string) is a fragment, not the text Playwright looks for, so it is
    dynamic and gets a not-covered note.
    """
    value = value.strip()
    if not value:
        return "dynamic", value
    if value[0] in "'\"`":
        end = _string_end(value, value[0])
        if end >= len(value) or value[end + 1:].strip():
            return "dynamic", value
        body = value[1:end]
        if value[0] == "`" and "${" in body:
            return "dynamic", value
        return "literal", _unescape(body)
    if value[0] == "/":
        return "regex", value
    return "dynamic", value


def _string_end(text, quote):
    index = 1
    while index < len(text):
        if text[index] == "\\":
            index += 2
            continue
        if text[index] == quote:
            return index
        index += 1
    return len(text)


def _unescape(body):
    out = []
    index = 0
    simple = {"n": "\n", "t": "\t", "r": "\r"}
    while index < len(body):
        if body[index] == "\\" and index + 1 < len(body):
            out.append(simple.get(body[index + 1], body[index + 1]))
            index += 2
            continue
        out.append(body[index])
        index += 1
    return "".join(out)


def property_value(options, key):
    """The source text of `key: value` in an options object, or None."""
    match = re.search(r"(?:^|[,{])\s*" + re.escape(key) + r"\s*:\s*", options)
    if not match:
        return None
    rest = options[match.end():]
    depth = 0
    index = 0
    quote = None
    while index < len(rest):
        char = rest[index]
        if quote:
            if char == "\\":
                index += 2
                continue
            if char == quote:
                quote = None
        elif char in "'\"`":
            quote = char
        elif char == "/" and _regex_may_start(rest, index):
            index = _skip_regex(rest, index)
            continue
        elif char in OPENERS:
            depth += 1
        elif char in CLOSERS:
            if depth == 0:
                return rest[:index].strip()
            depth -= 1
        elif char == "," and depth == 0:
            return rest[:index].strip()
        index += 1
    return rest.strip()


def is_exact(options):
    return bool(re.search(r"(?:^|[,{])\s*exact\s*:\s*true\b", options))


def _predicate(value):
    """True for a function literal, the shape Array.prototype.filter takes."""
    value = value.lstrip()
    return value.startswith("(") or bool(re.match(
        r"(?:async\s+)?(?:function\b|[A-Za-z_$][\w$]*\s*=>)", value))


def parse_spec(text):
    """Return (literal, line, exact) entries and the not-covered locator notes."""
    literals = []
    not_covered = []
    for match in LOCATOR.finditer(text):
        name = match.group(1)
        line = text.count("\n", 0, match.start()) + 1
        inner, _ = scan_call(text, match.end() - 1)
        if inner is None:
            not_covered.append((line, f"{match.group(0)[:-1]}() (unreadable call)"))
            continue
        args = split_top_level(inner)
        if name == "getByTestId":
            shown = args[0] if args else ""
            not_covered.append((line, f"getByTestId({shown}) (test id)"))
        elif name == "locator":
            shown = args[0] if args else ""
            not_covered.append((line, f"locator({shown}) (selector)"))
        elif name == "filter":
            first = args[0] if args else ""
            # Playwright's filter takes an options object; Array.prototype.filter
            # (the spec counts completed stages) takes a function literal. A bare
            # variable could be either, so it is listed rather than skipped.
            if not first.startswith("{"):
                if not _predicate(first):
                    not_covered.append((line, f"filter({first}) (dynamic filter)"))
                continue
            value = property_value(first, "hasText")
            if value is None:
                not_covered.append((line, "filter() without hasText (no literal)"))
                continue
            _record_literal("filter", line, value, first, literals, not_covered)
        elif name == "getByRole":
            role = args[0] if args else ""
            options = args[1] if len(args) > 1 else ""
            value = property_value(options, "name")
            if value is None:
                not_covered.append(
                    (line, f"getByRole({role}) (role without a literal name)"))
                continue
            _record_literal("getByRole", line, value, options, literals, not_covered)
        else:
            value = args[0] if args else ""
            options = args[1] if len(args) > 1 else ""
            _record_literal(name, line, value, options, literals, not_covered)
    return literals, not_covered


def _record_literal(call, line, value, options, literals, not_covered):
    kind, shown = classify(value)
    if kind == "literal":
        if shown.strip():
            literals.append((line, shown, is_exact(options)))
        else:
            not_covered.append((line, f"{call}('') (empty literal)"))
    elif kind == "regex":
        not_covered.append((line, f"{value.strip()} (regex)"))
    elif call == "getByRole":
        not_covered.append((line, f"name: {value.strip()} (dynamic)"))
    elif call == "filter":
        not_covered.append((line, f"hasText: {value.strip()} (dynamic)"))
    else:
        not_covered.append((line, f"{call}({value.strip()}) (dynamic)"))


def excluded(path):
    return bool(TEST_PATH.search(path) or NON_ENGLISH.search(path))


def whole_value(literal):
    """A pattern for the literal shown as a complete value on one line.

    Playwright's `exact: true` compares the whole accessible name (whitespace
    trimmed), so the source must show the literal as a complete quoted string,
    as a JSX text child, or alone on its line. A prefix, a longer label or a
    punctuation suffix is a different name.
    """
    escaped = re.escape(literal)
    return re.compile(
        rf"(?:'{escaped}'|\"{escaped}\"|`{escaped}`)|(?:>\s*{escaped}\s*<)"
        rf"|(?:^\s*{escaped}\s*$)")


def contains(repo, rev, literal, exact):
    """True when web/src at rev has the literal outside tests and translations.

    A locator without `exact` is a Playwright substring match, so any hit counts.
    `exact: true` is the whole, case-sensitive name: the hit must show a
    complete value, so a longer label, a prefix or a punctuation suffix cannot
    stand in for it.
    """
    if exact:
        args = ["grep", "-n", "-F", "-I", "--no-color", "-e", literal, rev, "--", SRC]
        done = git_call(repo, *args)
        if done.returncode == 2:
            raise OSError(done.stderr.strip() or "git grep could not read the tree")
        if done.returncode not in (0, 1):
            raise OSError(done.stderr.strip() or f"git grep exited {done.returncode}")
        whole = whole_value(literal)
        for row in done.stdout.splitlines():
            parts = row.split(":", 3)
            if len(parts) == 4 and not excluded(parts[1]) and whole.search(parts[3]):
                return True
        return False
    args = ["grep", "-l", "-F", "-I", "--no-color", "-i", "-e", literal, rev, "--", SRC]
    done = git_call(repo, *args)
    if done.returncode == 2:
        raise OSError(done.stderr.strip() or "git grep could not read the tree")
    if done.returncode not in (0, 1):
        raise OSError(done.stderr.strip() or f"git grep exited {done.returncode}")
    return any(not excluded(path) for path in done.stdout.splitlines())


def main():
    parser = argparse.ArgumentParser(
        description="Check the first-hour spec's literals against the console.")
    parser.add_argument("--repo", default=".", help="repository path (default: .)")
    parser.add_argument("--head", default="HEAD", help="revision to judge (default: HEAD)")
    options = parser.parse_args()
    repo = Path(options.repo).resolve()

    rev = git_call(repo, "rev-parse", "--verify", options.head + "^{commit}")
    if rev.returncode:
        print(f"check-first-hour-literals: cannot read revision {options.head!r}: "
              f"{rev.stderr.strip() or 'git rev-parse failed'}", file=sys.stderr)
        return 2
    head = rev.stdout.strip()

    try:
        spec_present = path_at(repo, head, SPEC)
    except OSError as error:
        print(f"check-first-hour-literals: {error}", file=sys.stderr)
        return 2
    if not spec_present:
        print(f"::error file={SPEC}::the first-hour spec is missing at "
              f"{head}; the release journey has no spec to run")
        return 1

    spec = git_call(repo, "show", f"{head}:{SPEC}")
    if spec.returncode:
        print(f"check-first-hour-literals: cannot read {SPEC} at {head}: "
              f"{spec.stderr.strip() or 'git show failed'}", file=sys.stderr)
        return 2

    literals, not_covered = parse_spec(strip_comments(spec.stdout))
    for line, note in not_covered:
        # repr: a newline in spec text must not become a workflow command line.
        print(f"NOT COVERED {SPEC}:{line}: {note!r}")
    print(f"limit: {LIMIT}")

    distinct = {}
    for line, literal, exact in literals:
        entry = distinct.setdefault(literal, {"line": line, "exact": False})
        entry["exact"] = entry["exact"] or exact

    if not distinct:
        print(f"::error file={SPEC}::the first-hour spec has no literal locators at "
              f"{head}; a check with nothing to check is not a pass")
        return 1

    try:
        src_present = path_at(repo, head, SRC)
    except OSError as error:
        print(f"check-first-hour-literals: {error}", file=sys.stderr)
        return 2
    missing = []
    for literal, entry in distinct.items():
        try:
            found = src_present and contains(repo, head, literal, entry["exact"])
        except OSError as error:
            print(f"check-first-hour-literals: {error}", file=sys.stderr)
            return 2
        if not found:
            missing.append((entry["line"], literal, entry["exact"]))

    for line, literal, exact in missing:
        how = "case-sensitive (exact: true)" if exact else "case-insensitive"
        print(f"::error file={SPEC},line={line}::{literal!r} is read by this "
              f"locator but no source or English locale catalog under {SRC} "
              f"contains it ({how})")
    if missing:
        print(f"first-hour literals: {len(missing)} missing from {SRC} at "
              f"{head}; {len(distinct) - len(missing)} of {len(distinct)} "
              f"distinct literal(s) found")
        return 1
    print(f"first-hour literals: {len(distinct)} distinct literal(s) read by "
          f"{SPEC}; all found under {SRC} at {head}")
    return 0


if __name__ == "__main__":
    sys.exit(main())
