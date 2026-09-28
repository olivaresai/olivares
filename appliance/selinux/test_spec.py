# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
"""Static checks of olivares-selinux.spec against Fedora's independent-policy packaging and the module's ports.

The spec is read as text: this suite needs no rpm tooling. The hosted workflow builds and installs the package.
Run: python3 -m unittest discover -s appliance/selinux -p 'test_*.py'
"""
import os
import re
import subprocess
import sys
import tempfile
import unittest

HERE = os.path.dirname(os.path.abspath(__file__))
sys.path.insert(0, HERE)

import generate  # noqa: E402

SPEC = os.path.join(HERE, "olivares-selinux.spec")
SECTIONS = ("%description", "%prep", "%build", "%install", "%check", "%pre", "%post", "%preun", "%postun",
            "%pretrans", "%posttrans", "%files", "%changelog")
INSTALLED_MODULE = "%{_datadir}/selinux/packages/%{selinuxtype}/%{modulename}.pp.bz2"
INSTALLED_IF = "%{_datadir}/selinux/devel/include/distributed/%{modulename}.if"
PORT_ADD = re.compile(r"^port (-a|-m) -t ([a-z0-9_]+) -p (tcp|udp) ([0-9]+)$")
PORT_DELETE = re.compile(r"^port -d -p (tcp|udp) ([0-9]+)$")


def read_spec():
    with open(SPEC, encoding="utf-8") as f:
        return f.read()


def split_sections(text):
    """Returns the preamble and a dict of section name -> list of stripped body lines."""
    sections, current, preamble = {}, None, []
    for line in text.splitlines():
        word = line.split(" ", 1)[0]
        if word in SECTIONS:
            current = word
            sections.setdefault(current, [])
            continue
        (sections[current] if current else preamble).append(line.strip())
    return preamble, sections


def guarded(lines, condition):
    """Returns the lines inside `if [ "$1" <test> ]; then` ... its own `fi`, and the lines outside. condition is
    a number (the test -eq N) or a test such as "-ge 2". Nested `if ...; then` lines open a level; a bare `fi`
    closes one; a one-line `if ...; fi` does neither."""
    test = condition if isinstance(condition, str) else f"-eq {condition}"
    inside, outside, depth = [], [], None
    for line in lines:
        if depth is None and line == f'if [ "$1" {test} ]; then':
            depth = 1
            continue
        if isinstance(depth, int):
            if line.startswith("if ") and line.endswith("then"):
                depth += 1
            elif line == "fi":
                depth -= 1
                if depth == 0:
                    depth = "done"
                    continue
            inside.append(line)
            continue
        outside.append(line)
    return inside, outside


def scriptlet(section):
    """The body of a scriptlet as sh runs it, with the RPM macros this suite cannot expand replaced: the
    selinux-policy macros by a no-op, %{_sbindir}/ by nothing (the stub commands are on PATH), %{selinuxtype} by
    targeted."""
    body = []
    for line in read_spec().splitlines()[read_spec().splitlines().index(section) + 1:]:
        if line.split(" ", 1)[0] in SECTIONS:
            break
        if line.startswith("%selinux_"):
            line = ":"
        body.append(line.replace("%{_sbindir}/", "").replace("%{selinuxtype}", "targeted"))
    return "\n".join(body) + "\n"


STUB = """#!/bin/sh
# A stand-in for semanage: port -l -C prints $STUB_DIR/records; import appends its input to $STUB_DIR/imports
# and exits $STUB_IMPORT_RC.
case "$1 $2 $3" in
"port -l -C") cat "$STUB_DIR/records" ;;
import*) cat >>"$STUB_DIR/imports"; exit "${STUB_IMPORT_RC:-0}" ;;
*) echo "unexpected semanage $*" >&2; exit 64 ;;
esac
"""


def run_scriptlet(section, count, records, import_rc=0):
    """Runs a scriptlet with $1 = count against stub semanage and selinuxenabled (SELinux off). Returns (exit,
    stderr, the lines semanage import received or None when it was not called)."""
    with tempfile.TemporaryDirectory() as tmp:
        for name, text in (("semanage", STUB), ("selinuxenabled", "#!/bin/sh\nexit 1\n")):
            path = os.path.join(tmp, name)
            with open(path, "w", encoding="utf-8") as f:
                f.write(text)
            os.chmod(path, 0o755)
        with open(os.path.join(tmp, "records"), "w", encoding="utf-8") as f:
            f.write("".join(f"{t:<30} {p:<8} {n}\n" for t, p, n in records))
        env = dict(os.environ, PATH=tmp + os.pathsep + os.environ.get("PATH", ""), STUB_DIR=tmp,
                   STUB_IMPORT_RC=str(import_rc))
        r = subprocess.run(["sh", "-c", scriptlet(section), "scriptlet", str(count)], env=env, capture_output=True,
                           text=True, check=False, timeout=30)
        imports = os.path.join(tmp, "imports")
        received = None
        if os.path.exists(imports):
            with open(imports, encoding="utf-8") as f:
                received = [line for line in f.read().splitlines() if line.strip()]
        return r.returncode, r.stderr, received


def expected_ports():
    """The port rows of contexts.tsv. semanage port -m changes a record only when the policy holds one for exactly
    that port (fedora_rule exact); a port Fedora covers only by a range, its unreserved range or a narrower one such
    as pki_ca_port_t's 9443-9447 (fedora_rule range), gets its own record with port -a."""
    rows = generate.read_table(os.path.join(HERE, "contexts.tsv"), generate.CONTEXTS_COLUMNS + ("fedora_rule",))
    ports = []
    for r in rows:
        if r["file_type"] != "port":
            continue
        proto, number = r["path"].split(" ")
        op = {"exact": "-m", "range": "-a"}[r["fedora_rule"]]
        ports.append((op, r["type"], proto, number))
    return ports


class Spec(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.text = read_spec()
        cls.preamble, cls.sections = split_sections(cls.text)
        cls.tags = {}
        for line in cls.preamble:
            m = re.match(r"^([A-Za-z0-9()]+):\s*(.+)$", line)
            if m:
                cls.tags.setdefault(m.group(1), []).append(m.group(2).strip())

    def test_names_the_module_and_the_targeted_policy(self):
        self.assertIn("%global modulename olivares", self.preamble)
        self.assertIn("%global selinuxtype targeted", self.preamble)
        self.assertEqual(self.tags["Name"], ["olivares-selinux"])

    def test_version_is_the_modules(self):
        self.assertEqual(self.tags["Version"], [generate.MODULE_VERSION])

    def test_is_noarch_and_licensed_like_the_appliance(self):
        self.assertEqual(self.tags["BuildArch"], ["noarch"])
        self.assertEqual(self.tags["License"], ["AGPL-3.0-only"])

    def test_sources_are_the_three_generated_files(self):
        self.assertEqual([self.tags.get(f"Source{i}", [None])[0] for i in range(3)],
                         ["%{modulename}.te", "%{modulename}.fc", "%{modulename}.if"])

    def test_requires_the_targeted_policy_and_semanage(self):
        self.assertIn("selinux-policy-%{selinuxtype}", self.tags["Requires"])
        self.assertIn("selinux-policy-%{selinuxtype}", self.tags["Requires(post)"])
        self.assertIn("policycoreutils-python-utils", self.tags["Requires(post)"])
        self.assertIn("policycoreutils-python-utils", self.tags["Requires(postun)"])
        self.assertIn("selinux-policy-devel", self.tags["BuildRequires"])
        self.assertIn("%{?selinux_requires_min}", self.preamble)

    def test_builds_with_the_policy_makefile(self):
        build = self.sections["%build"]
        self.assertIn("make -f %{_datadir}/selinux/devel/Makefile %{modulename}.pp", build)
        self.assertIn("bzip2 -9 %{modulename}.pp", build)

    def test_installs_the_module_and_its_interface_file(self):
        install = self.sections["%install"]
        self.assertIn(f"install -D -m 0644 %{{modulename}}.pp.bz2 %{{buildroot}}{INSTALLED_MODULE}", install)
        self.assertIn(f"install -D -p -m 0644 %{{modulename}}.if %{{buildroot}}{INSTALLED_IF}", install)

    def test_scriptlets_use_the_guideline_macros(self):
        self.assertIn("%selinux_relabel_pre -s %{selinuxtype}", self.sections["%pre"])
        self.assertIn(f"%selinux_modules_install -s %{{selinuxtype}} {INSTALLED_MODULE}", self.sections["%post"])
        self.assertIn("%selinux_relabel_post -s %{selinuxtype}", self.sections["%posttrans"])
        inside, _ = guarded(self.sections["%postun"], 0)
        self.assertIn("%selinux_modules_uninstall -s %{selinuxtype} %{modulename}", inside)

    def test_ports_are_recorded_on_first_install_only(self):
        inside, outside = guarded(self.sections["%post"], 1)
        added = [PORT_ADD.match(line).groups() for line in inside if PORT_ADD.match(line)]
        self.assertEqual(sorted(added), sorted(expected_ports()))
        self.assertFalse([line for line in outside if line.startswith("port ")], "a port line outside first install")
        self.assertTrue(any(line.startswith("%{_sbindir}/semanage import -S %{selinuxtype} $reload")
                            for line in inside))

    def test_ports_are_deleted_on_erase_only_and_before_the_module_leaves(self):
        inside, outside = guarded(self.sections["%postun"], 0)
        deleted = [PORT_DELETE.match(line).groups() for line in inside if PORT_DELETE.match(line)]
        self.assertEqual(sorted(deleted), sorted((proto, number) for _, _, proto, number in expected_ports()))
        self.assertFalse([line for line in outside if line.startswith("port ")], "a port line outside erase")
        last_port = max(i for i, line in enumerate(inside) if PORT_DELETE.match(line))
        uninstall = inside.index("%selinux_modules_uninstall -s %{selinuxtype} %{modulename}")
        self.assertLess(last_port, uninstall, "the port records name the module's types, so they leave first")

    def test_a_failed_import_is_named_on_stderr_and_the_scriptlet_still_exits_0(self):
        for section in ("%post", "%postun"):
            imports = [line for line in self.sections[section] if "semanage import" in line]
            with self.subTest(section=section):
                self.assertTrue(imports)
                for line in imports:
                    self.assertRegex(line, r'\|\| echo "olivares-selinux: [^"]+" >&2$')
                    self.assertNotIn("|| :", line)
        for section in ("%pre", "%post", "%postun", "%posttrans"):
            for line in self.sections.get(section, []):
                with self.subTest(section=section, line=line):
                    self.assertIsNone(re.match(r"^exit\b", line), "a scriptlet never fails the transaction")

    def test_an_upgrade_repairs_every_record_and_only_those(self):
        inside, _ = guarded(self.sections["%post"], "-ge 2")
        self.assertIn("records=$(%{_sbindir}/semanage port -l -C 2>/dev/null || :)", inside)
        repairs = [m.groups() for m in (re.match(r"^([a-z0-9_]+) ([0-9]+) port (-a|-m) -t ([a-z0-9_]+) -p (tcp) "
                                                 r"([0-9]+)$", line) for line in inside) if m]
        self.assertTrue(all(t == t2 and n == n2 for t, n, _, t2, _, n2 in repairs), repairs)
        self.assertEqual(sorted((op, t, proto, n) for _, _, op, t, proto, n in repairs), sorted(expected_ports()))

    def test_scriptlets_use_no_percent_sign_but_rpm_macros(self):
        for section in ("%pre", "%post", "%postun", "%posttrans"):
            for line in self.sections.get(section, []):
                if line.startswith("#"):
                    continue
                with self.subTest(section=section, line=line):
                    self.assertIsNone(re.search(r"%(?!\{|selinux_)", line), "rpm would read it as a macro")

    def test_the_scriptlets_parse_as_sh(self):
        for section in ("%pre", "%post", "%postun", "%posttrans"):
            with self.subTest(section=section):
                r = subprocess.run(["sh", "-n", "-c", scriptlet(section)], capture_output=True, text=True,
                                   check=False, timeout=30)
                self.assertEqual(r.returncode, 0, r.stderr)

    def test_semanage_does_not_reload_a_policy_that_is_not_loaded(self):
        for section in ("%post", "%postun"):
            with self.subTest(section=section):
                self.assertIn("if selinuxenabled; then reload=; else reload=-N; fi", self.sections[section])

    def test_no_other_scriptlet_touches_ports(self):
        for section in ("%pre", "%posttrans", "%preun", "%pretrans"):
            for line in self.sections.get(section, []):
                with self.subTest(section=section, line=line):
                    self.assertNotIn("semanage", line)

    def test_files_are_the_module_the_interface_and_the_installed_copy(self):
        files = [line for line in self.sections["%files"] if line]
        self.assertIn(INSTALLED_MODULE, files)
        self.assertIn(INSTALLED_IF, files)
        self.assertIn("%ghost %verify(not md5 size mode mtime) "
                      "%{_sharedstatedir}/selinux/%{selinuxtype}/active/modules/200/%{modulename}", files)

    def test_never_lowers_enforcement(self):
        for word in ("setenforce", "permissive", "audit2allow", "semodule -d", "selinux=0", "enforcing=0"):
            with self.subTest(word=word):
                self.assertNotIn(word, self.text)


class Helpers(unittest.TestCase):
    def test_guarded_splits_the_block_and_the_rest(self):
        inside, outside = guarded(["a", 'if [ "$1" -eq 1 ]; then', "b", "c", "fi", "d"], 1)
        self.assertEqual((inside, outside), (["b", "c"], ["a", "d"]))

    def test_guarded_keeps_a_nested_block_inside(self):
        lines = ['if [ "$1" -ge 2 ]; then', "a", 'if [ -n "$x" ]; then', "b", "fi", "c", "fi", "d"]
        self.assertEqual(guarded(lines, "-ge 2"), (["a", 'if [ -n "$x" ]; then', "b", "fi", "c"], ["d"]))


RECORDS = [("olivares_console_port_t", "tcp", "8443"), ("olivares_grpc_port_t", "tcp", "8444"),
           ("olivares_portal_port_t", "tcp", "9443"), ("olivares_model_runtime_port_t", "tcp", "11434")]


class Scriptlets(unittest.TestCase):
    """The scriptlets themselves, run by sh against stub semanage and selinuxenabled commands."""

    def expected_lines(self, ports):
        return sorted(f"port {op} -t {t} -p {proto} {n}" for op, t, proto, n in ports)

    def test_first_install_records_all_four_ports(self):
        code, err, received = run_scriptlet("%post", 1, [])
        self.assertEqual((code, err), (0, ""))
        self.assertEqual(sorted(received), self.expected_lines(expected_ports()))

    def test_a_failed_first_install_import_is_named_and_exits_0(self):
        code, err, _ = run_scriptlet("%post", 1, [], import_rc=1)
        self.assertEqual(code, 0)
        self.assertIn("olivares-selinux:", err)

    def test_an_upgrade_with_every_record_imports_nothing(self):
        self.assertEqual(run_scriptlet("%post", 2, RECORDS), (0, "", None))

    def test_an_upgrade_adds_only_the_missing_record(self):
        code, err, received = run_scriptlet("%post", 2, [r for r in RECORDS if r[2] != "9443"])
        self.assertEqual((code, err), (0, ""))
        self.assertEqual(received, ["port -a -t olivares_portal_port_t -p tcp 9443"])

    def test_an_upgrade_repairs_a_modified_record_that_went_missing(self):
        code, _, received = run_scriptlet("%post", 3, [r for r in RECORDS if r[2] != "8443"])
        self.assertEqual(code, 0)
        self.assertEqual(received, ["port -m -t olivares_console_port_t -p tcp 8443"])

    def test_a_failed_upgrade_repair_is_named_and_exits_0(self):
        code, err, _ = run_scriptlet("%post", 2, [], import_rc=1)
        self.assertEqual(code, 0)
        self.assertIn("olivares-selinux:", err)

    def test_erase_deletes_all_four_and_an_upgrade_deletes_nothing(self):
        code, err, received = run_scriptlet("%postun", 0, RECORDS)
        self.assertEqual((code, err), (0, ""))
        self.assertEqual(sorted(received), sorted(f"port -d -p {p} {n}" for _, p, n in RECORDS))
        self.assertEqual(run_scriptlet("%postun", 1, RECORDS), (0, "", None))

    def test_a_failed_erase_import_is_named_and_exits_0(self):
        code, err, _ = run_scriptlet("%postun", 0, RECORDS, import_rc=1)
        self.assertEqual(code, 0)
        self.assertIn("olivares-selinux:", err)

    def test_expected_ports_follow_the_fedora_record(self):
        ops = {number: op for op, _, _, number in expected_ports()}
        self.assertEqual(ops, {"8443": "-m", "8444": "-a", "9443": "-a", "11434": "-a"})


if __name__ == "__main__":
    unittest.main()
