#!/usr/bin/env python3
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
"""Reject common Spanish prose in source and exported English documentation.

Explicit paths and the default tracked-file scan check full files. --base checks
added lines so existing text can be translated without allowing new Spanish text.
--public-docs scans shipped Markdown and docs-site/public text without Git metadata.
--public-tooling checks existing contributor-facing tooling prose in the curated file set.
"""

import argparse
import ast
import json
import os
from pathlib import Path
import re
import subprocess
import sys
import unicodedata

SOURCE_SUFFIXES = {".go", ".py", ".sh", ".bash", ".zsh", ".js", ".jsx", ".ts", ".tsx",
                   ".mjs", ".cjs", ".yml", ".yaml", ".json", ".sql", ".proto", ".toml",
                   ".c", ".h", ".cpp", ".html", ".css", ".scss", ".java", ".rs",
                   ".rb", ".lua", ".ps1", ".mk", ".work"}
WORD_DATA = "archivo|archivos|fichero|ficheros|formateador|incumplidor|incumplidores|senuelo|senuelos|probar|juzgar|carril|censo|deuda|salida|fallo|fallan|fallar|arreglado|arreglados|arreglar|anadir|anadido|ninguno|ninguna|ilegible|medida|medidas|falta|faltan|pudo|pude|puedo|cuantos|cuantas|tarea|tareas"  # language-data: vocabulary
PHRASE_DATA = "por defecto|sin cambios|se puede|no existe|en la|de la|esto es|no he podido mirar"  # language-data: vocabulary
SPANISH = re.compile(r"[\u00e1\u00e9\u00ed\u00f3\u00fa\u00f1\u00bf\u00a1]"
                     + r"|(?<![a-z])(?:" + WORD_DATA + r")(?![a-z])|\b(?:"
                     + PHRASE_DATA + r")\b", re.IGNORECASE)
# Names such as Diataxis and translated language labels are valid in English docs.
# Check vocabulary, not accents alone, and also catch unaccented Spanish prose.
DOC_WORD_DATA = "configuracion|documentacion|pestana|rastreador|rastreadores"  # language-data: vocabulary
DOC_PHRASE_DATA = "lo que|asi que|aqui decia|de este|en este|por su"  # language-data: vocabulary
DOC_SPANISH = re.compile(r"\b(?:" + WORD_DATA + "|" + DOC_WORD_DATA + "|"
                         + PHRASE_DATA + "|" + DOC_PHRASE_DATA + r")\b", re.IGNORECASE)
BINARY_ASSETS = {".png", ".jpg", ".jpeg", ".gif", ".webp", ".ico", ".woff", ".woff2",
                 ".ttf", ".pdf", ".mp4", ".webm", ".zip", ".gz"}
LITERAL = r'"(?:\\.|[^"\\])*"|\'(?:\\.|[^\'\\])*\''
QUOTED = re.compile(LITERAL + r"|`[^`]*`")
DATA_MARKER = re.compile(r"(?:#|//)\s*language-data:\s+\S")
COMMENT = re.compile(QUOTED.pattern + r"|(?P<comment>#.*|//.*)")
DATA_VALUE = re.compile(r"^\s*(?P<declaration>(?:export\s+)?(?:const|let|var)\s+)?[A-Za-z_]\w*"
                        + r"\s*(?P<operator>:=|=)\s*(?P<value>" + LITERAL + r")\s*;?\s*$")


def git(*args):
    result = subprocess.run(["git", "--literal-pathspecs", *args], capture_output=True,
                            env={**os.environ, "GIT_NO_LAZY_FETCH": "1"})
    if result.returncode:
        raise ValueError("cannot read repository inputs")
    return result.stdout


def is_source(path):
    # Locale JSON and generated locale bundles are data; check authored source instead.
    path = os.path.relpath(path)
    if re.search(r"(?:^|/)(?:i18n|locales)/(?:es|fr|de|ru|ja|zh)(?:\.json|/.*\.json)$", path):
        return False
    if path.startswith("core/internal/webui/dist/") or "testdata" in Path(path).parts:
        return False
    source = Path(path)
    if (source.suffix in SOURCE_SUFFIXES or source.name == "Makefile"
            or source.name.startswith("Dockerfile")):
        return True
    if source.is_symlink():
        return False
    with source.open("rb") as stream:
        return stream.read(2) == b"#!"


def data_assignment_lines(path, text):
    # Python keyword arguments look like assignments until their syntax is checked.
    if Path(path).suffix != ".py":
        return None
    # Bare CR makes Python line numbers differ from Git line numbers.
    if re.search(r"\r(?!\n)", text):
        return set()
    try:
        return {node.lineno for node in ast.walk(ast.parse(text))
                if isinstance(node, (ast.Assign, ast.AnnAssign))
                and isinstance(node.value, ast.Constant) and isinstance(node.value.value, str)}
    except SyntaxError:
        return set()


def prose(path, line, number, assignments):
    # Inline code keys identify accepted input syntax; surrounding prose stays checked.
    line = QUOTED.sub(
        lambda m: re.sub(r"`[A-Za-z_][A-Za-z0-9_.-]*:`",
                         lambda key: " " * len(key.group()), m.group())
        if m.group()[0] in {"\"", "'"} else m.group(), line)
    # Task's list format carries a stable command key before its human description.
    if Path(path).suffix in {".yml", ".yaml"}:
        line = re.sub(r"^\*\s+[^\s]+:\s{2,}", "", line)
    # Match patterns are input data; comments and emitted diagnostics still use English.
    if Path(path).suffix in {".sh", ".bash", ".zsh", ".yml", ".yaml"} and not line.lstrip().startswith("#"):
        matches = list(QUOTED.finditer(line))
        outside = COMMENT.sub(lambda m: " " * len(m.group()), line)
        for match in reversed(matches):
            if not any(token in match.group() for token in ("$(", "`")) and re.search(
                    r"(?:^|[|;&]\s*|\b(?:then|do)\s+)\s*(?:command\s+)?grep\s+(?:--quiet|-[A-Za-z]*q[A-Za-z]*)(?:\s+--?[\w-]+)*\s*$", outside[:match.start()]):
                line = line[:match.start()] + " " * len(match.group()) + line[match.end():]
    # Only annotated data assignments bypass the check. Keep comments and calls visible.
    if assignments is not None and number not in assignments:
        return line
    name = Path(path).name
    fixture = (name.startswith(("test-", "test_")) or name.endswith("_test.go")
               or ".test." in name or ".spec." in name
               or bool({"i18n", "locales"}.intersection(Path(path).parts))
               or Path(path).resolve() == Path(__file__).resolve())
    if fixture:
        comment = next((m for m in COMMENT.finditer(line) if m.group("comment")), None)
        if comment and DATA_MARKER.match(comment.group()):
            data = DATA_VALUE.fullmatch(line[:comment.start()])
            if data and (assignments is not None or data.group("declaration")
                         or data.group("operator") == ":="
                         or Path(path).suffix in {".sh", ".bash", ".zsh"}):
                if assignments is not None or not any(
                        token in data.group("value") for token in ("$(", "${", "`")):
                    return line[:data.start("value")] + " " + line[data.end("value"):]
    return line


def added_lines(base, path):
    diff = git("diff", "--no-ext-diff", "--no-textconv", "--no-color", "--no-renames",
               "--unified=0", base, "--", path).decode("utf-8")
    if re.search(r"(?m)^(?:Binary files .+ differ|GIT binary patch)$", diff):
        raise ValueError("cannot check a binary source diff")
    selected = set()
    number = None
    for line in diff.split("\n"):
        hunk = re.match(r"^@@ -\d+(?:,\d+)? \+(\d+)(?:,\d+)? @@", line)
        if hunk:
            number = int(hunk.group(1))
        elif number is not None and line.startswith("+"):
            selected.add(number)
            number += 1
        elif number is not None and line.startswith(" "):
            number += 1
    return selected


def public_docs(root):
    if not root.is_dir():
        raise ValueError("public documentation root is missing")
    # Import the site's authoritative locale map; do not duplicate its directory list.
    module_path = root / "docs-site/src/site-locales.mjs"
    if module_path.is_symlink() or any(parent.is_symlink() for parent in module_path.parents):
        raise ValueError("cannot read documentation locales through a symlink")
    module = module_path.as_uri()
    result = subprocess.run(
        ["node", "--input-type=module", "-e",
         "import(process.argv[1]).then(m => console.log(JSON.stringify(m.PUBLISHED_LOCALES)))",
         module], capture_output=True, text=True, timeout=10)
    if result.returncode:
        raise ValueError("cannot read published documentation locales")
    locales = json.loads(result.stdout)
    if (not isinstance(locales, list) or not locales
            or any(not isinstance(locale, str) or not re.fullmatch(r"[a-z][a-z0-9-]*", locale)
                   or locale in {"root", "en"} for locale in locales)):
        raise ValueError("invalid published documentation locales")

    def unavailable(error):
        raise error

    paths = []
    for directory, dirs, files in os.walk(root, onerror=unavailable):
        dirs[:] = sorted(d for d in dirs if d != ".git")
        for name in sorted(files):
            path = Path(directory) / name
            relative = path.relative_to(root)
            parts = relative.parts
            if (parts[:4] == ("docs-site", "src", "content", "docs")
                    and len(parts) > 4 and parts[4] in locales):
                continue
            markdown = path.suffix in {".md", ".mdx"}
            if markdown and any(name.endswith(f".{locale}{path.suffix}") for locale in locales):
                continue
            static = parts[:2] == ("docs-site", "public")
            if not markdown and not (static and path.suffix not in BINARY_ASSETS):
                continue
            if path.is_symlink() or any(parent.is_symlink() for parent in path.parents):
                raise ValueError("cannot check public documentation through a symlink")
            paths.append(str(relative))
        # os.walk does not follow directory symlinks; refuse selected docs hidden there.
        if any((Path(directory) / d).is_symlink() for d in dirs):
            raise ValueError("cannot scan public documentation through a directory symlink")
    if not paths:
        raise ValueError("no public documentation inputs were selected")
    return paths


def public_tooling(root):
    """Use the export's file set, or discover an already exported tree without Git."""
    if root.is_symlink() or any(parent.is_symlink() for parent in root.parents):
        raise ValueError("cannot scan public tooling through a symlink")
    if not root.is_dir() or not (root / "Taskfile.yml").is_file():
        raise ValueError("public tooling root or Taskfile is missing")
    exporter = root / "scripts" / "export-public.sh"
    if exporter.is_symlink():
        raise ValueError("cannot read the export manifest through a symlink")
    if exporter.is_file():
        if exporter.is_symlink() or any(parent.is_symlink() for parent in exporter.parents):
            raise ValueError("cannot read the export manifest through a symlink")
        result = subprocess.run(["bash", str(exporter), "--manifest"], cwd=root,
                                capture_output=True, text=True, timeout=120)
        if result.returncode:
            raise ValueError("cannot read the public export manifest")
        names = result.stdout.splitlines()
    else:
        names = ["Taskfile.yml"]
        for directory in (".github/workflows", ".githooks", "scripts"):
            folder = root / directory
            if not folder.is_dir():
                raise ValueError("public tooling directory is missing")
            def unavailable(error):
                raise error

            for base, dirs, files in os.walk(folder, onerror=unavailable):
                if any((Path(base) / d).is_symlink() for d in dirs):
                    raise ValueError("cannot scan public tooling through a directory symlink")
                names.extend(str((Path(base) / name).relative_to(root)) for name in files)
    paths = []
    for name in sorted(set(names)):
        path = Path(name)
        if path.is_absolute() or ".." in path.parts:
            raise ValueError("invalid public tooling path")
        if name != "Taskfile.yml" and not name.startswith(
                (".github/workflows/", ".githooks/", "scripts/")):
            continue
        full = root / path
        if full.is_symlink() or any(parent.is_symlink() for parent in full.parents):
            raise ValueError("cannot check public tooling through a symlink")
        if not full.is_file():
            raise ValueError("a selected public tooling file is missing")
        if name.startswith("scripts/") and path.suffix not in SOURCE_SUFFIXES:
            with full.open("rb") as stream:
                if stream.read(2) != b"#!":
                    continue
        paths.append(name)
    if "Taskfile.yml" not in paths or ".githooks/pre-push" not in paths or not any(
            p.startswith(".github/workflows/") for p in paths) or not any(
            p.startswith("scripts/") for p in paths):
        raise ValueError("public tooling inputs are incomplete")
    return paths


TOOLING_OUTPUT_WORDS = "echo|printf|print|say|die|fail|fatal|error|warn|warning|notice|refuse|unverified|ok|bad|ko|no_puedo|no_he_podido|no_he_podido_mirar|raise|write|Print|Printf|Println|Fprint|Fprintf|Fprintln|Sprintf|Fatal|Fatalf|Errorf|cannot|cannot_check|cannot_look|no_pude|die_unreadable|nombra|add|malo|nota|anota|report|check|espera|mal|ciego|blind|decir|di|require|require_assertion_marker|log|info|Info|Infof|WriteString|awk"  # language-data: diagnostic helper names
TOOLING_OUTPUT = re.compile(r"\b(?:" + TOOLING_OUTPUT_WORDS + r")\b")
TOOLING_WORD_DATA = "instalalas|comprueba|comprueban|comprobar|comprobacion|techos|cuarta|entorno|vacia|vacio|sujeto|gancho|repite|plantilla|emite|medido|hallazgos|huerfano|segun|actualiza|buscados|corrida|rojo|resuelto|encontrado|declaradas|reales|rutas|verificadas|claves|crudas|vistas|imagenes|decisiones|sembradas|releidas|pagina|presente|modo|cero|anonima|recibido|remedio|corriendo|privilegios|ejecutala|bloque|bloques|omitido|parsean"  # language-data: vocabulary
TOOLING_PHRASE_DATA = "de los|de las|con el|y no|el mismo|en los|en este|lo que|no hay|esto no|se informa|no se cobra|dos veces"  # language-data: vocabulary
TOOLING_SPANISH = re.compile(SPANISH.pattern + r"|\b(?:" + TOOLING_WORD_DATA + "|"
                             + TOOLING_PHRASE_DATA + r")\b", re.IGNORECASE)


def redirected_group_lines(text):
    """Locate standalone shell groups whose stdout is written to a file.

    Ambiguous groups and explicit stream overrides remain checked. Heredoc
    bodies cannot supply structural braces for the surrounding shell script.
    """
    groups = []
    redirected = set()
    delimiter = None
    for number, line in enumerate(text.split("\n"), 1):
        if delimiter is not None:
            if line.strip() == delimiter:
                delimiter = None
            continue
        source = COMMENT.sub(lambda m: "" if m.group("comment") else m.group(), line)
        outside = QUOTED.sub(lambda m: " " * len(m.group()), source)
        document = re.search(r"(?<!<)<<-?\s*(['\"]?)([A-Za-z_]\w*)\1", source)
        if document and outside[document.start():].startswith("<<"):
            delimiter = document.group(2)
        # A stream override or exec makes the group's output destination unclear.
        if re.search(r">&|\bexec\b", outside) or re.search(r"/(?:dev/(?:stdout|stderr|fd/[12])|proc/self/fd/[12])\b", source):
            for group in groups:
                group[2] = True
        indent = len(line) - len(line.lstrip())
        if outside.strip() == "{":
            groups.append([number, indent, False])
        elif re.match(r"\s*}(?:\s|$)", outside) and groups and indent == groups[-1][1]:
            start, _, ambiguous = groups.pop()
            if not ambiguous and re.match(r"\s*}\s*(?:1)?>>?(?![>&])", outside):
                redirected.update(range(start + 1, number))
    return redirected


def tooling_lines(path, text):
    """Select full descriptions, Actions names/annotations and emitted diagnostics.

    Comments, shell variables and literal paths are not contributor prose. Keep
    quoted continuations so multiline diagnostics are checked in their entirety.
    """
    field_indent = None
    continued = False
    pending_quote = None
    heredoc = None
    fixture_heredoc = None
    redirected_groups = redirected_group_lines(text) if (Path(path).suffix in {".sh", ".bash", ".zsh", ".yml", ".yaml"} or path.startswith(".githooks/")) else set()
    assignments = data_assignment_lines(path, text)
    for number, line in enumerate(text.split("\n"), 1):
        if fixture_heredoc is not None:
            if line.strip() == fixture_heredoc:
                fixture_heredoc = None
            continue
        if heredoc is not None:
            if line.strip() == heredoc:
                heredoc = None
            else:
                yield number, line
            continue
        if not line.strip() or line.lstrip().startswith(("#", "//")):
            continue
        yaml = Path(path).suffix in {".yml", ".yaml"}
        # Locate calls outside string literals, so matcher inputs and data are not
        # mistaken for commands. Preserve positions while masking quoted tokens.
        source = prose(path, line, number, assignments)
        source = COMMENT.sub(lambda m: "" if m.group("comment") else m.group(), source)
        command = re.match(r"\s*(?:-\s*)?run:\s*(['\"].*)", source) if yaml else None
        if command:
            scalar = command.group(1).strip()
            if scalar.startswith("'") and scalar.endswith("'"):
                source = scalar[1:-1].replace("''", "'")
            elif scalar.startswith('"') and scalar.endswith('"'):
                source = ast.literal_eval(scalar)
        outside = QUOTED.sub(lambda m: " " * len(m.group()), source)
        help_block = re.search(r"\bcat\s+(?:[12]?>&[12]\s*)?<<-?\s*(['\"]?)([A-Za-z_]\w*)\1", source)
        if help_block and outside[help_block.start():].startswith("cat"):
            if (re.search(r"(?<![<>])>(?![>&])", outside)
                    or number in redirected_groups):
                fixture_heredoc = help_block.group(2)
            else:
                heredoc = help_block.group(2)
            continue
        written_block = re.search(r"\bcat\s+[^\n]*?<<-?\s*(['\"]?)([A-Za-z_]\w*)\1", source)
        if written_block and re.search(r"(?<![<>])>(?![>&])", outside):
            fixture_heredoc = written_block.group(2)
            continue
        indent = len(line) - len(line.lstrip())
        field = re.match(r"\s*(?:-\s*)?(?:desc|name):\s*(.*)", source) if yaml else None
        if field:
            value = field.group(1).strip()
            field_indent = indent + (2 if line.lstrip().startswith("- ") else 0)
        elif field_indent is not None and indent > field_indent:
            value = line.strip()
        else:
            field_indent = None
            outputs = [match for match in TOOLING_OUTPUT.finditer(outside)
                       if re.match(r"(?:\s*\(|\s+\S)", source[match.end():])
                       and (match.group() != "check" or re.match(r'''\s+['"]''', source[match.end():]))
                       and not re.search(r"\b(?:def|func|function)\s+$", outside[:match.start()])]
            if not outputs and pending_quote is None and not source.lstrip().startswith(("'", '"', "+")):
                continued = False
            if not (outputs or continued):
                continued = False
                continue
            fragments = []
            if pending_quote is not None or not outputs:
                fragments.append((source, None))
            else:
                for output in outputs:
                    fragment = source[output.start():]
                    tail = outside[output.start():]
                    if number in redirected_groups and output.group() in {"echo", "printf", "say"}:
                        continue
                    if Path(path).suffix in {".sh", ".bash", ".zsh", ".yml", ".yaml"} or path.startswith(".githooks/"):
                        separator = re.search(r"\||;|&&", tail)
                        if separator:
                            fragment = fragment[:separator.start()]
                        command_tail = tail[:separator.start()] if separator else tail
                        if re.search(r"(?<![<>])>>(?![>&])|(?<![<>])>(?![>&])", command_tail):
                            continue
                    # Assertion helpers emit their reason, not their checked input.
                    if output.group() in {"require", "require_assertion_marker"} and "(" in tail:
                        depth = 0
                        for offset, char in enumerate(tail):
                            depth += (char == "(") - (char == ")")
                            if char == "," and depth == 1:
                                fragment = fragment[offset + 1:]
                                break
                    fragments.append((fragment, output.group()))
            values = []
            for fragment, emitter in fragments:
                was_pending = pending_quote is not None
                quoted_found = False
                if pending_quote is not None:
                    parts = fragment.split(pending_quote, 1)
                    literal = parts[0]
                    pending_quote = None if len(parts) == 2 else pending_quote
                else:
                    literals = []
                    for match in QUOTED.finditer(fragment):
                        # A dictionary lookup carries a key, not emitted prose.
                        if (re.search(r"[\w)]\s*\[\s*$", fragment[:match.start()])
                                and re.match(r"\s*]", fragment[match.end():])):
                            continue
                        quoted_found = True
                        quoted = match.group()
                        if emitter == "awk":
                            quoted = " ".join(m.group("literal") for m in re.finditer(
                                r"\b(?:printf|sprintf)\s*\(?\s*(?P<literal>" + LITERAL + r")", quoted[1:-1]))
                        prefix = re.search(r"\b(?:f|fr|rf)$", fragment[:match.start()], re.IGNORECASE)
                        if prefix and quoted and quoted[0] in {"'", '"'}:
                            try:
                                expression = ast.parse(prefix.group() + quoted, mode="eval").body
                            except SyntaxError:
                                expression = None
                            if isinstance(expression, ast.JoinedStr):
                                quoted = " ".join(node.value for node in expression.values
                                                  if isinstance(node, ast.Constant) and isinstance(node.value, str))
                        literals.append(quoted)
                    literal = " ".join(literals)
                    if emitter in {"echo", "printf", "say"} and not re.match(
                            r"\s*printf\s+(?:" + LITERAL + r")\s*,", fragment):
                        literal += " " + QUOTED.sub(" ", fragment[len(emitter):])
                # An unterminated literal can continue after complete arguments.
                unmatched = re.search(r'[\"\']', QUOTED.sub(lambda m: " " * len(m.group()), fragment))
                if (not was_pending and pending_quote is None and unmatched
                        and emitter != "awk"
                        and (unmatched.start() == 0 or fragment[unmatched.start() - 1] in " \t(,+=")):
                    literal += " " + fragment[unmatched.start():]
                    pending_quote = unmatched.group()
                elif not literal and not quoted_found:
                    literal = fragment[len(emitter):] if emitter in {"echo", "printf", "say"} else ""
                values.append(literal)
            value = " ".join(values)
            continued = bool(re.search(r"[\\,(+]\s*$", fragments[-1][0] if fragments else "")
                             or re.search(r"\b(?:" + TOOLING_OUTPUT_WORDS + r")\b.*\(", line)
                             and not line.rstrip().endswith(")")
                             or pending_quote is not None)
        # Paths and expansions carry identifiers, not translated messages.
        value = re.sub(r"\$\{[^}]*\}",
                       lambda m: m.group()[2:-1].split(":-", 1)[1]
                       if ":-" in m.group() else "", value)
        value = re.sub(r"\$[A-Za-z_]\w*|\$\([^)]*\)", "", value)
        value = re.sub(r"(?:[\w.-]+/)+[\w.*:-]+", "", value)
        value = re.sub(r"(?<!\w)--[\w:-]+", "", value)
        value = re.sub(r"(?<![\w-])" + re.escape(Path(path).stem) + r"(?![\w-])", "", value)
        value = re.sub(r"\b[\w.-]+\.(?:sh|py|go|mjs|yml|yaml|json|txt)\b", "", value)
        yield number, value


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("paths", nargs="*", help="files to check; default: tracked source files")
    parser.add_argument("--base", help="check added lines since this ancestor commit")
    parser.add_argument("--public-docs", type=Path, metavar="ROOT",
                        help="check full English documentation in a curated export")
    parser.add_argument("--public-tooling", type=Path, metavar="ROOT",
                        help="check full contributor-facing text in the curated tooling")
    args = parser.parse_args()
    if args.public_tooling is not None and (args.base is not None or args.paths
                                           or args.public_docs is not None):
        parser.error("choose --public-tooling, --public-docs, explicit paths, or --base")
    if args.base is not None and args.paths:
        parser.error("choose explicit paths or --base")
    if args.public_docs is not None and (args.base is not None or args.paths):
        parser.error("choose --public-docs, explicit paths, or --base")
    try:
        if args.public_tooling is not None:
            root = args.public_tooling.absolute()
            paths = public_tooling(root)
            os.chdir(root)
        elif args.public_docs is not None:
            root = args.public_docs.absolute()
            paths = public_docs(root)
            os.chdir(root)
        elif args.base is not None:
            base = git("rev-parse", "--verify", "--end-of-options",
                       args.base + "^{commit}").strip().decode()
            git("merge-base", "--is-ancestor", base, "HEAD")
            inputs = git("diff", "--name-only", "-z", "--diff-filter=ACMRT",
                         "--no-renames", base, "--")
            paths = [os.fsdecode(p) for p in inputs.split(b"\0") if p]
        else:
            paths = args.paths or [os.fsdecode(p) for p in git("ls-files", "-z").split(b"\0") if p]
            if not paths:
                raise ValueError("no source inputs were selected")
        checked = failures = 0
        for name in paths:
            path = Path(name)
            if not path.is_file() and not path.is_symlink():
                raise ValueError("a selected source file is missing")
            if args.public_docs is None and not is_source(name):
                continue
            # A Git symlink stores its target path; do not read files outside the source tree.
            text = os.readlink(path) if path.is_symlink() else path.read_bytes().decode("utf-8")
            if "\0" in text:
                raise ValueError("cannot check a binary source file")
            selected = added_lines(base, name) if args.base is not None else None
            assignments = data_assignment_lines(name, text)
            checked += 1
            lines = (tooling_lines(name, text) if args.public_tooling is not None
                     else enumerate(text.split("\n"), 1))
            for number, line in lines:
                if selected is not None and number not in selected:
                    continue
                text_line = unicodedata.normalize(
                    "NFC", line if args.public_docs is not None
                    else prose(name, line, number, assignments))
                text_line = re.sub(r"(?<=[a-z])(?=[A-Z])", " ", text_line)
                detector = SPANISH
                if args.public_tooling is not None:
                    detector = TOOLING_SPANISH
                if args.public_docs is not None:
                    text_line = "".join(c for c in unicodedata.normalize("NFD", text_line)
                                        if not unicodedata.combining(c))
                    detector = DOC_SPANISH
                if detector.search(text_line):
                    label = json.dumps(name, ensure_ascii=False)[1:-1]
                    print(f"{label}:{number}: Spanish text detected; use US English.")
                    failures += 1
        scope = "added lines" if args.base is not None else "full files"
        print(f"source-language: checked {checked} file(s), {scope}; {failures} finding(s)")
        return 1 if failures else 0
    except (OSError, UnicodeError, ValueError, SyntaxError, subprocess.TimeoutExpired) as error:
        print(f"source-language: UNAVAILABLE: {error}", file=sys.stderr)
        return 2


if __name__ == "__main__":
    sys.exit(main())
