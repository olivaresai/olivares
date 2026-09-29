# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
import copy
import importlib.util
import json
from pathlib import Path
import re
import subprocess
import sys
import tempfile
import unittest
from unittest.mock import patch

HERE=Path(__file__).resolve().parents[2]/"images/toolchain"


class InstallerContract(unittest.TestCase):
    def module(self):
        spec=importlib.util.spec_from_file_location("installer",HERE/"install.py")
        module=importlib.util.module_from_spec(spec);spec.loader.exec_module(module)
        return module

    def lock(self):
        return json.loads((HERE/"input-lock.json").read_text())

    def test_metadata_closure_matches_fixed_interpreter(self):
        self.module().validate(self.lock())

    def test_omitted_transitive_or_typer_refuses(self):
        for name in ('typer','mdurl'):
            lock=self.lock();lock['toolchain']['python']['packages']=[x for x in lock['toolchain']['python']['packages'] if x['name']!=name]
            with self.assertRaises(ValueError):self.module().validate(lock)

    def test_unlisted_source_build_refuses(self):
        lock=self.lock();lock['toolchain']['python']['packages'][1]['selected_artifact']['filename']='typer.tar.gz'
        with self.assertRaises(ValueError):self.module().validate(lock)

    def test_hash_change_refuses_actual_download_bytes(self):
        with tempfile.TemporaryDirectory() as tmp:
            path=Path(tmp)/'wheel';path.write_bytes(b'fixture')
            with self.assertRaises(ValueError):self.module().verify(path,'0'*64)

    # The exact Requires-Dist the first hosted install read from urllib3's wheel METADATA: single
    # quotes and a parenthesized clause, where the lock records the same requirement with double
    # quotes and none. The installer requests no extra, so this dependency is never selected.
    RUNTIME_MARKER = "brotli>=1.2.0; (platform_python_implementation == 'CPython') and extra == 'brotli'"

    def test_the_runtime_marker_of_the_first_hosted_install_classifies(self):
        self.assertIsNone(self.module().dependency(self.RUNTIME_MARKER))

    def test_every_locked_requirement_classifies_in_both_quote_styles(self):
        # Every requires_dist the lock records, as recorded (double quotes), with single quotes,
        # and with each clause before the extra parenthesized, as a wheel's METADATA may write it.
        module = self.module()
        seen = 0
        for row in self.lock()["toolchain"]["python"]["packages"]:
            for declared in row["requires_dist"]:
                requirement, _, marker = declared.partition(";")
                if not marker:
                    self.assertIsNotNone(module.dependency(declared), declared)
                    continue
                self.assertIn('extra == "', marker, declared)
                clauses = marker.strip().split(" and ")
                wrapped = " and ".join(["(" + c.replace('"', "'") + ")" for c in clauses[:-1]] + [clauses[-1].replace('"', "'")])
                for form in (declared, declared.replace('"', "'"), requirement + "; " + wrapped):
                    self.assertIsNone(module.dependency(form), form)
                    seen += 1
        self.assertEqual(seen, 3 * 50)  # the lock records 50 requirements with a marker, 16 without

    def test_a_marker_that_can_select_outside_an_extra_still_refuses(self):
        module = self.module()
        for marker in ('python_version < "3.11"', "platform_python_implementation == 'CPython'",
                       "extra == 'brotli' or python_version < '3.9'", "(extra == 'a' or platform_system == 'Linux')",
                       "python_version < '3.14' or extra == 'zstd'", "extra != 'brotli'", 'extra != "brotli"',
                       "'brotli' == extra", "extra == brotli"):
            with self.assertRaises(ValueError, msg=marker):
                module.dependency("dependency>=1; " + marker)

    def command_in_a_child(self, script):
        # install.command() as a Containerfile RUN step calls it: a child interpreter whose stdout and
        # stderr are the build log. The stub is bash, which command()'s PATH finds in /usr/bin or /bin.
        code = "import sys; sys.path.insert(0, %r); import install; install.command(['bash', '-c', %r])" % (str(HERE), script)
        return subprocess.run([sys.executable, "-c", code], capture_output=True, text=True, timeout=60)

    def test_a_failed_command_ends_the_log_with_its_command_exit_and_bounded_output(self):
        # The evidence keeps only the tail of the build log, so the diagnosis must be the tail: the
        # command, its exit code and the last 2048 bytes of each of its streams, then one line naming
        # the command and the exit, and no traceback after it.
        child = self.command_in_a_child(
            "printf 'the-stdout-line\\n'; head -c 10000 /dev/zero | tr '\\0' x >&2; printf '\\nthe-stderr-line\\n' >&2; exit 1")
        self.assertNotEqual(child.returncode, 0)
        self.assertFalse("Traceback" in child.stderr, "a traceback follows the diagnosis: " + child.stderr[-600:])
        last = child.stderr.strip().splitlines()[-1]
        self.assertIn("bash", last)
        self.assertIn("exit 1", last)
        self.assertIn("stdout (16 bytes", child.stderr)
        self.assertIn("the-stdout-line", child.stderr)
        self.assertIn("stderr (10017 bytes, last 2048 shown)", child.stderr)
        self.assertIn("the-stderr-line", child.stderr)
        self.assertLessEqual(sum(len(run) for run in re.findall("x{64,}", child.stderr)), 2048, "stderr is not bounded")

    def test_a_successful_command_still_shows_its_output_in_the_log(self):
        child = self.command_in_a_child("printf 'visible-output\\n'; printf 'visible-diagnostic\\n' >&2")
        self.assertEqual(child.returncode, 0, child.stderr)
        self.assertIn("visible-output", child.stdout)
        self.assertIn("visible-diagnostic", child.stderr)

    # What kiwi-ng 11.0.4 does with exactly the locked set (measured offline with the 18 locked artifacts and
    # CPython 3.13.5): it prints what was asked and exits 1, because its Cli exits 1 whenever no command ran
    # (kiwi/cli.py, Cli.__init__). The help texts are cut to their first lines, as the real ones begin.
    KIWI_RUNS = {
        ("--version",): (1, b"KIWI (next generation) version 11.0.4\n", b""),
        ("--help",): (1, b" " * 80 + b"\n Usage: kiwi-ng [OPTIONS] COMMAND [ARGS]...     \n\n KIWI - Appliance Builder\n", b""),
        ("system", "build", "--help"): (1, b" " * 80 + b"\n Usage: kiwi-ng system build [OPTIONS]     \n\n Build a system image\n", b""),
    }

    class Stop(Exception):
        pass

    def qualify_with(self, runs):
        """Run qualify() against recorded kiwi-ng runs; stop at the first other command and return the calls."""
        module, calls = self.module(), []

        def fake(argv, **kwargs):
            calls.append([str(a) for a in argv])
            key = tuple(argv[1:]) if str(argv[0]).endswith("/kiwi-ng") else None
            if key not in runs:
                raise self.Stop(argv)
            code, out, err = runs[key]
            if kwargs.get("check") and code:
                raise subprocess.CalledProcessError(code, argv, out, err)
            return subprocess.CompletedProcess(argv, code, out, err)
        with patch.object(module.subprocess, "run", fake), patch.object(module.sys, "stdout"), patch.object(module.sys, "stderr"):
            try:
                module.qualify(self.lock())
            except self.Stop:
                return "reached the next command", calls
            except (SystemExit, subprocess.CalledProcessError, ValueError) as refusal:
                return "refused: " + type(refusal).__name__, calls
        return "finished", calls

    def test_kiwi_informational_commands_are_judged_by_their_output(self):
        outcome, calls = self.qualify_with(self.KIWI_RUNS)
        self.assertEqual(outcome, "reached the next command")
        self.assertEqual([c[1:] for c in calls[:3]], [["--version"], ["--help"], ["system", "build", "--help"]])
        self.assertIn("import kiwi", " ".join(calls[3]))

    def test_a_kiwi_run_with_an_error_a_wrong_version_or_another_exit_is_refused(self):
        version, usage = self.KIWI_RUNS[("--version",)], self.KIWI_RUNS[("--help",)]
        for key, run in ((("--version",), (1, b"", b"KiwiError: something went wrong\n")),
                         (("--version",), (1, version[1], b"a warning\n")),
                         (("--version",), (2, version[1], b"")),
                         (("--version",), (1, b"KIWI (next generation) version 11.0.3\n", b"")),
                         (("--version",), (1, b"KIWI (next generation) version 11.0.4\nmore\n", b"")),
                         (("--help",), (1, b"Error: No such option\n", b"")),
                         (("--help",), (3, usage[1], b"")),
                         (("system", "build", "--help"), (1, usage[1], b""))):
            outcome, calls = self.qualify_with({**self.KIWI_RUNS, key: run})
            self.assertTrue(outcome.startswith("refused"), (key, run, outcome))
            self.assertFalse(any("import kiwi" in " ".join(c) for c in calls), (key, run))

    def test_report_prints_the_version_line_and_the_qualification(self):
        module = self.module()
        with tempfile.TemporaryDirectory() as tmp:
            qualification = Path(tmp) / "qualification.json"
            qualification.write_text('{"kiwi_version": "11.0.4"}\n')
            code, out, err = self.KIWI_RUNS[("--version",)]
            printed = []
            with patch.object(module.subprocess, "run", lambda argv, **kw: subprocess.CompletedProcess(argv, code, out, err)), \
                    patch.object(module.subprocess, "check_output", lambda argv, **kw: (_ for _ in ()).throw(
                        subprocess.CalledProcessError(code, argv, out, err))), \
                    patch("builtins.print", lambda *a, **k: printed.append(" ".join(map(str, a)))):
                module.report(self.lock(), qualification)
        self.assertEqual(printed, ["KIWI (next generation) version 11.0.4", '{"kiwi_version": "11.0.4"}'])

    def test_moved_snapshot_and_root_version_refuse(self):
        for mutate in (lambda x:x['distribution'].update(snapshot_basis='https://deb.debian.org/debian/'),
                       lambda x:x['distribution']['system_packages']['python3'].update(Version='3.14.0')):
            lock=self.lock();mutate(lock)
            with self.assertRaises(ValueError):self.module().validate(lock)
