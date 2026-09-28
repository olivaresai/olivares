#!/usr/bin/env python3
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
"""generate.py writes the appliance's SELinux policy module from its two rule tables.

access.tsv holds one row per rule: an allow, a type_transition or the boolean, each with its id and reason, and
one interface row: the module's single call of a Fedora interface, whose source and target are its two arguments and
whose perms column is its name.
contexts.tsv holds one row per file or port context, with how the module declares the type. From them this
script writes olivares.te, olivares.fc and olivares.if. Rows whose `via` column names a Fedora macro
(init_daemon_domain and init_nnp_daemon_domain for a service, application_domain for a program users run) are
written as that macro, once per domain; every other row is one rule, preceded by a comment with its id and
reason. The interface row is written inside optional_policy, after the rules. Python's standard library only.

It refuses a table the module must not carry, and then writes nothing: a rule kind other than allow,
type_transition, boolean or interface; a permissive or unconfined term outside the one interface call; an interface
call other than INTERFACES' one, a second one, or one with a condition or via; a permission set that is not plain permission
names; the execution of a generic command type (bin_t, shell_exec_t); a module type nothing declares; a
condition without its boolean; a backquote in text it copies (it would end an m4 quote); a repeated id; a
macro or declaration it does not write; a condition on a row a macro writes (the macro would drop it); a domain
under both a service macro and application_domain.

Usage:
  generate.py [--access FILE] [--contexts FILE] [--out DIR] [--check] [--declarations-only]
    --access, --contexts   the tables (default: access.tsv and contexts.tsv beside this script)
    --out DIR              where the three files are written or compared (default: beside this script)
    --check                compare instead of writing; exit 1 when a committed file differs
    --declarations-only    write the module without the row rules: the baseline the hosted check subtracts
Exit 0: written or equal. Exit 1: a refused table or a differing file. Exit 2: usage or an unreadable table.
"""
import argparse
import os
import re
import sys

MODULE_NAME = "olivares"
MODULE_VERSION = "1.0.0"
HERE = os.path.dirname(os.path.abspath(__file__))

KINDS = ("allow", "type_transition", "boolean", "interface")
# The one Fedora interface call the module makes, with its name and both arguments fixed: the operator's SSH login
# runs the local API's command-line client in its own domain (unconfineduser.if:179-188). It is the only place the
# module may name the login's domain family, and it is written inside optional_policy.
INTERFACES = (("unconfined_run_to", "olivares_cli_t", "olivares_cli_exec_t"),)
MACROS = ("init_daemon_domain", "init_nnp_daemon_domain", "application_domain")
DECLARATIONS = ("init_daemon_domain", "application_domain", "files_type", "files_config_file", "files_security_file",
                "systemd_unit_file", "corenet_port")
FILE_TYPES = ("all", "--", "-d", "-s", "-l", "-p", "-b", "-c")
GENERIC_COMMANDS = ("bin_t", "shell_exec_t")
EXECUTE = ("execute", "execute_no_trans", "entrypoint")
FORBIDDEN = re.compile(r"permissive|unconfined", re.I)
NAME = re.compile(r"^[a-z][a-z0-9_]*$")
# Fedora names some types with capitals (NetworkManager_t, networkmanager.te); the module's own types stay lowercase.
TYPE_NAME = re.compile(r"^[A-Za-z][A-Za-z0-9_]*$")
PORT = re.compile(r"^(tcp|udp) [0-9]+(-[0-9]+)?$")
ACCESS_COLUMNS = ("id", "kind", "source", "target", "class", "perms", "condition", "via", "rationale")
CONTEXTS_COLUMNS = ("id", "path", "file_type", "type", "entry", "declare", "fedora_type", "used_by", "note")

LICENSE = "# SPDX-FileCopyrightText: 2026 Olivares.AI\n# SPDX-License-Identifier: AGPL-3.0-only\n"


class Refused(Exception):
    """A table the module must not carry; each reason names the row."""

    def __init__(self, reasons):
        super().__init__("\n".join(reasons))
        self.reasons = reasons


def read_table(path, columns):
    """Returns the rows of a tab-separated table as dicts of the named columns. Lines starting with # are
    comments; other columns are ignored, so the evidence copies of the tables read the same."""
    try:
        with open(path, encoding="utf-8") as f:
            lines = [line.rstrip("\n") for line in f if line.strip() and not line.startswith("#")]
    except OSError as e:
        raise SystemExit(f"generate.py: cannot read {path}: {e}") from None
    if not lines:
        raise SystemExit(f"generate.py: {path} has no header")
    header = lines[0].split("\t")
    missing = [c for c in columns if c not in header]
    if missing:
        raise SystemExit(f"generate.py: {path} lacks the columns {', '.join(missing)}")
    index = {c: header.index(c) for c in columns}
    rows = []
    for n, line in enumerate(lines[1:], start=2):
        fields = line.split("\t")
        if len(fields) != len(header):
            raise SystemExit(f"generate.py: {path}: data line {n} has {len(fields)} fields, the header {len(header)}")
        rows.append({c: fields[i] for c, i in index.items()})
    return rows


class Module:
    """The module a validated pair of tables describes."""

    def __init__(self, access, contexts):
        self.access, self.contexts = access, contexts
        self.reasons = []
        self.domains = []       # (domain, entry type, [row ids], nnp) in table order
        self.macro = {}         # domain -> the macro that declares it: init_daemon_domain or application_domain
        self.types = []         # (type, declaration, [context ids]) in table order
        self.booleans = {}      # name -> (default, row)
        self.rules = []         # rows written as rules, in table order
        self.interfaces = []    # the interface row, written inside optional_policy
        self._validate()
        if self.reasons:
            raise Refused(self.reasons)

    def refuse(self, rid, reason):
        self.reasons.append(f"{rid}: {reason}")

    # --- validation ---------------------------------------------------------------------------------------
    def _validate(self):
        seen = set()
        for r in self.access:
            rid = r["id"]
            if rid in seen:
                self.refuse(rid, "repeated row id")
            seen.add(rid)
            call = r["kind"] == "interface" and (r["perms"], r["source"], r["target"]) in INTERFACES
            for column in ("kind", "source", "target", "class", "perms", "condition", "via"):
                if FORBIDDEN.search(r[column]) and not (call and column == "perms"):
                    self.refuse(rid, f"{column} names permissive or unconfined outside the one interface call")
            for column in ("rationale",):
                if "`" in r[column]:
                    self.refuse(rid, "a backquote would break the module's m4 quoting")
            if r["kind"] not in KINDS:
                self.refuse(rid, f"rule kind {r['kind']!r} is not allow, type_transition, boolean or interface")
                continue
            if r["kind"] == "interface":
                if not call or r["class"] != "-":
                    name, _, _ = INTERFACES[0]
                    self.refuse(rid, f"interface {r['perms']}({r['source']}, {r['target']}) is not the one call the module "
                                     f"makes, {name}({INTERFACES[0][1]}, {INTERFACES[0][2]})")
                elif r["condition"] or r["via"]:
                    self.refuse(rid, "an interface row carries no condition and no via")
                elif self.interfaces:
                    self.refuse(rid, "a second interface row; the module makes one call")
                else:
                    self.interfaces.append(r)
                continue
            if r["via"] and r["via"] not in MACROS and not FORBIDDEN.search(r["via"]):
                self.refuse(rid, f"via {r['via']} is not a macro the generator writes")
            if r["via"] in MACROS and r["condition"]:
                self.refuse(rid, f"condition {r['condition']} on a row the {r['via']} macro writes; "
                                 "the macro cannot carry a condition")
            if r["kind"] == "boolean":
                if not NAME.match(r["source"]) or r["perms"] not in ("true", "false"):
                    self.refuse(rid, "a boolean row is <name> with the default true or false")
                else:
                    self.booleans[r["source"]] = (r["perms"], r)
                continue
            for column in ("source", "target", "class"):
                self._name(rid, column, r[column], NAME if column == "class" else TYPE_NAME)
            if r["kind"] == "allow":
                perms = r["perms"].split()
                if not perms or any(not NAME.match(p) for p in perms):
                    self.refuse(rid, "permission set is not plain permission names")
                elif (r["target"] in GENERIC_COMMANDS and r["class"] == "file"
                      and any(p in EXECUTE for p in perms)):
                    self.refuse(rid, f"{r['source']} may not execute {r['target']}, a generic command type")
            else:
                parts = r["perms"].split(" ", 1)
                if not TYPE_NAME.match(parts[0]) or (len(parts) == 2 and not re.match(r'^"[^"`\s]+"$', parts[1])):
                    self.refuse(rid, "a type_transition row names one type and at most one quoted object name")
        for r in self.access:
            if r["kind"] != "boolean" and r["condition"] and r["condition"] not in self.booleans:
                self.refuse(r["id"], f"condition {r['condition']} is not a boolean row")
        self._domains()
        self._contexts()
        declared = {d for d, _, _, _ in self.domains} | {e for _, e, _, _ in self.domains} | {t for t, _, _ in self.types}
        for r in self.rules + self.interfaces:
            names = [r["source"], r["target"]]
            if r["kind"] == "type_transition":
                names.append(r["perms"].split(" ", 1)[0])
            for t in names:
                if t.startswith(MODULE_NAME + "_") and t not in declared:
                    self.refuse(r["id"], f"{t} is a module type that nothing declares")

    def _name(self, rid, column, value, pattern):
        """A plain name; a module type must also be lowercase. Returns whether it passed."""
        if value.lower().startswith(MODULE_NAME + "_") and not NAME.match(value):
            self.refuse(rid, f"{column} {value!r} is a module type that is not lowercase")
            return False
        if not pattern.match(value) and not FORBIDDEN.search(value):
            self.refuse(rid, f"{column} {value!r} is not a plain name")
            return False
        return True

    def _domains(self):
        entry, nnp, ids, macros = {}, set(), {}, {}
        for r in self.access:
            if r["kind"] in ("boolean", "interface"):
                continue
            if not r["via"]:
                self.rules.append(r)
                continue
            if r["via"] not in MACROS:
                continue
            if r["via"] == "init_daemon_domain":
                if r["kind"] == "allow" and r["class"] == "file" and "entrypoint" in r["perms"].split():
                    domain = r["source"]
                    entry[domain] = r["target"]
                elif r["kind"] == "allow" and r["source"] == "init_t" and r["class"] == "process":
                    domain = r["target"]
                elif r["kind"] == "type_transition" and r["source"] == "init_t" and r["class"] == "process":
                    domain = r["perms"]
                else:
                    self.refuse(r["id"], "not a row init_daemon_domain writes")
                    continue
            elif r["via"] == "application_domain":
                if r["kind"] == "allow" and r["class"] == "file" and "entrypoint" in r["perms"].split():
                    domain = r["source"]
                    entry[domain] = r["target"]
                else:
                    self.refuse(r["id"], "not a row application_domain writes")
                    continue
            else:
                if r["kind"] == "allow" and r["source"] == "init_t" and r["class"] == "process2":
                    domain = r["target"]
                    nnp.add(domain)
                else:
                    self.refuse(r["id"], "not a row init_nnp_daemon_domain writes")
                    continue
            family = "application_domain" if r["via"] == "application_domain" else "init_daemon_domain"
            if macros.setdefault(domain, family) != family:
                self.refuse(r["id"], f"{domain} is declared by both application_domain and init_daemon_domain")
                continue
            ids.setdefault(domain, []).append(r["id"])
        for domain, rows in ids.items():
            if domain not in entry:
                self.refuse(rows[0], f"{domain} has no entry point row (allow ... file entrypoint)")
                continue
            self.domains.append((domain, entry[domain], rows, domain in nnp))
            self.macro[domain] = macros[domain]

    def _contexts(self):
        seen, declared = set(), {}
        for c in self.contexts:
            cid = c["id"]
            if cid in seen:
                self.refuse(cid, "repeated context id")
            seen.add(cid)
            for column in ("type", "fedora_type", "declare", "entry"):
                if FORBIDDEN.search(c[column]):
                    self.refuse(cid, f"{column} names permissive or unconfined outside the one interface call")
            for column in ("path", "note", "used_by"):
                if "`" in c[column]:
                    self.refuse(cid, "a backquote would break the module's m4 quoting")
            if not self._name(cid, "type", c["type"], TYPE_NAME):
                continue
            if c["entry"] == "fedora":
                continue
            if c["entry"] != "module":
                self.refuse(cid, f"entry {c['entry']!r} is not module or fedora")
                continue
            if c["declare"] not in DECLARATIONS:
                self.refuse(cid, f"declare {c['declare']} is not an interface the generator writes")
                continue
            if c["file_type"] == "port":
                if not PORT.match(c["path"]) or c["declare"] != "corenet_port":
                    self.refuse(cid, "a port row is 'tcp N' or 'udp N' declared by corenet_port")
            elif c["file_type"] not in FILE_TYPES:
                self.refuse(cid, f"file type {c['file_type']!r} is not a file-context type")
            if c["declare"] in ("init_daemon_domain", "application_domain"):
                if c["type"] not in {e for d, e, _, _ in self.domains if self.macro[d] == c["declare"]}:
                    self.refuse(cid, f"{c['type']} is declared by {c['declare']} but no row makes it an entry point")
                continue
            if c["type"] in declared:
                if declared[c["type"]][0] != c["declare"]:
                    self.refuse(cid, f"{c['type']} is declared twice, differently")
                declared[c["type"]][1].append(cid)
                continue
            declared[c["type"]] = (c["declare"], [cid])
            self.types.append((c["type"], c["declare"], declared[c["type"]][1]))

    # --- output -------------------------------------------------------------------------------------------
    def requires(self):
        types, classes = set(), {}
        for r in self.rules:
            names = [r["source"], r["target"]]
            if r["kind"] == "type_transition":
                names.append(r["perms"].split(" ", 1)[0])
            types.update(t for t in names if not t.startswith(MODULE_NAME + "_"))
            perms = r["perms"].split() if r["kind"] == "allow" else []
            classes.setdefault(r["class"], set()).update(perms)
        lines = [f"\ttype {t};" for t in sorted(types)]
        for cls in sorted(classes):
            perms = sorted(classes[cls])
            if not perms:
                raise Refused([f"class {cls} is used only by a type_transition; the module needs an allow on it"])
            lines.append(f"\tclass {cls} {{ {' '.join(perms)} }};")
        return "gen_require(`\n" + "\n".join(lines) + "\n')\n"

    @staticmethod
    def rule(r):
        if r["kind"] == "type_transition":
            parts = r["perms"].split(" ", 1)
            name = f" {parts[1]}" if len(parts) == 2 else ""
            return f"type_transition {r['source']} {r['target']}:{r['class']} {parts[0]}{name};"
        target = "self" if r["target"] == r["source"] else r["target"]
        perms = r["perms"].split()
        perm = perms[0] if len(perms) == 1 else "{ " + " ".join(perms) + " }"
        return f"allow {r['source']} {target}:{r['class']} {perm};"

    def te(self, declarations_only=False):
        out = [LICENSE, "#\n",
               "# The Olivares AI appliance's SELinux policy module. GENERATED by generate.py from access.tsv and\n",
               "# contexts.tsv: change a row there, run python3 appliance/selinux/generate.py and commit both; do not\n",
               "# edit this file. Each rule follows the id of the row that requires it and the row's reason.\n\n",
               f"policy_module({MODULE_NAME}, {MODULE_VERSION})\n\n",
               section("Fedora types and classes the rules use"), self.requires(), "\n",
               section("Declarations")]
        for name, (default, r) in self.booleans.items():
            out.append(f"# {r['id']}: {r['rationale']}\n## <desc>\n##\t<p>\n##\t{r['rationale']}\n##\t</p>\n## </desc>\n")
            out.append(f"gen_tunable({name}, {default})\n\n")
        for domain, entry, rows, nnp in self.domains:
            out.append(f"# {' '.join(rows)}\ntype {domain};\ntype {entry};\n"
                       f"{self.macro[domain]}({domain}, {entry})\n")
            if nnp:
                out.append(f"init_nnp_daemon_domain({domain})\n")
            out.append("\n")
        for t, declaration, ids in self.types:
            out.append(f"# {' '.join(ids)}\ntype {t};\n{declaration}({t})\n\n")
        if not declarations_only:
            out.append(section("Rules, one per row"))
            group = None
            for r in self.rules:
                prefix = r["id"].rsplit("-", 1)[0]
                if prefix != group:
                    group = prefix
                    out.append(f"# ---- {prefix} ----\n\n")
                out.append(f"# {r['id']}: {r['rationale']}\n")
                if r["condition"]:
                    out.append(f"tunable_policy(`{r['condition']}',`\n\t{self.rule(r)}\n')\n\n")
                else:
                    out.append(self.rule(r) + "\n\n")
            for r in self.interfaces:
                out.append(section("The one Fedora interface call"))
                out.append(f"# {r['id']}: {r['rationale']}\noptional_policy(`\n\t{r['perms']}({r['source']}, {r['target']})\n')\n\n")
        return "".join(out).rstrip("\n") + "\n"

    def fc(self):
        out = [LICENSE, "#\n",
               "# File contexts of the Olivares AI appliance's SELinux policy module. GENERATED by generate.py from\n",
               "# contexts.tsv; do not edit. Ports are semanage records written by the package, not file contexts.\n\n"]
        for c in self.contexts:
            if c["entry"] != "module" or c["file_type"] == "port":
                continue
            ftype = "" if c["file_type"] == "all" else c["file_type"] + "\t"
            out.append(f"# {c['id']}: {c['note']}\n{c['path']}\t{ftype}gen_context(system_u:object_r:{c['type']},s0)\n")
        return "".join(out)

    @staticmethod
    def interface():
        return (LICENSE + "#\n# GENERATED by generate.py; do not edit.\n\n"
                "## <summary>Olivares AI appliance: its services, helpers, snapshot core, first boot and product"
                " engine.</summary>\n"
                "## <desc>\n##\t<p>\n##\tThis module exports no interface.\n##\t</p>\n## </desc>\n")


def section(title):
    return f"########################################\n#\n# {title}\n#\n\n"


def main(argv=None):
    p = argparse.ArgumentParser(prog="generate.py", description="Write the appliance's SELinux policy module.")
    p.add_argument("--access", default=os.path.join(HERE, "access.tsv"))
    p.add_argument("--contexts", default=os.path.join(HERE, "contexts.tsv"))
    p.add_argument("--out", default=HERE)
    p.add_argument("--check", action="store_true")
    p.add_argument("--declarations-only", action="store_true")
    try:
        args = p.parse_args(argv)
    except SystemExit as e:
        return 2 if e.code else 0
    try:
        access = read_table(args.access, ACCESS_COLUMNS)
        contexts = read_table(args.contexts, CONTEXTS_COLUMNS)
    except SystemExit as e:
        print(e, file=sys.stderr)
        return 2
    try:
        module = Module(access, contexts)
        files = {f"{MODULE_NAME}.te": module.te(args.declarations_only), f"{MODULE_NAME}.fc": module.fc(),
                 f"{MODULE_NAME}.if": module.interface()}
    except Refused as e:
        for reason in e.reasons:
            print(f"generate.py: refused: {reason}")
        return 1
    if args.check:
        differ = []
        for name, text in files.items():
            try:
                with open(os.path.join(args.out, name), encoding="utf-8") as f:
                    if f.read() != text:
                        differ.append(name)
            except OSError:
                differ.append(name)
        for name in differ:
            print(f"generate.py: {name} is not the generator's output; run generate.py and commit it")
        return 1 if differ else 0
    os.makedirs(args.out, exist_ok=True)
    for name, text in files.items():
        with open(os.path.join(args.out, name), "w", encoding="utf-8") as f:
            f.write(text)
    print(f"generate.py: wrote {', '.join(files)} to {args.out}: {len(module.rules)} rules, "
          f"{len(module.domains)} domains, {len(module.types)} declared types")
    return 0


if __name__ == "__main__":
    sys.exit(main())
