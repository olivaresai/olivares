# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
"""The contract of generate.py: the rule tables produce the policy module, and a table the module must not
carry stops the generator.

Run: python3 -m unittest discover -s appliance/selinux -p 'test_*.py'
"""
import contextlib
import io
import os
import subprocess
import sys
import tempfile
import unittest

HERE = os.path.dirname(os.path.abspath(__file__))
sys.path.insert(0, HERE)

import generate  # noqa: E402  (the module under test sits beside this file)

ACCESS_HEADER = "id\tkind\tsource\ttarget\tclass\tperms\tcondition\tvia\trationale\n"
CONTEXTS_HEADER = "id\tpath\tfile_type\ttype\tentry\tdeclare\tfedora_type\tused_by\tnote\n"

# A small, complete pair of tables: one domain with its entry point and NNP transition, a file type, a port
# type, a raw rule, a self rule, a named type transition, the boolean and its conditional rule.
ACCESS = ACCESS_HEADER + "".join("\t".join(r) + "\n" for r in [
    ["D-01", "allow", "init_t", "olivares_d_t", "process", "transition", "", "init_daemon_domain", "PID 1 starts it"],
    ["D-02", "allow", "olivares_d_t", "olivares_d_exec_t", "file", "entrypoint execute getattr map open read", "",
     "init_daemon_domain", "its only entry point"],
    ["D-03", "type_transition", "init_t", "olivares_d_exec_t", "process", "olivares_d_t", "", "init_daemon_domain",
     "the exec transition"],
    ["D-04", "allow", "init_t", "olivares_d_t", "process2", "nnp_transition nosuid_transition", "",
     "init_nnp_daemon_domain", "the unit sets NoNewPrivileges="],
    ["D-05", "allow", "olivares_d_t", "init_t", "fd", "use", "", "", "journal streams PID 1 opened"],
    ["D-06", "allow", "olivares_d_t", "olivares_d_t", "tcp_socket", "accept read", "", "", "serves its listener"],
    ["D-07", "allow", "olivares_d_t", "olivares_d_var_lib_t", "dir", "add_name search write", "", "", "its data"],
    ["D-08", "type_transition", "olivares_d_t", "var_lib_t", "dir", 'olivares_d_var_lib_t "d"', "", "",
     "a directory it creates in /var/lib gets its type"],
    ["D-09", "allow", "olivares_d_t", "olivares_d_port_t", "tcp_socket", "name_bind", "", "", "its private port"],
    ["D-10", "boolean", "olivares_d_jit", "-", "-", "false", "", "", "the JIT switch, off"],
    ["D-11", "allow", "olivares_d_t", "olivares_d_t", "process", "execmem", "olivares_d_jit", "", "the JIT"],
    ["D-12", "allow", "olivares_d_t", "var_lib_t", "dir", "search", "", "", "reaches /var/lib"],
])
CONTEXTS = CONTEXTS_HEADER + "".join("\t".join(r) + "\n" for r in [
    ["C01", "/usr/libexec/d", "--", "olivares_d_exec_t", "module", "init_daemon_domain", "bin_t", "olivares_d_t",
     "entry point"],
    ["C02", "/var/lib/d(/.*)?", "all", "olivares_d_var_lib_t", "module", "files_type", "var_lib_t", "olivares_d_t",
     "its data"],
    ["C03", "tcp 4242", "port", "olivares_d_port_t", "module", "corenet_port", "unreserved_port_t", "olivares_d_t",
     "its port"],
    ["C04", "/usr/libexec", "-d", "bin_t", "fedora", "-", "bin_t", "all", "Fedora's own label"],
])


def run_generator(access, contexts, *extra):
    """Writes the two tables to a scratch directory, runs generate.main and returns (exit, output, files)."""
    with tempfile.TemporaryDirectory() as tmp:
        a, c, out = os.path.join(tmp, "access.tsv"), os.path.join(tmp, "contexts.tsv"), os.path.join(tmp, "out")
        with open(a, "w", encoding="utf-8") as f:
            f.write(access)
        with open(c, "w", encoding="utf-8") as f:
            f.write(contexts)
        buf = io.StringIO()
        with contextlib.redirect_stdout(buf), contextlib.redirect_stderr(buf):
            code = generate.main(["--access", a, "--contexts", c, "--out", out, *extra])
        files = {}
        if os.path.isdir(out):
            for name in sorted(os.listdir(out)):
                with open(os.path.join(out, name), encoding="utf-8") as f:
                    files[name] = f.read()
        return code, buf.getvalue(), files


def with_row(table, row):
    return table + "\t".join(row) + "\n"


class GenerateModule(unittest.TestCase):
    def setUp(self):
        self.code, self.out, self.files = run_generator(ACCESS, CONTEXTS)
        self.assertEqual(self.code, 0, self.out)
        self.te, self.fc, self.iface = self.files["olivares.te"], self.files["olivares.fc"], self.files["olivares.if"]

    def test_writes_the_three_module_files(self):
        self.assertEqual(sorted(self.files), ["olivares.fc", "olivares.if", "olivares.te"])

    def test_module_statement_names_the_module_and_its_version(self):
        self.assertIn("\npolicy_module(olivares, %s)\n" % generate.MODULE_VERSION, self.te)

    def test_domains_use_the_fedora_daemon_macros_with_their_row_ids(self):
        self.assertIn("# D-01 D-02 D-03 D-04\ntype olivares_d_t;\ntype olivares_d_exec_t;\n"
                      "init_daemon_domain(olivares_d_t, olivares_d_exec_t)\n"
                      "init_nnp_daemon_domain(olivares_d_t)\n", self.te)

    def test_macro_rows_are_not_written_twice(self):
        self.assertNotIn("allow init_t olivares_d_t:process transition;", self.te)
        self.assertNotIn("olivares_d_exec_t:file", self.te)

    def test_file_and_port_types_are_declared_from_the_contexts(self):
        self.assertIn("# C02\ntype olivares_d_var_lib_t;\nfiles_type(olivares_d_var_lib_t)\n", self.te)
        self.assertIn("# C03\ntype olivares_d_port_t;\ncorenet_port(olivares_d_port_t)\n", self.te)

    def test_each_rule_follows_its_row_id_and_reason(self):
        self.assertIn("# D-05: journal streams PID 1 opened\nallow olivares_d_t init_t:fd use;\n", self.te)
        self.assertIn("# D-07: its data\nallow olivares_d_t olivares_d_var_lib_t:dir { add_name search write };\n",
                      self.te)

    def test_a_rule_on_its_own_domain_uses_self(self):
        self.assertIn("allow olivares_d_t self:tcp_socket { accept read };\n", self.te)

    def test_a_named_type_transition_keeps_its_object_name(self):
        self.assertIn('type_transition olivares_d_t var_lib_t:dir olivares_d_var_lib_t "d";\n', self.te)

    def test_the_boolean_is_declared_off_and_guards_its_rule(self):
        self.assertIn("gen_tunable(olivares_d_jit, false)\n", self.te)
        self.assertIn("# D-11: the JIT\ntunable_policy(`olivares_d_jit',`\n"
                      "\tallow olivares_d_t self:process execmem;\n')\n", self.te)

    def test_fedora_types_and_classes_are_required_once(self):
        start = self.te.index("gen_require(`")
        block = self.te[start:self.te.index("')", start)]
        self.assertIn("\ttype init_t;\n", block)
        self.assertIn("\ttype var_lib_t;\n", block)
        self.assertIn("\tclass dir { add_name search write };\n", block)
        self.assertIn("\tclass fd { use };\n", block)
        self.assertNotIn("olivares_", block)

    def test_the_file_contexts_carry_the_module_rows_only(self):
        self.assertIn("/usr/libexec/d\t--\tgen_context(system_u:object_r:olivares_d_exec_t,s0)\n", self.fc)
        self.assertIn("/var/lib/d(/.*)?\tgen_context(system_u:object_r:olivares_d_var_lib_t,s0)\n", self.fc)
        self.assertNotIn("4242", self.fc)
        self.assertNotIn("/usr/libexec\t", self.fc)

    def test_the_interface_file_exports_nothing(self):
        self.assertIn("## <summary>", self.iface)
        self.assertNotIn("interface(", self.iface)

    def test_every_file_carries_the_license_header_and_the_generated_notice(self):
        for name, text in self.files.items():
            self.assertTrue(text.startswith("# SPDX-FileCopyrightText: 2026 Olivares.AI\n"
                                            "# SPDX-License-Identifier: AGPL-3.0-only\n"), name)
            self.assertIn("GENERATED by generate.py", text, name)

    def test_generation_is_deterministic(self):
        _, _, again = run_generator(ACCESS, CONTEXTS)
        self.assertEqual(again, self.files)

    def test_declarations_only_drops_every_row_rule(self):
        code, out, files = run_generator(ACCESS, CONTEXTS, "--declarations-only")
        self.assertEqual(code, 0, out)
        te = files["olivares.te"]
        self.assertIn("init_daemon_domain(olivares_d_t, olivares_d_exec_t)", te)
        self.assertIn("gen_tunable(olivares_d_jit, false)", te)
        self.assertNotIn("allow ", te)
        self.assertNotIn("type_transition ", te)
        self.assertNotIn("tunable_policy(", te)
        self.assertEqual(files["olivares.fc"], self.fc)


class RefuseTables(unittest.TestCase):
    def assertRefused(self, access, contexts, reason):
        code, out, files = run_generator(access, contexts)
        self.assertEqual(code, 1, out)
        self.assertIn(reason, out)
        self.assertEqual(files, {}, "a refused table writes nothing")

    def test_a_permissive_row_stops_the_generator(self):
        self.assertRefused(with_row(ACCESS, ["X-01", "permissive", "olivares_d_t", "-", "-", "-", "", "", "no"]),
                           CONTEXTS, "X-01: rule kind 'permissive' is not allow, type_transition, boolean or interface")

    def test_an_unconfined_type_stops_the_generator(self):
        self.assertRefused(with_row(ACCESS, ["X-01", "allow", "init_t", "unconfined_service_t", "process",
                                             "transition", "", "", "no"]),
                           CONTEXTS, "X-01: target names permissive or unconfined")

    def test_a_wildcard_permission_stops_the_generator(self):
        for perms in ("*", "~{ read }", "{ read }", ""):
            with self.subTest(perms=perms):
                self.assertRefused(with_row(ACCESS, ["X-01", "allow", "olivares_d_t", "var_lib_t", "dir", perms,
                                                     "", "", "no"]),
                                   CONTEXTS, "X-01: permission set is not plain permission names")

    def test_executing_a_generic_command_type_stops_the_generator(self):
        for target in ("bin_t", "shell_exec_t"):
            with self.subTest(target=target):
                self.assertRefused(with_row(ACCESS, ["X-01", "allow", "olivares_d_t", target, "file",
                                                     "execute execute_no_trans getattr map open read", "", "",
                                                     "python3 and id"]),
                                   CONTEXTS, f"X-01: olivares_d_t may not execute {target}")

    def test_an_undeclared_module_type_stops_the_generator(self):
        self.assertRefused(with_row(ACCESS, ["X-01", "allow", "olivares_d_t", "olivares_nowhere_t", "file", "read",
                                             "", "", "no"]),
                           CONTEXTS, "X-01: olivares_nowhere_t is a module type that nothing declares")

    def test_a_condition_without_its_boolean_stops_the_generator(self):
        self.assertRefused(with_row(ACCESS, ["X-01", "allow", "olivares_d_t", "var_lib_t", "dir", "read",
                                             "olivares_no_bool", "", "no"]),
                           CONTEXTS, "X-01: condition olivares_no_bool is not a boolean row")

    def test_a_backquote_in_copied_text_stops_the_generator(self):
        self.assertRefused(with_row(ACCESS, ["X-01", "allow", "olivares_d_t", "var_lib_t", "dir", "read", "", "",
                                             "reads `x`"]),
                           CONTEXTS, "X-01: a backquote would break the module's m4 quoting")

    def test_a_repeated_row_id_stops_the_generator(self):
        self.assertRefused(with_row(ACCESS, ["D-05", "allow", "olivares_d_t", "var_lib_t", "dir", "read", "", "",
                                             "again"]),
                           CONTEXTS, "D-05: repeated row id")

    def test_an_unknown_macro_stops_the_generator(self):
        self.assertRefused(with_row(ACCESS, ["X-01", "allow", "init_t", "olivares_d_t", "process", "transition", "",
                                             "unconfined_domain", "no"]),
                           CONTEXTS, "X-01: via names permissive or unconfined")
        self.assertRefused(with_row(ACCESS, ["X-01", "allow", "init_t", "olivares_d_t", "process", "transition", "",
                                             "init_other_domain", "no"]),
                           CONTEXTS, "X-01: via init_other_domain is not a macro the generator writes")

    def test_a_condition_on_a_macro_row_stops_the_generator(self):
        for rid in ("D-01", "D-02", "D-03", "D-04"):
            with self.subTest(row=rid):
                lines = []
                for line in ACCESS.splitlines(keepends=True):
                    f = line.rstrip("\n").split("\t")
                    if f[0] == rid:
                        f[6] = "olivares_d_jit"
                        line = "\t".join(f) + "\n"
                    lines.append(line)
                macro = "init_nnp_daemon_domain" if rid == "D-04" else "init_daemon_domain"
                self.assertRefused("".join(lines), CONTEXTS, f"{rid}: condition olivares_d_jit on a row the {macro} "
                                   "macro writes; the macro cannot carry a condition")

    def test_an_unknown_declaration_stops_the_generator(self):
        self.assertRefused(ACCESS, with_row(CONTEXTS, ["C09", "/x", "--", "olivares_x_t", "module", "files_mystery",
                                                       "etc_t", "-", "no"]),
                           "C09: declare files_mystery is not an interface the generator writes")


# A program a user runs, declared with application_domain: its entry-point row is the only row the macro writes.
APP_ACCESS = with_row(ACCESS, ["E-01", "allow", "olivares_e_t", "olivares_e_exec_t", "file",
                               "entrypoint execute getattr map open read", "", "application_domain", "a program users run"])
APP_CONTEXTS = with_row(CONTEXTS, ["C05", "/usr/bin/e", "--", "olivares_e_exec_t", "module", "application_domain", "bin_t",
                                   "olivares_e_t", "its entry point"])


class ApplicationDomain(unittest.TestCase):
    def assertRefused(self, access, contexts, reason):
        code, out, files = run_generator(access, contexts)
        self.assertEqual(code, 1, out)
        self.assertIn(reason, out)
        self.assertEqual(files, {}, "a refused table writes nothing")

    def test_a_program_users_run_is_declared_with_application_domain(self):
        code, out, files = run_generator(APP_ACCESS, APP_CONTEXTS)
        self.assertEqual(code, 0, out)
        te = files["olivares.te"]
        self.assertIn("# E-01\ntype olivares_e_t;\ntype olivares_e_exec_t;\napplication_domain(olivares_e_t, olivares_e_exec_t)\n", te)
        self.assertNotIn("init_daemon_domain(olivares_e_t", te)
        self.assertNotIn("allow olivares_e_t olivares_e_exec_t", te)
        self.assertIn("/usr/bin/e\t--\tgen_context(system_u:object_r:olivares_e_exec_t,s0)", files["olivares.fc"])

    def test_a_row_application_domain_does_not_write_is_refused(self):
        self.assertRefused(with_row(APP_ACCESS, ["E-02", "allow", "init_t", "olivares_e_t", "process", "transition", "",
                                                 "application_domain", "no"]),
                           APP_CONTEXTS, "E-02: not a row application_domain writes")

    def test_one_domain_under_both_macros_is_refused(self):
        self.assertRefused(with_row(APP_ACCESS, ["E-02", "allow", "init_t", "olivares_e_t", "process", "transition", "",
                                                 "init_daemon_domain", "no"]),
                           APP_CONTEXTS, "E-02: olivares_e_t is declared by both application_domain and init_daemon_domain")

    def test_an_application_domain_context_needs_its_entry_point_row(self):
        self.assertRefused(ACCESS, APP_CONTEXTS,
                           "C05: olivares_e_exec_t is declared by application_domain but no row makes it an entry point")


# The one Fedora interface call the module makes: the operator's login domain runs the CLI in the CLI's domain.
CALL = "unconfined_run_to(olivares_cli_t, olivares_cli_exec_t)"
IF_ACCESS = with_row(with_row(ACCESS, ["K-01", "allow", "olivares_cli_t", "olivares_cli_exec_t", "file",
                                       "entrypoint execute getattr map open read", "", "application_domain", "the CLI"]),
                     ["K-02", "interface", "olivares_cli_t", "olivares_cli_exec_t", "-", "unconfined_run_to", "", "",
                      "the operator's login runs the CLI in its domain"])
IF_CONTEXTS = with_row(CONTEXTS, ["C06", "/usr/bin/cli", "--", "olivares_cli_exec_t", "module", "application_domain",
                                  "bin_t", "olivares_cli_t", "the CLI"])


def replace_field(table, rid, column, value):
    names = ACCESS_HEADER.rstrip("\n").split("\t")
    out = []
    for line in table.splitlines(keepends=True):
        f = line.rstrip("\n").split("\t")
        if f[0] == rid:
            f[names.index(column)] = value
            line = "\t".join(f) + "\n"
        out.append(line)
    return "".join(out)


class InterfaceCall(unittest.TestCase):
    def assertRefused(self, access, contexts, reason):
        code, out, files = run_generator(access, contexts)
        self.assertEqual(code, 1, out)
        self.assertIn(reason, out)
        self.assertEqual(files, {}, "a refused table writes nothing")

    def test_the_one_call_is_written_inside_optional_policy_and_nothing_else(self):
        code, out, files = run_generator(IF_ACCESS, IF_CONTEXTS)
        self.assertEqual(code, 0, out)
        te = files["olivares.te"]
        self.assertIn("# K-02: the operator's login runs the CLI in its domain\noptional_policy(`\n\t" + CALL + "\n')\n", te)
        self.assertEqual(te.count("unconfined"), 1)

    def test_declarations_only_leaves_the_call_out(self):
        code, out, files = run_generator(IF_ACCESS, IF_CONTEXTS, "--declarations-only")
        self.assertEqual(code, 0, out)
        self.assertNotIn("unconfined", files["olivares.te"])

    def test_the_call_naming_another_domain_or_entry_type_is_refused(self):
        for column, value in (("source", "olivares_portal_t"), ("target", "olivares_portal_exec_t")):
            with self.subTest(column=column):
                self.assertRefused(replace_field(IF_ACCESS, "K-02", column, value), IF_CONTEXTS,
                                   "K-02: interface unconfined_run_to(" + (value if column == "source" else "olivares_cli_t")
                                   + ", " + (value if column == "target" else "olivares_cli_exec_t")
                                   + ") is not the one call the module makes, " + CALL)

    def test_another_unconfined_interface_is_refused(self):
        self.assertRefused(replace_field(IF_ACCESS, "K-02", "perms", "unconfined_domain"), IF_CONTEXTS,
                           "K-02: perms names permissive or unconfined outside the one interface call")

    def test_a_second_call_is_refused(self):
        self.assertRefused(with_row(IF_ACCESS, ["K-03", "interface", "olivares_cli_t", "olivares_cli_exec_t", "-",
                                                "unconfined_run_to", "", "", "again"]),
                           IF_CONTEXTS, "K-03: a second interface row; the module makes one call")

    def test_a_condition_or_a_via_on_the_call_is_refused(self):
        for column, value in (("condition", "olivares_d_jit"), ("via", "application_domain")):
            with self.subTest(column=column):
                self.assertRefused(replace_field(IF_ACCESS, "K-02", column, value), IF_CONTEXTS,
                                   "K-02: an interface row carries no condition and no via")

    def test_a_raw_row_of_the_calls_expansion_is_refused(self):
        self.assertRefused(with_row(IF_ACCESS, ["E12-02", "allow", "unconfined_t", "olivares_cli_t", "process",
                                                "transition", "", "", "the login enters the CLI"]),
                           IF_CONTEXTS, "E12-02: source names permissive or unconfined outside the one interface call")
        self.assertRefused(with_row(IF_ACCESS, ["E12-05", "allow", "olivares_cli_t", "unconfined_t", "fd", "use", "", "",
                                                "the shell's descriptors"]),
                           IF_CONTEXTS, "E12-05: target names permissive or unconfined outside the one interface call")

    def test_unconfined_domain_of_an_olivares_domain_is_refused(self):
        self.assertRefused(with_row(IF_ACCESS, ["X-01", "allow", "olivares_cli_t", "olivares_cli_exec_t", "file",
                                                "entrypoint", "", "unconfined_domain", "no"]),
                           IF_CONTEXTS, "X-01: via names permissive or unconfined outside the one interface call")
        self.assertRefused(IF_ACCESS, with_row(IF_CONTEXTS, ["C09", "/x", "--", "olivares_x_t", "module",
                                                             "unconfined_domain", "etc_t", "-", "no"]),
                           "C09: declare names permissive or unconfined outside the one interface call")


class RepositoryModule(unittest.TestCase):
    """The repository's own tables generate a module with every row in it."""

    def test_the_repository_tables_generate(self):
        with open(os.path.join(HERE, "access.tsv"), encoding="utf-8") as f:
            access = f.read()
        with open(os.path.join(HERE, "contexts.tsv"), encoding="utf-8") as f:
            contexts = f.read()
        code, out, files = run_generator(access, contexts)
        self.assertEqual(code, 0, out)
        te = files["olivares.te"]
        for line in access.splitlines():
            if line.startswith("#") or line.startswith("id\t") or not line.strip():
                continue
            with self.subTest(row=line.split("\t")[0]):
                self.assertRegex(te, r"(?m)^# (.* )?%s(:| |$)" % line.split("\t")[0])

    def test_the_command_line_runs_from_the_repository_root(self):
        with tempfile.TemporaryDirectory() as tmp:
            r = subprocess.run([sys.executable, os.path.join(HERE, "generate.py"), "--out", tmp],
                               capture_output=True, text=True, check=False)
            self.assertEqual(r.returncode, 0, r.stdout + r.stderr)
            self.assertEqual(sorted(os.listdir(tmp)), ["olivares.fc", "olivares.if", "olivares.te"])



class RepositoryMutants(unittest.TestCase):
    """Mutants of the repository's own tables that must be refused."""

    def test_portal_01_with_the_operate_runtime_condition_is_refused(self):
        with open(os.path.join(HERE, "access.tsv"), encoding="utf-8") as f:
            access = f.read()
        with open(os.path.join(HERE, "contexts.tsv"), encoding="utf-8") as f:
            contexts = f.read()
        header = next(line for line in access.splitlines() if line.startswith("id\t")).split("\t")
        lines = []
        for line in access.splitlines(keepends=True):
            f = line.rstrip("\n").split("\t")
            if f[0] == "PORTAL-01":
                f[header.index("condition")] = "olivares_operate_runtime"
                line = "\t".join(f) + "\n"
            lines.append(line)
        code, out, files = run_generator("".join(lines), contexts)
        self.assertEqual(code, 1, out)
        self.assertIn("PORTAL-01: condition olivares_operate_runtime on a row the init_daemon_domain macro writes; "
                      "the macro cannot carry a condition", out)
        self.assertEqual(files, {})


class CommittedModule(unittest.TestCase):
    """olivares.te, olivares.fc and olivares.if are committed, and are exactly what the tables generate."""

    def test_the_committed_module_is_the_generators_output(self):
        buf = io.StringIO()
        with contextlib.redirect_stdout(buf), contextlib.redirect_stderr(buf):
            code = generate.main(["--check"])
        self.assertEqual(code, 0, buf.getvalue())


if __name__ == "__main__":
    unittest.main()
