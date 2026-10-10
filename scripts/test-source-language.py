#!/usr/bin/env python3
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
"""Test the source-language check through its command-line interface."""

import os
from pathlib import Path
import subprocess
import sys
import tempfile
import unittest

CHECK = Path(__file__).with_name("check-source-language.py")


class SourceLanguageTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory(dir=os.environ.get("TMPDIR"))
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name)
        self.env = {k: v for k, v in os.environ.items() if not k.startswith("GIT_")}
        self.env.update(GIT_NO_LAZY_FETCH="1", GIT_CONFIG_GLOBAL="/dev/null",
                        GIT_CONFIG_SYSTEM="/dev/null", GIT_CONFIG_NOSYSTEM="1",
                        GIT_TEMPLATE_DIR="", GIT_AUTHOR_NAME="Language test",
                        GIT_AUTHOR_EMAIL="language-test@example.invalid",
                        GIT_COMMITTER_NAME="Language test",
                        GIT_COMMITTER_EMAIL="language-test@example.invalid")

    def write(self, name, text):
        path = self.root / name
        path.parent.mkdir(parents=True, exist_ok=True)
        path.write_text(text, encoding="utf-8")
        return path

    def check(self, *args):
        return subprocess.run([sys.executable, str(CHECK), *map(str, args)],
                              cwd=self.root, env=self.env, capture_output=True, text=True)

    def git(self, *args):
        subprocess.run(["git", "-c", "core.hooksPath=/dev/null", *args], cwd=self.root,
                       env=self.env, check=True, stdout=subprocess.DEVNULL,
                       stderr=subprocess.PIPE)

    def tooling_tree(self):
        self.write("Taskfile.yml", "version: '3'\ntasks:\n  lint:\n    desc: Check source files\n    cmds: [true]\n")
        self.write(".github/workflows/check.yml", "name: Checks\njobs:\n  lint:\n    steps:\n      - name: Check source\n        run: echo Clean\n")
        self.write(".githooks/pre-push", '#!/bin/sh\necho "Push checked"\n')
        self.write("scripts/check-example.sh", '#!/bin/sh\necho "Clean"\n')

    def test_public_tooling_checks_full_existing_messages_without_git(self):
        self.tooling_tree()
        source_0 = "version: '3'\ntasks:\n  lint:\n    desc: >-\n      Check inputs.\n      El archivo no existe.\n    cmds: [true]\n"  # language-data: rejection fixture
        source_1 = "jobs:\n  lint:\n    steps:\n      - name: Validación del archivo\n        run: echo Clean\n"  # language-data: rejection fixture
        source_2 = '#!/bin/sh\necho "Falló la tarea"\n'  # language-data: rejection fixture
        source_3 = '#!/bin/sh\nprintf \'El archivo no existe\\n\' >&2\n'  # language-data: rejection fixture
        cases = {
            "Taskfile.yml": source_0,
            ".github/workflows/check.yml": source_1,
            ".githooks/pre-push": source_2,
            "scripts/check-example.sh": source_3,
        }
        for name, source in cases.items():
            with self.subTest(name=name):
                self.tooling_tree()
                self.write(name, source)
                result = self.check("--public-tooling", self.root)
                self.assertEqual(result.returncode, 1, result.stdout + result.stderr)
                self.assertIn(name + ":", result.stdout)

    def test_public_tooling_checks_annotations_and_multiline_diagnostics(self):
        self.tooling_tree()
        source = 'jobs:\n  lint:\n    steps:\n      - run: |\n          echo "::warning::El archivo no existe"\n'  # language-data: rejection fixture
        self.write(".github/workflows/check.yml", source)
        result = self.check("--public-tooling", self.root)
        self.assertEqual(result.returncode, 1, result.stdout + result.stderr)
        self.tooling_tree()
        source = 'print("Check inputs: "\n      "el archivo no existe")\n'  # language-data: rejection fixture
        self.write("scripts/check-example.py", source)
        result = self.check("--public-tooling", self.root)
        self.assertEqual(result.returncode, 1, result.stdout + result.stderr)
        self.assertIn("scripts/check-example.py:2", result.stdout)

    def test_public_tooling_checks_unquoted_and_multiline_shell_output(self):
        unquoted = "#!/bin/sh\necho El archivo no existe\n"  # language-data: rejection fixture
        multiline = '#!/bin/sh\necho "Check inputs:\nel archivo no existe"\n'  # language-data: rejection fixture
        for source in (unquoted, multiline):
            self.tooling_tree()
            self.write("scripts/check-example.sh", source)
            result = self.check("--public-tooling", self.root)
            self.assertEqual(result.returncode, 1, result.stdout + result.stderr)

    def test_public_tooling_checks_shell_default_messages(self):
        self.tooling_tree()
        source = '#!/bin/sh\necho "${MESSAGE:-El archivo no existe}"\n'  # language-data: rejection fixture
        self.write("scripts/check-example.sh", source)
        result = self.check("--public-tooling", self.root)
        self.assertEqual(result.returncode, 1, result.stdout + result.stderr)

    def test_public_tooling_checks_extensionless_helpers_and_generic_verdicts(self):
        source = '#!/bin/sh\necho "NO HE PODIDO MIRAR: missing input"\n'  # language-data: rejection fixture
        for name in ("scripts/example", "scripts/check-example.sh"):
            with self.subTest(name=name):
                self.tooling_tree()
                self.write(name, source)
                result = self.check("--public-tooling", self.root)
                self.assertEqual(result.returncode, 1, result.stdout + result.stderr)
                self.assertIn(name + ":2", result.stdout)
                (self.root / name).unlink()

    def test_public_tooling_ignores_comments_paths_and_variable_names(self):
        self.tooling_tree()
        source = '# El archivo no existe.\nsalida=1\necho "Output: $salida; see scripts/pre-verify-tanda.sh"\n'  # language-data: code fixture
        self.write("scripts/check-example.sh", source)
        result = self.check("--public-tooling", self.root)
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)

    def test_public_tooling_checks_emitted_heredoc_help(self):
        self.tooling_tree()
        source = "#!/bin/sh\ncat <<'HELP' >&2\nEl archivo no existe\nHELP\n"  # language-data: rejection fixture
        self.write("scripts/check-example.sh", source)
        result = self.check("--public-tooling", self.root)
        self.assertEqual(result.returncode, 1, result.stdout + result.stderr)
        self.assertIn("scripts/check-example.sh:3", result.stdout)

    def test_public_tooling_does_not_treat_heredoc_fixture_as_help(self):
        self.tooling_tree()
        source = "#!/bin/sh\ncat >input <<'DATA'\nEl archivo no existe\nDATA\necho 'Checked input'\n"  # language-data: code fixture
        self.write("scripts/check-example.sh", source)
        result = self.check("--public-tooling", self.root)
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)

    def test_public_tooling_checks_output_arguments_instead_of_matcher_inputs(self):
        self.tooling_tree()
        source = "#!/bin/sh\nlocal ok=0 salida\n# A historical document still uses this heading.\ngrep -q 'Salida 1' input || fail 'record lost Exit 1'\nprintf '%s\\n' \"$output\" | grep -E 'no existe'\n"  # language-data: code fixture
        self.write("scripts/check-example.sh", source)
        data = '{"note":"print el archivo no existe"}\n'  # language-data: code fixture
        self.write("scripts/input.json", data)
        result = self.check("--public-tooling", self.root)
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)

    def test_public_tooling_ignores_program_arguments_and_legacy_script_names(self):
        self.tooling_tree()
        source = '#!/bin/sh\necho "check-badge-vacio-filas: missing input"\nawk \'BEGIN { printf "%s\\n", fichero }\' input\nprintf \'%s\' "$jobs_json" | jq \\\n  "{rojo:.failure}"\nes_ilegible() { case "$1" in missing) return 0 ;; esac; }\n'  # language-data: code fixture
        self.write("scripts/check-badge-vacio-filas.sh", source)
        result = self.check("--public-tooling", self.root)
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)

    def test_public_tooling_ignores_written_fixture_text(self):
        self.tooling_tree()
        source = "#!/bin/sh\ncat >input <<'DATA'\nprintf 'El archivo no existe'\nDATA\nprintf 'El archivo no existe' >input\necho 'Checked input'\n"  # language-data: code fixture
        self.write("scripts/check-example.sh", source)
        result = self.check("--public-tooling", self.root)
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)

    def test_public_tooling_redirection_does_not_hide_other_diagnostics(self):
        shell = 'echo "El archivo no existe"; echo Clean > data\n'  # language-data: rejection fixture
        python = 'print("El archivo no existe" if count > 0 else "Clean")\n'  # language-data: rejection fixture
        for name, source in (("scripts/check-example.sh", shell), ("scripts/check-example.py", python)):
            with self.subTest(name=name):
                self.tooling_tree()
                self.write(name, source)
                result = self.check("--public-tooling", self.root)
                self.assertEqual(result.returncode, 1, result.stdout + result.stderr)

    def test_legacy_matcher_inputs_do_not_hide_new_diagnostics(self):
        source = 'grep -q \'SALIDA 1\' input || echo "Required heading missing"\n'  # language-data: code fixture
        self.write("scripts/check-example.sh", source)
        result = self.check("scripts/check-example.sh")
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
        diagnostic = 'grep -q \'SALIDA 1\' input || echo "El archivo no existe"\n'  # language-data: rejection fixture
        self.write("scripts/check-example.sh", diagnostic)
        result = self.check("scripts/check-example.sh")
        self.assertEqual(result.returncode, 1, result.stdout + result.stderr)
        executable = 'grep -q "$(printf \'El archivo no existe\')" input\n'  # language-data: rejection fixture
        self.write("scripts/check-example.sh", executable)
        result = self.check("scripts/check-example.sh")
        self.assertEqual(result.returncode, 1, result.stdout + result.stderr)
        quoted_command = 'echo "grep -q El archivo no existe"\n'  # language-data: rejection fixture
        self.write("scripts/check-example.sh", quoted_command)
        result = self.check("scripts/check-example.sh")
        self.assertEqual(result.returncode, 1, result.stdout + result.stderr)
        emitted = 'grep -E \'El archivo no existe\' input\n'  # language-data: rejection fixture
        self.write("scripts/check-example.sh", emitted)
        result = self.check("scripts/check-example.sh")
        self.assertEqual(result.returncode, 1, result.stdout + result.stderr)
        comment = '# grep -q \'SALIDA 1\' el archivo no existe\n'  # language-data: rejection fixture
        self.write("scripts/check-example.sh", comment)
        result = self.check("scripts/check-example.sh")
        self.assertEqual(result.returncode, 1, result.stdout + result.stderr)

    def test_public_tooling_tracks_group_output_destinations(self):
        source = "{\n cat <<'DATA'\nEl archivo no existe\nDATA\n} >data\n"  # language-data: selector fixture
        with self.subTest(case='group_fixture'):
            self.tooling_tree()
            self.write("scripts/check-example.sh", source)
            result = self.check("--public-tooling", self.root)
            self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
        source = "{\n cat <<'HELP'\nEl archivo no existe\nHELP\n} >&2\n"  # language-data: selector fixture
        with self.subTest(case='group_help'):
            self.tooling_tree()
            self.write("scripts/check-example.sh", source)
            result = self.check("--public-tooling", self.root)
            self.assertEqual(result.returncode, 1, result.stdout + result.stderr)
        source = "{\n cat >&2 <<'HELP'\nEl archivo no existe\nHELP\n} >data\n"  # language-data: selector fixture
        with self.subTest(case='stderr_override'):
            self.tooling_tree()
            self.write("scripts/check-example.sh", source)
            result = self.check("--public-tooling", self.root)
            self.assertEqual(result.returncode, 1, result.stdout + result.stderr)
        source = "{\n printf 'El archivo no existe\\n'\n} >data\n"  # language-data: selector fixture
        with self.subTest(case='printf_fixture'):
            self.tooling_tree()
            self.write("scripts/check-example.sh", source)
            result = self.check("--public-tooling", self.root)
            self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
        source = "{\n printf 'El archivo no existe\\n' >&2\n} >data\n"  # language-data: selector fixture
        with self.subTest(case='printf_stderr'):
            self.tooling_tree()
            self.write("scripts/check-example.sh", source)
            result = self.check("--public-tooling", self.root)
            self.assertEqual(result.returncode, 1, result.stdout + result.stderr)
        source = "cat <<'HELP'\nEl archivo no existe\nHELP\n"  # language-data: selector fixture
        with self.subTest(case='ordinary_help'):
            self.tooling_tree()
            self.write("scripts/check-example.sh", source)
            result = self.check("--public-tooling", self.root)
            self.assertEqual(result.returncode, 1, result.stdout + result.stderr)
        source = "{\n cat <<'HELP'\n} >data\nEl archivo no existe\nHELP\n}\n"  # language-data: selector fixture
        with self.subTest(case='body_fake_close'):
            self.tooling_tree()
            self.write("scripts/check-example.sh", source)
            result = self.check("--public-tooling", self.root)
            self.assertEqual(result.returncode, 1, result.stdout + result.stderr)
        source = "{\n echo 'El archivo no existe'\n} >/dev/stderr\n"  # language-data: selector fixture
        with self.subTest(case='stream_file_group'):
            self.tooling_tree()
            self.write("scripts/check-example.sh", source)
            result = self.check("--public-tooling", self.root)
            self.assertEqual(result.returncode, 1, result.stdout + result.stderr)

    def test_task_list_checks_descriptions_and_preserves_published_keys(self):
        source = '* buzon:censo:            Check inbox sizes\n'  # language-data: published task-key fixture
        self.write("list.yml", source)
        result = self.check("list.yml")
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
        bad = '* buzon:censo:            El archivo no existe\n'  # language-data: rejection fixture
        self.write("list.yml", bad)
        result = self.check("list.yml")
        self.assertEqual(result.returncode, 1, result.stdout + result.stderr)

    def test_quiet_matcher_context_keeps_comments_and_echo_arguments(self):
        comment = "true # grep -q 'El archivo no existe'\n"  # language-data: rejection fixture
        emitted = "echo grep -q 'El archivo no existe'\n"  # language-data: rejection fixture
        for source in (comment, emitted):
            with self.subTest(source=source):
                self.write("scripts/check-example.sh", source)
                result = self.check("scripts/check-example.sh")
                self.assertEqual(result.returncode, 1, result.stdout + result.stderr)

    def test_public_tooling_checks_multiline_custom_reporter_calls(self):
        source = 'di("Checked input"\n   + ("modo --solo-medir: cero POST" if dry_run else ""))\n'  # language-data: rejection fixture
        self.tooling_tree()
        self.write("scripts/check-example.py", source)
        result = self.check("--public-tooling", self.root)
        self.assertEqual(result.returncode, 1, result.stdout + result.stderr)

    def test_public_tooling_ignores_reporter_definitions_and_data_keys(self):
        definition = 'def di(*parts):\n    """Unica salida por stdout."""\n    print("Checked input")\n'  # language-data: code fixture
        lookup = 'print("%s" % entry["vistas"])\n'  # language-data: data-key fixture
        for source in (definition, lookup):
            with self.subTest(source=source):
                self.tooling_tree()
                self.write("scripts/check-example.py", source)
                result = self.check("--public-tooling", self.root)
                self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
        emitted = 'def di():\n    print("El archivo no existe")\n'  # language-data: rejection fixture
        array = 'print(["El archivo no existe"])\n'  # language-data: rejection fixture
        for source in (emitted, array):
            with self.subTest(source=source):
                self.tooling_tree()
                self.write("scripts/check-example.py", source)
                result = self.check("--public-tooling", self.root)
                self.assertEqual(result.returncode, 1, result.stdout + result.stderr)

    def test_public_tooling_checks_every_command_and_literal_braces(self):
        emitted = 'echo "Clean"; echo "El archivo no existe"\n'  # language-data: rejection fixture
        conditional = 'echo "Clean" && printf "El archivo no existe\\n"\n'  # language-data: rejection fixture
        literal = 'print("{El archivo no existe}")\n'  # language-data: rejection fixture
        for name, source in (("scripts/check-example.sh", emitted),
                             ("scripts/check-example.sh", conditional),
                             ("scripts/check-example.py", literal)):
            with self.subTest(name=name, source=source):
                self.tooling_tree()
                self.write(name, source)
                result = self.check("--public-tooling", self.root)
                self.assertEqual(result.returncode, 1, result.stdout + result.stderr)

    def test_public_tooling_preserves_fstring_literal_text_and_ignores_expressions(self):
        self.tooling_tree()
        expression = 'ficheros = 2\nprint(f"{ficheros}")\n'  # language-data: code fixture
        self.write("scripts/check-example.py", expression)
        result = self.check("--public-tooling", self.root)
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
        literal = 'print(f"{{El archivo no existe}}")\n'  # language-data: rejection fixture
        self.write("scripts/check-example.py", literal)
        result = self.check("--public-tooling", self.root)
        self.assertEqual(result.returncode, 1, result.stdout + result.stderr)

    def test_public_tooling_rejects_unaccented_contributor_guidance(self):
        description = "tasks:\n  lint:\n    desc: CUARTA invariante de los techos, la unica que mira DENTRO\n    cmds: [true]\n"  # language-data: rejection fixture
        diagnostic = 'echo "Instalalas con: pnpm --dir web install --frozen-lockfile"\n'  # language-data: rejection fixture
        for name, source in (("Taskfile.yml", description), ("scripts/check-example.sh", diagnostic)):
            with self.subTest(name=name):
                self.tooling_tree()
                self.write(name, source)
                result = self.check("--public-tooling", self.root)
                self.assertEqual(result.returncode, 1, result.stdout + result.stderr)

    def test_public_tooling_checks_mixed_arguments_logs_and_quoted_continuations(self):
        mixed = 'echo "$name": El archivo no existe\n'  # language-data: rejection fixture
        argument = "printf '%s\\n' archivo\n"  # language-data: rejection fixture
        javascript = 'console.log("El archivo no existe")\n'  # language-data: rejection fixture
        quoted = 'tasks:\n  lint:\n    desc: "Check inputs\n      El archivo no existe"\n    cmds: [true]\n'  # language-data: rejection fixture
        help_text = "cat 1>&2 <<'HELP'\nEl archivo no existe\nHELP\n"  # language-data: rejection fixture
        for name, source in (("scripts/check-example.sh", mixed), ("scripts/check-example.sh", argument),
                             ("scripts/check-example.mjs", javascript), ("Taskfile.yml", quoted),
                             ("scripts/check-example.sh", help_text)):
            with self.subTest(name=name, source=source):
                self.tooling_tree()
                self.write(name, source)
                result = self.check("--public-tooling", self.root)
                self.assertEqual(result.returncode, 1, result.stdout + result.stderr)

    def test_public_tooling_checks_quoted_yaml_commands_and_commented_blocks(self):
        command = "jobs:\n  lint:\n    steps:\n      - run: 'echo El archivo no existe'\n"  # language-data: rejection fixture
        block = "tasks:\n  lint:\n    desc: >- # Summary\n      El archivo no existe\n    cmds: [true]\n"  # language-data: rejection fixture
        for name, source in ((".github/workflows/check.yml", command), ("Taskfile.yml", block)):
            with self.subTest(name=name):
                self.tooling_tree()
                self.write(name, source)
                result = self.check("--public-tooling", self.root)
                self.assertEqual(result.returncode, 1, result.stdout + result.stderr)

    def test_public_tooling_requires_a_complete_successful_export_manifest(self):
        self.tooling_tree()
        exporter = self.root / "scripts" / "export-public.sh"
        exporter.write_text("#!/bin/sh\nexit 2\n", encoding="utf-8")
        result = self.check("--public-tooling", self.root)
        self.assertEqual(result.returncode, 2, result.stdout + result.stderr)
        required = ["Taskfile.yml", ".githooks/pre-push", ".github/workflows/check.yml", "scripts/check-example.sh"]
        for names in (required[1:], required + ["../outside.sh"], required):
            exporter.write_text("#!/bin/sh\nprintf '%s\\n' " + " ".join("'" + name + "'" for name in names) + "\n")
            result = self.check("--public-tooling", self.root)
            self.assertEqual(result.returncode, 0 if names == required else 2, result.stdout + result.stderr)

    def test_public_tooling_refuses_incomplete_and_symlinked_inputs(self):
        result = self.check("--public-tooling", self.root)
        self.assertEqual(result.returncode, 2, result.stdout + result.stderr)
        self.tooling_tree()
        self.write("outside.sh", 'echo "Clean"\n')
        (self.root / "scripts/check-example.sh").unlink()
        (self.root / "scripts/check-example.sh").symlink_to(self.root / "outside.sh")
        result = self.check("--public-tooling", self.root)
        self.assertEqual(result.returncode, 2, result.stdout + result.stderr)

    def test_public_tooling_rejects_unaccented_received_label(self):
        labels = ("Recibido", "Remedio", "corriendo como root", "se informa, no se cobra", "se define dos veces", "bloque", "omitido", "NO parsean")  # language-data: rejection vocabulary
        for label in labels:
            with self.subTest(label=label):
                self.tooling_tree()
                self.write("scripts/check-example.sh", f'echo "{label}: $*" >&2\n')
                result = self.check("--public-tooling", self.root)
                self.assertEqual(result.returncode, 1, result.stdout + result.stderr)
                self.assertIn("scripts/check-example.sh:1", result.stdout)

    def test_public_tooling_checks_check_reporter_verdicts(self):
        self.tooling_tree()
        source = '#!/bin/sh\ncheck "an unreadable release is NO HE PODIDO MIRAR" "three verdicts" $?\n'  # language-data: rejection fixture
        self.write("scripts/check-example.sh", source)
        result = self.check("--public-tooling", self.root)
        self.assertEqual(result.returncode, 1, result.stdout + result.stderr)
        self.assertIn("scripts/check-example.sh:2", result.stdout)

        source = '#!/bin/sh\ncheck bash -c \'case "$1" in *"dejó 1 entrada"*) exit 0;; *) exit 1;; esac\' _ "$out"\n'  # language-data: matcher fixture
        self.write("scripts/check-example.sh", source)
        result = self.check("--public-tooling", self.root)
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)


    def test_inline_machine_heading_key_is_preserved(self):
        self.tooling_tree()
        source = "#!/bin/sh\nsay 'repair: add `Carril:` followed by a nonempty owner identifier.' >&2\n"  # language-data: machine-key fixture
        self.write("scripts/check-example.sh", source)
        for arguments in (("--public-tooling", self.root), ("scripts/check-example.sh",)):
            with self.subTest(arguments=arguments):
                result = self.check(*arguments)
                self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
        messages = "`Carril:` el archivo no existe\n`el archivo no existe`"  # language-data: rejection fixtures
        for message in messages.splitlines():
            with self.subTest(message=message):
                self.write("scripts/check-example.sh", "#!/bin/sh\nsay '" + message + "' >&2\n")
                for arguments in (("--public-tooling", self.root), ("scripts/check-example.sh",)):
                    result = self.check(*arguments)
                    self.assertEqual(result.returncode, 1, result.stdout + result.stderr)

    def test_spanish_comment_is_rejected(self):
        source = "# El archivo no existe.\n"  # language-data: rejection fixture
        self.write("scripts/example.sh", source)
        result = self.check("scripts/example.sh")
        self.assertEqual(result.returncode, 1, result.stderr)
        self.assertIn("scripts/example.sh:1", result.stdout)

    def test_locale_and_unicode_data_are_retained(self):
        locale = '{"title": "Configuración"}\n'  # language-data: locale fixture
        source = 'const input = "mañana"; // language-data: Unicode fixture\n'  # language-data: fixture
        self.write("web/src/features/example/i18n/es.json", locale)
        self.write("web/src/example.test.ts", source + "// Temporal workflow test.\n")
        result = self.check("web/src/features/example/i18n/es.json", "web/src/example.test.ts")
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
        self.assertEqual((self.root / "web/src/example.test.ts").read_text(),
                         source + "// Temporal workflow test.\n")

    def test_locale_source_keeps_data_but_checks_diagnostics(self):
        data = 'const name = "Español"; // language-data: language name\n'  # language-data: fixture
        self.write("web/src/lib/i18n/index.ts", data)
        result = self.check("web/src/lib/i18n/index.ts")
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
        message = 'console.error("Falló"); // language-data: fixture\n'  # language-data: rejection
        self.write("web/src/lib/i18n/index.ts", data + message)
        result = self.check("web/src/lib/i18n/index.ts")
        self.assertEqual(result.returncode, 1, result.stderr)
        self.assertIn("web/src/lib/i18n/index.ts:2", result.stdout)

    def test_diagnostic_cannot_use_the_data_exception(self):
        source = 't.Fatal("Falló la tarea") // language-data: fixture\n'  # language-data: rejection fixture
        self.write("core/example_test.go", source)
        result = self.check("core/example_test.go")
        self.assertEqual(result.returncode, 1, result.stderr)

    def test_changed_lines_reject_new_spanish_without_hiding_existing_debt(self):
        original = "// El archivo no existe.\nvar count = 1\n"  # language-data: existing debt
        self.write("core/example.go", original)
        self.git("init", "-q", "-b", "main")
        self.git("add", ".")
        self.git("commit", "-qm", "base")
        self.write("core/example.go", original.replace("count = 1", "count = 2"))
        result = self.check("--base", "HEAD")
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
        result = self.check("core/example.go")
        self.assertEqual(result.returncode, 1, result.stderr)
        added = "// La tarea falla.\n"  # language-data: rejection fixture
        self.write("core/example.go", original + added)
        result = self.check("--base", "HEAD")
        self.assertEqual(result.returncode, 1, result.stderr)
        self.assertIn("core/example.go:3", result.stdout)

    def test_spanish_test_name_is_rejected(self):
        source = "func TestArchivo(t *testing.T) {}\n"  # language-data: rejection fixture
        self.write("core/example_test.go", source)
        result = self.check("core/example_test.go")
        self.assertEqual(result.returncode, 1, result.stdout + result.stderr)

    def test_ci_output_and_workflow_text_are_checked(self):
        output = 'echo "Ejecución falló" # language-data: fixture\n'  # language-data: rejection
        workflow = "jobs:\n  build:\n    name: Validación del archivo\n"  # language-data: rejection
        self.write("scripts/test-output.sh", output)
        self.write(".github/workflows/example.yml", workflow)
        result = self.check("scripts/test-output.sh", ".github/workflows/example.yml")
        self.assertEqual(result.returncode, 1, result.stderr)
        self.assertIn("scripts/test-output.sh:1", result.stdout)
        self.assertIn(".github/workflows/example.yml:3", result.stdout)

    def test_data_marker_cannot_hide_comments_or_product_messages(self):
        comment = '// "archivo" // language-data: fixture\n'  # language-data: rejection fixture
        message = 'const message = "Falló"; // language-data: fixture\n'  # language-data: rejection
        self.write("core/example_test.go", comment)
        self.write("web/src/example.ts", message)
        result = self.check("core/example_test.go", "web/src/example.ts")
        self.assertEqual(result.returncode, 1, result.stderr)
        self.assertIn("core/example_test.go:1", result.stdout)
        self.assertIn("web/src/example.ts:1", result.stdout)

    def test_english_locale_is_checked(self):
        locale = '{"title": "Configuración"}\n'  # language-data: rejection fixture
        self.write("web/src/features/example/i18n/en.json", locale)
        result = self.check("web/src/features/example/i18n/en.json")
        self.assertEqual(result.returncode, 1, result.stderr)

    def test_generated_locale_assets_are_exempt_in_both_modes(self):
        # The console build generates the embedded bundle under core/internal/webui/dist;
        # it is not committed, so the regression synthesizes its actual layout: hashed
        # asset names holding translated values. Only that generated tree is exempt.
        asset = '{\n  "login": {\n    "title": "Configuración",\n    "error": "El archivo no existe"\n  }\n}\n'  # language-data: generated locale fixture
        self.write("README.md", "Language check fixture.\n")
        self.git("init", "-q", "-b", "main")
        self.git("add", ".")
        self.git("commit", "-qm", "base")
        names = ["core/internal/webui/dist/assets/auth-" + suffix + ".json"
                 for suffix in ("B_HmHY_p", "1ct8euev")]
        for name in names:
            self.write(name, asset)
        result = self.check(*names)
        self.assertEqual(result.returncode, 0, result.stdout.splitlines()[-1:] + [result.stderr])
        for paths in (["./" + name for name in names], [self.root / name for name in names]):
            with self.subTest(paths=paths):
                result = self.check(*paths)
                self.assertEqual(result.returncode, 0, result.stdout.splitlines()[-1:] + [result.stderr])
        self.git("add", ".")
        result = self.check("--base", "HEAD")
        self.assertEqual(result.returncode, 0, result.stdout.splitlines()[-1:] + [result.stderr])
        source = '// El archivo no existe.\n'  # language-data: rejection fixture
        for sibling, text in (("core/internal/webui/dist-other/example.ts", source),
                              ("web/dist/assets/auth-B_HmHY_p.json", asset)):
            with self.subTest(sibling=sibling):
                self.write(sibling, text)
                result = self.check(sibling)
                self.assertEqual(result.returncode, 1, result.stderr)

    def test_data_marker_does_not_hide_quoted_inline_comments(self):
        plain = 'value = 1 # "archivo" # language-data: fixture\n'  # language-data: rejection fixture
        data = 'value = "mañana" # language-data: fixture # "archivo"\n'  # language-data: rejection fixture
        fake = 'value = "plain // language-data: fixture" # "archivo"\n'  # language-data: rejection fixture
        for source in (plain, data, fake):
            with self.subTest(source=source):
                self.write("scripts/test-example.py", source)
                result = self.check("scripts/test-example.py")
                self.assertEqual(result.returncode, 1, result.stderr)
                self.assertIn("scripts/test-example.py:1", result.stdout)

    def test_data_marker_does_not_hide_multiline_call_arguments(self):
        title = 'it(\n  "El archivo no existe", // language-data: fixture\n  () => {},\n);\n'  # language-data: rejection fixture
        diagnostic = 'console.error(\n  "Falló", // language-data: fixture\n);\n'  # language-data: rejection fixture
        for source in (title, diagnostic):
            with self.subTest(source=source):
                self.write("web/src/example.test.ts", source)
                result = self.check("web/src/example.test.ts")
                self.assertEqual(result.returncode, 1, result.stderr)
                self.assertIn("web/src/example.test.ts:2", result.stdout)

    def test_data_marker_does_not_hide_executable_string_values(self):
        template = 'const input = `${console.error("Falló")}`; // language-data: fixture\n'  # language-data: rejection
        command = 'input="$(printf \'Falló\')" # language-data: fixture\n'  # language-data: rejection
        for name, source in (("web/src/example.test.ts", template), ("scripts/test-example.sh", command)):
            with self.subTest(name=name):
                self.write(name, source)
                result = self.check(name)
                self.assertEqual(result.returncode, 1, result.stderr)
                self.assertIn(name + ":1", result.stdout)

    def test_data_marker_does_not_hide_call_assignment_arguments(self):
        keyword = 'logging.error(\n  msg="Falló" # language-data: fixture\n)\n'  # language-data: rejection
        assignment = 'console.error(\n  msg="Falló" // language-data: fixture\n)\n'  # language-data: rejection
        for name, source in (("scripts/test-example.py", keyword), ("web/src/example.test.ts", assignment)):
            with self.subTest(name=name):
                self.write(name, source)
                result = self.check(name)
                self.assertEqual(result.returncode, 1, result.stderr)
                self.assertIn(name + ":2", result.stdout)

    def test_changed_lines_use_git_lf_line_numbers(self):
        self.write("README.md", "Language check fixture.\n")
        self.git("init", "-q", "-b", "main")
        self.git("add", ".")
        self.git("commit", "-qm", "base")
        ending = "continuation\n# El archivo no existe.\n"  # language-data: rejection
        for separator in ("\u2028", "\r"):
            with self.subTest(separator=separator):
                source = "# English" + separator + ending
                self.write("scripts/example.py", source)
                self.git("add", ".")
                result = self.check("--base", "HEAD")
                self.assertEqual(result.returncode, 1, result.stderr)
                self.assertIn("scripts/example.py:2", result.stdout)

    def test_python_assignment_exception_requires_aligned_line_numbers(self):
        tail = '  msg="Falló" # language-data: fixture\n)\n'  # language-data: rejection
        first = '# English\rinput="plain"\rlogging.error(\n'
        second = '# English\r# continuation\rinput="plain"\nlogging.error(\n'
        for prefix, number in ((first, 2), (second, 3)):
            with self.subTest(number=number):
                self.write("scripts/test-example.py", prefix + tail)
                result = self.check("scripts/test-example.py")
                self.assertEqual(result.returncode, 1, result.stderr)
                self.assertIn(f"scripts/test-example.py:{number}", result.stdout)
        data = 'input="mañana" # language-data: fixture\r\n'  # language-data: fixture
        self.write("scripts/test-example.py", data)
        result = self.check("scripts/test-example.py")
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)

    def test_changed_lines_treat_file_names_as_literal_paths(self):
        self.write("README.md", "Language check fixture.\n")
        self.git("init", "-q", "-b", "main")
        self.git("add", ".")
        self.git("commit", "-qm", "base")
        source = "# El archivo no existe.\n"  # language-data: rejection
        self.write(":(literal)example.py", source)
        self.git("add", ".")
        result = self.check("--base", "HEAD")
        self.assertEqual(result.returncode, 1, result.stderr)
        self.assertIn(":(literal)example.py:1", result.stdout)

    def test_missing_base_and_unreadable_source_are_unavailable(self):
        self.write("core/example.go", "var count = 1\n")
        self.git("init", "-q", "-b", "main")
        self.git("add", ".")
        self.git("commit", "-qm", "base")
        result = self.check("--base", "missing-base")
        self.assertEqual(result.returncode, 2, result.stderr)
        self.assertIn("UNAVAILABLE", result.stderr)
        self.write("scripts/bad.sh", "").write_bytes(b"\xff")
        result = self.check("scripts/bad.sh")
        self.assertEqual(result.returncode, 2, result.stderr)
        result = self.check("scripts/missing.sh")
        self.assertEqual(result.returncode, 2, result.stderr)

    def test_newline_filename_is_checked_and_reported_without_split_records(self):
        self.write("core/example.go", "var count = 1\n")
        self.git("init", "-q", "-b", "main")
        self.git("add", ".")
        self.git("commit", "-qm", "base")
        source = "# El archivo no existe.\n"  # language-data: rejection fixture
        self.write("scripts/new\nname.sh", source)
        self.git("add", ".")
        result = self.check("--base", "HEAD")
        self.assertEqual(result.returncode, 1, result.stderr)
        self.assertIn(r"scripts/new\nname.sh:1", result.stdout)

    def test_script_without_extension_and_normalized_accents_are_checked(self):
        source = "#!/bin/sh\n# sen\u0303al\n"  # language-data: rejection fixture
        self.write("scripts/example", source)
        result = self.check("scripts/example")
        self.assertEqual(result.returncode, 1, result.stderr)
        self.assertIn("scripts/example:2", result.stdout)

    def test_go_workspace_file_is_checked(self):
        # go.work ships in the public export and carries comments; a .work suffix
        # must not hide it from the gate (issue #590).
        source = "// El archivo no existe.\nuse (\n\t./core\n)\n"  # language-data: rejection fixture
        self.write("go.work", source)
        result = self.check("go.work")
        self.assertEqual(result.returncode, 1, result.stderr)
        self.assertIn("go.work:1", result.stdout)

    def test_diff_metadata_words_inside_source_are_not_binary_headers(self):
        self.write("scripts/example.py", "print('ready')\n")
        self.git("init", "-q", "-b", "main")
        self.git("add", ".")
        self.git("commit", "-qm", "base")
        self.write("scripts/example.py", "print('Binary files a/example and b/example differ')\n")
        result = self.check("--base", "HEAD")
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)

    def test_binary_source_is_unavailable(self):
        self.write("scripts/example.py", "").write_bytes(b"x\0y")
        self.git("init", "-q", "-b", "main")
        self.git("add", ".")
        self.git("commit", "-qm", "base")
        self.write("scripts/example.py", "print('ready')\n")
        result = self.check("--base", "HEAD")
        self.assertEqual(result.returncode, 2, result.stderr)
        self.assertIn("UNAVAILABLE", result.stderr)

    def public_docs(self):
        self.write("docs-site/src/site-locales.mjs",
                   "export const PUBLISHED_LOCALES = ['es', 'fr', 'zh-cn'];\n")
        return self.check("--public-docs", self.root)

    def test_public_docs_reject_markdown_mdx_and_static_text(self):
        spanish = "El archivo no existe.\n"  # language-data: rejection fixture
        correction = "Lo que si es cierto es que update nunca genera uno.\n"  # language-data: rejection fixture
        manifest = '{"route":"pestaña"}\n'  # language-data: rejection fixture
        annotated = 'const text = "El archivo no existe"; // language-data: fixture\n'  # language-data: rejection fixture
        for name, text in (("docs/integrations/test-example.md", spanish),
                           ("docs-site/src/content/docs/reference/connectors.md", spanish),
                           ("docs/CLI-VERB-PARITY.md", correction),
                           ("docs-site/src/content/docs/start/index.mdx", spanish),
                           ("docs-site/public/robots.txt", "# " + spanish),
                           ("docs-site/public/_redirects", "# " + spanish),
                           ("docs-site/public/testdata/robots.txt", "# " + spanish),
                           ("modules/example/testdata/README.md", spanish),
                           ("docs-site/public/console/manifest.json", manifest),
                           ("docs/test-example.md", annotated)):
            with self.subTest(name=name):
                path = self.write(name, text)
                result = self.public_docs()
                self.assertEqual(result.returncode, 1, result.stdout + result.stderr)
                self.assertIn(name + ":1:", result.stdout)
                path.unlink()

    def test_public_docs_preserve_translations_names_and_binary_assets(self):
        spanish = "El archivo no existe.\n"  # language-data: locale fixture
        for name in ("README.es.md", "docs-site/src/content/docs/es/start.md",
                     "docs-site/src/content/docs/fr/2026-06/start.mdx",
                     "docs-site/src/content/docs/zh-cn/start.md"):
            self.write(name, spanish)
        names = "[Español](README.es.md), Diátaxis, Unicode `é`.\n"  # language-data: names fixture
        self.write("README.md", names)
        self.write("docs-site/public/favicon.png", "").write_bytes(b"\x89PNG\0\xff")
        result = self.public_docs()
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
        self.assertIn("checked 1 file(s)", result.stdout)
        for name in ("docs-site/src/content/docs/es-other/start.md",
                     "docs-site/src/content/docs/2026-06/start.md"):
            with self.subTest(name=name):
                path = self.write(name, spanish)
                result = self.public_docs()
                self.assertEqual(result.returncode, 1, result.stdout + result.stderr)
                self.assertIn(name + ":1:", result.stdout)
                path.unlink()

    def test_public_docs_fail_closed_on_missing_inputs_and_invalid_text(self):
        result = self.check("--public-docs", self.root / "missing")
        self.assertEqual(result.returncode, 2, result.stderr)
        self.assertIn("UNAVAILABLE", result.stderr)
        self.write("docs/guide.md", "English documentation.\n")
        result = self.check("--public-docs", self.root)
        self.assertEqual(result.returncode, 2, result.stderr)
        self.assertIn("UNAVAILABLE", result.stderr)
        self.write("docs-site/public/robots.txt", "").write_bytes(b"\xff")
        result = self.public_docs()
        self.assertEqual(result.returncode, 2, result.stderr)
        self.assertIn("UNAVAILABLE", result.stderr)

    def test_public_docs_do_not_follow_symlinks(self):
        self.write("docs/guide.md", "English documentation.\n")
        (self.root / "docs/link.md").symlink_to("/nonexistent/private.md")
        result = self.public_docs()
        self.assertEqual(result.returncode, 2, result.stderr)
        self.assertIn("UNAVAILABLE", result.stderr)

    def test_public_docs_require_valid_locales_and_nonempty_scope(self):
        self.write("docs/guide.md", "English documentation.\n")
        for value in ("undefined", "{}", "[]", "['en']", "['../es']", "[1]"):
            with self.subTest(value=value):
                self.write("docs-site/src/site-locales.mjs",
                           f"export const PUBLISHED_LOCALES = {value};\n")
                result = self.check("--public-docs", self.root)
                self.assertEqual(result.returncode, 2, result.stderr)
                self.assertIn("UNAVAILABLE", result.stderr)
        (self.root / "docs/guide.md").unlink()
        result = self.public_docs()
        self.assertEqual(result.returncode, 2, result.stderr)
        self.assertIn("no public documentation inputs", result.stderr)

    def test_public_docs_refuse_locale_module_and_directory_symlinks(self):
        self.write("docs/guide.md", "English documentation.\n")
        self.public_docs()
        module = self.root / "docs-site/src/site-locales.mjs"
        module.unlink()
        module.symlink_to("/nonexistent/private.mjs")
        result = self.check("--public-docs", self.root)
        self.assertEqual(result.returncode, 2, result.stderr)
        self.assertIn("locales through a symlink", result.stderr)
        module.unlink()
        self.public_docs()
        (self.root / "docs/linked").symlink_to(self.root / "docs", target_is_directory=True)
        result = self.check("--public-docs", self.root)
        self.assertEqual(result.returncode, 2, result.stderr)
        self.assertIn("directory symlink", result.stderr)


if __name__ == "__main__":
    unittest.main()
