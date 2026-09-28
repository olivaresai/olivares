#!/usr/bin/env python3
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
"""compare_rules.py checks that the compiled SELinux module holds exactly the rows of access.tsv.

cil mode compares the module with the rows. The module and the same module built with generate.py
--declarations-only are converted to CIL (/usr/libexec/selinux/hll/pp); the rules of the first minus those of the
second are the rules the rows wrote, and they must equal the row rules atom for atom: one permission of one rule,
with its condition. The declarations and the Fedora macros they call are in both and cancel. The one interface row
stands for the rules its Fedora interface writes (INTERFACE_RULES). A rule in the module that no row asks for, or a
row the module does not hold, fails.

policy mode checks the installed policy with setools (the library sesearch uses): every row, the macro rows
included, is granted (a conditional row with its boolean on), every type transition and the boolean's default are
present, the interface row's rules, transition and role are there, a row kind the check does not read is refused, and no access of denied.tsv is granted in any state the check evaluates: the policy's defaults, and each
boolean the module declares at true and at false. Conditional rules count in the states where their expression
puts them in force; a denied row may name the one state it is allowed in (allowed_when); a condition that cannot be
evaluated is refused.

Usage:
  compare_rules.py cil --module MODULE.cil --baseline DECLARATIONS.cil [--access access.tsv]
  compare_rules.py policy --policy /etc/selinux/targeted/policy/policy.N [--access access.tsv] [--denied denied.tsv]
Exit 0: equal. Exit 1: a difference, each printed. Exit 2: usage or unreadable input.
"""
import argparse
import os
import re
import sys

HERE = os.path.dirname(os.path.abspath(__file__))
sys.path.insert(0, HERE)

import generate  # noqa: E402

RULE_HEADS = ("allow", "auditallow", "dontaudit", "neverallow", "allowx", "auditallowx", "dontauditx",
              "neverallowx", "typetransition", "typechange", "typemember")
BLOCK_HEADS = ("optional", "block", "in", "tunableif")
DENIED_COLUMNS = ("id", "source", "target", "class", "perm", "allowed_when", "reason")
LOGIN = "unconfined_t"
# What Fedora 44's unconfined_run_to(domain, entry) writes (unconfineduser.if:179-188): domtrans_pattern from the login
# domain (misc_patterns.spt: mmap_exec_file_perms on the entry, the transition, the type transition, and the new
# domain's fd use, inherited fifo and sigchld back to the login), the role, and userdom_use_user_terminals
# (userdomain.if:4158-4165, rw_term_perms). (source, target, class, permissions); {d} and {e} are the arguments.
INTERFACE_RULES = {
    "unconfined_run_to": {
        "allow": [(LOGIN, "{e}", "file", "getattr open map read execute ioctl"),
                  (LOGIN, "{d}", "process", "transition"),
                  ("{d}", LOGIN, "fd", "use"),
                  ("{d}", LOGIN, "fifo_file", "getattr read write append ioctl lock"),
                  ("{d}", LOGIN, "process", "sigchld"),
                  ("{d}", "user_tty_device_t", "chr_file", "getattr lock read write append ioctl open"),
                  ("{d}", "user_devpts_t", "chr_file", "getattr lock read write append ioctl open")],
        "type_transition": [(LOGIN, "{e}", "process", "{d}")],
        "role": [("unconfined_r", "{d}")],
    },
}


def interface_rules(r):
    """The rules an interface row stands for, its two arguments filled in."""
    spec = INTERFACE_RULES.get(r["perms"])
    if spec is None:
        raise ValueError(f"{r['id']}: no rules are known for interface {r['perms']}")
    fill = lambda x: x.format(d=r["source"], e=r["target"])  # noqa: E731
    return ([tuple(fill(x) for x in rule) for rule in spec["allow"]],
            [tuple(fill(x) for x in rule) for rule in spec["type_transition"]],
            [tuple(fill(x) for x in rule) for rule in spec["role"]])


# --- CIL ----------------------------------------------------------------------------------------------------
def parse_cil(text):
    """Returns the list of top-level S-expressions of a CIL text; atoms are strings, quoted strings keep quotes."""
    tokens = re.findall(r'"[^"]*"|[()]|[^\s()";]+|;[^\n]*', text)
    stack, top = [], []
    for tok in tokens:
        if tok.startswith(";"):
            continue
        if tok == "(":
            stack.append([])
        elif tok == ")":
            if not stack:
                raise ValueError("unbalanced ) in CIL")
            node = stack.pop()
            (stack[-1] if stack else top).append(node)
        else:
            (stack[-1] if stack else top).append(tok)
    if stack:
        raise ValueError("unbalanced ( in CIL")
    return top


def _expr(node):
    if isinstance(node, list):
        if len(node) == 1:
            return _expr(node[0])
        return "(" + " ".join(_expr(n) for n in node) + ")"
    return node


def cil_atoms(nodes, condition=""):
    """The rules of CIL nodes as atoms: (kind, source, target, class, permission, condition) for access rules,
    (type_transition, source, target, class, name, new type, condition) for type transitions."""
    atoms = set()
    for node in nodes:
        if not isinstance(node, list) or not node:
            continue
        head = node[0]
        if head in ("booleanif", "tunableif") and len(node) >= 3:
            expr = _expr(node[1])
            for branch in node[2:]:
                if isinstance(branch, list) and branch and branch[0] in ("true", "false"):
                    atoms |= cil_atoms(branch[1:], f"{expr}:{branch[0]}")
            continue
        if head in BLOCK_HEADS:
            atoms |= cil_atoms(node[1:], condition)
            continue
        if head not in RULE_HEADS:
            continue
        if head == "typetransition":
            if len(node) == 6:
                _, s, t, c, name, new = node
                atoms.add(("type_transition", s, t if t != "self" else s, c, name.strip('"'), new, condition))
            elif len(node) == 5:
                _, s, t, c, new = node
                atoms.add(("type_transition", s, t if t != "self" else s, c, "", new, condition))
            continue
        if len(node) != 4:
            atoms.add((head, "unparsed", repr(node), "", "", condition))
            continue
        _, s, t, perms = node
        t = s if t == "self" else t
        if isinstance(perms, list) and len(perms) == 2 and isinstance(perms[1], list):
            for p in perms[1]:
                atoms.add((head, s, t, perms[0], p, condition))
        else:
            atoms.add((head, s, t, "named", _expr(perms), condition))
    return atoms


def row_atoms(rows):
    """The atoms the row rules must produce; rows written by a Fedora macro are left to the policy check."""
    atoms = set()
    for r in rows:
        if r["via"] or r["kind"] == "boolean":
            continue
        if r["kind"] == "interface":
            allows, transitions, _ = interface_rules(r)
            for s, t, c, perms in allows:
                atoms |= {("allow", s, t, c, p, "") for p in perms.split()}
            atoms |= {("type_transition", s, t, c, "", new, "") for s, t, c, new in transitions}
            continue
        cond = f"{r['condition']}:true" if r["condition"] else ""
        if r["kind"] == "allow":
            for p in r["perms"].split():
                atoms.add(("allow", r["source"], r["target"], r["class"], p, cond))
        else:
            parts = r["perms"].split(" ", 1)
            name = parts[1].strip('"') if len(parts) == 2 else ""
            atoms.add(("type_transition", r["source"], r["target"], r["class"], name, parts[0], cond))
    return atoms


def compare_cil(module_text, baseline_text, rows):
    """Returns (extra, missing): atoms in the module that neither the declarations nor a row account for, and
    row atoms the module lacks."""
    module = cil_atoms(parse_cil(module_text))
    baseline = cil_atoms(parse_cil(baseline_text))
    rows_ = row_atoms(rows)
    return module - baseline - rows_, rows_ - module


def describe(atom):
    if atom[0] == "type_transition":
        _, s, t, c, name, new, cond = atom
        text = f"type_transition {s} {t}:{c} {new}" + (f' "{name}"' if name else "")
    else:
        kind, s, t, c, p, cond = atom
        text = f"{kind} {s} {t}:{c} {p}"
    return text + (f" under {cond}" if cond else "")


# --- the installed policy -------------------------------------------------------------------------------------
class SetoolsView:
    """The installed policy through setools. Imported only in policy mode: the hosted container has it."""

    def __init__(self, path):
        import setools  # pylint: disable=import-outside-toplevel
        self.setools = setools
        self.policy = setools.SELinuxPolicy(path)

    def _rules(self, ruletype, source, target, cls, **kw):
        query = self.setools.TERuleQuery(self.policy, ruletype=(ruletype,), source=source, target=target,
                                         tclass=(cls,), **kw)
        return query.results()

    def rules(self, source, target, cls):
        """Every allow rule that applies to source, target and class, conditional or not, as setools returns it."""
        return list(self._rules("allow", source, target, cls))

    def has_transition(self, source, target, cls, name, new_type, condition):
        for rule in self._rules("type_transition", source, target, cls, default=new_type):
            if str(getattr(rule, "filename", "") or "") != name:
                continue
            cond, _ = condition_of(rule)
            if bool(condition) == (cond is not None):
                return True
        return False

    def role_has_type(self, role, type_):
        for r in self.policy.roles():
            if str(r) == role:
                return type_ in {str(t) for t in r.types()}
        return False

    def boolean_default(self, name):
        for b in self.policy.bools():
            if str(b) == name:
                return bool(b.state)
        return None


# --- evaluating conditional rules ---------------------------------------------------------------------------
class Unevaluable(Exception):
    """A rule's condition names a boolean without a known value, or is an expression without evaluate()."""


def condition_of(rule):
    """Returns (conditional expression, block) of a rule, or (None, None) for an unconditional rule. setools raises
    RuleNotConditional when an unconditional rule's .conditional is read; rule facts may also carry None."""
    try:
        cond = rule.conditional
    except Exception:  # pylint: disable=broad-except
        return None, None
    if cond is None:
        return None, None
    return cond, bool(rule.conditional_block)


def enabled(rule, value_of):
    """Whether the rule is in force when each boolean has the value value_of gives it: an unconditional rule always;
    a conditional rule when its expression's truth equals the block it sits in."""
    cond, block = condition_of(rule)
    if cond is None:
        return True
    names = sorted(str(b) for b in cond.booleans)
    values = {}
    for name in names:
        value = value_of(name)
        if value is None:
            raise Unevaluable(" ".join(names))
        values[name] = bool(value)
    if hasattr(cond, "evaluate"):
        truth = bool(cond.evaluate(**values))
    elif len(names) == 1:
        truth = values[names[0]]
    else:
        raise Unevaluable(" ".join(names))
    return truth == block


def states(module_booleans):
    """The states the denial check evaluates: the policy's defaults, then each boolean the module declares at true
    and at false, the others at their defaults. Returns (label, overrides) pairs."""
    out = [("the defaults", {})]
    for name in module_booleans:
        out += [(f"{name}=true", {name: True}), (f"{name}=false", {name: False})]
    return out


def value_in(view, overrides):
    return lambda name: overrides[name] if name in overrides else view.boolean_default(name)


def grants(view, source, target, cls, perm, value_of):
    """Whether some rule in force grants perm; only rules that carry perm are evaluated."""
    return any(perm in {str(p) for p in rule.perms} and enabled(rule, value_of)
               for rule in view.rules(source, target, cls))


def unconditional_perms(view, source, target, cls):
    perms = set()
    for rule in view.rules(source, target, cls):
        if condition_of(rule)[0] is None:
            perms |= {str(p) for p in rule.perms}
    return perms


ALLOWED_WHEN = re.compile(r"^([a-z0-9_]+)=(true|false)$")


def compare_policy(view, rows, denied):
    failures = []
    module_booleans = [r["source"] for r in rows if r["kind"] == "boolean"]
    for r in rows:
        if r["kind"] == "boolean":
            default = view.boolean_default(r["source"])
            if default is None:
                failures.append(f"{r['id']}: the policy has no boolean {r['source']}")
            elif default != (r["perms"] == "true"):
                failures.append(f"{r['id']}: boolean {r['source']} defaults to {str(default).lower()}, the row says "
                                f"{r['perms']}")
        elif r["kind"] == "allow":
            for p in r["perms"].split():
                if r["condition"]:
                    try:
                        ok = grants(view, r["source"], r["target"], r["class"], p,
                                    value_in(view, {r["condition"]: True}))
                    except Unevaluable:
                        ok = False
                else:
                    ok = p in unconditional_perms(view, r["source"], r["target"], r["class"])
                if not ok:
                    failures.append(f"{r['id']}: the policy does not grant {p} on {r['target']}:{r['class']}"
                                    + (f" under {r['condition']}" if r["condition"] else ""))
        elif r["kind"] == "type_transition":
            parts = r["perms"].split(" ", 1)
            name = parts[1].strip('"') if len(parts) == 2 else ""
            if not view.has_transition(r["source"], r["target"], r["class"], name, parts[0], r["condition"]):
                failures.append(f"{r['id']}: the policy has no type_transition {r['source']} {r['target']}:{r['class']} "
                                f"{r['perms']}")
        elif r["kind"] == "interface":
            allows, transitions, roles = interface_rules(r)
            for s, t, c, perms in allows:
                held = unconditional_perms(view, s, t, c)
                for p in perms.split():
                    if p not in held:
                        failures.append(f"{r['id']}: the policy does not grant {p} on {t}:{c} to {s} ({r['perms']})")
            for s, t, c, new in transitions:
                if not view.has_transition(s, t, c, "", new, ""):
                    failures.append(f"{r['id']}: the policy has no type_transition {s} {t}:{c} {new} ({r['perms']})")
            for role, t in roles:
                if not view.role_has_type(role, t):
                    failures.append(f"{r['id']}: role {role} does not hold {t} ({r['perms']})")
        else:
            failures.append(f"{r['id']}: rule kind {r['kind']!r} is not one this check reads; refused")
    for d in denied:
        allowed_when = d.get("allowed_when", "")
        allowed = ALLOWED_WHEN.match(allowed_when) if allowed_when else None
        if allowed_when and not allowed:
            failures.append(f"{d['id']}: allowed_when {allowed_when!r} is not <boolean>=true or <boolean>=false")
            continue
        reached = []
        try:
            for label, overrides in states(module_booleans):
                value_of = value_in(view, overrides)
                if allowed and value_of(allowed.group(1)) == (allowed.group(2) == "true"):
                    continue
                if grants(view, d["source"], d["target"], d["class"], d["perm"], value_of):
                    reached.append(label)
        except Unevaluable as e:
            failures.append(f"{d['id']}: a rule granting {d['perm']} on {d['target']}:{d['class']} to {d['source']} has "
                            f"a condition this check cannot evaluate ({e}); refused")
            continue
        if reached:
            failures.append(f"{d['id']}: the policy grants {d['perm']} on {d['target']}:{d['class']} to {d['source']} in "
                            f"{', '.join(reached)} ({d['reason']})")
    return failures


def read_denied(path):
    return generate.read_table(path, DENIED_COLUMNS)


def main(argv=None):
    p = argparse.ArgumentParser(prog="compare_rules.py")
    sub = p.add_subparsers(dest="mode", required=True)
    c = sub.add_parser("cil")
    c.add_argument("--module", required=True)
    c.add_argument("--baseline", required=True)
    c.add_argument("--access", default=os.path.join(HERE, "access.tsv"))
    q = sub.add_parser("policy")
    q.add_argument("--policy", required=True)
    q.add_argument("--access", default=os.path.join(HERE, "access.tsv"))
    q.add_argument("--denied", default=os.path.join(HERE, "denied.tsv"))
    try:
        args = p.parse_args(argv)
    except SystemExit as e:
        return 2 if e.code else 0
    try:
        rows = generate.read_table(args.access, generate.ACCESS_COLUMNS)
        if args.mode == "cil":
            with open(args.module, encoding="utf-8") as f:
                module_text = f.read()
            with open(args.baseline, encoding="utf-8") as f:
                baseline_text = f.read()
            extra, missing = compare_cil(module_text, baseline_text, rows)
            for atom in sorted(extra):
                print(f"FAIL in the module but in no row: {describe(atom)}")
            for atom in sorted(missing):
                print(f"FAIL a row the module does not hold: {describe(atom)}")
            held = len(row_atoms(rows)) - len(missing)
            print(f"RESULT {'PASS' if not extra and not missing else 'FAIL'}: {held} row atoms held, "
                  f"{len(extra)} extra, {len(missing)} missing")
            return 1 if extra or missing else 0
        denied = read_denied(args.denied)
        failures = compare_policy(SetoolsView(args.policy), rows, denied)
    except (OSError, ValueError, SystemExit) as e:
        print(f"compare_rules.py: {e}", file=sys.stderr)
        return 2
    for f in failures:
        print(f"FAIL {f}")
    print(f"RESULT {'PASS' if not failures else 'FAIL'}: {len(rows)} rows and {len(denied)} denied accesses checked, "
          f"{len(failures)} failures")
    return 1 if failures else 0


if __name__ == "__main__":
    sys.exit(main())
