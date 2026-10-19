#!/usr/bin/env python3
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
"""CI owns behavior batteries; release owns artifact facts and required qualification."""
from pathlib import Path
import re
import shlex
import sys

import yaml

ROOT = Path(__file__).resolve().parents[1]


def steps(workflow):
    return [step for job in workflow['jobs'].values() for step in job.get('steps', [])]


def commands(body):
    # Inspect command positions, not comments or echo strings naming a battery.
    lexer = shlex.shlex(body.replace('\\\n', ' '), posix=True, punctuation_chars=';&|\n')
    lexer.whitespace = ' \t\r'
    lexer.whitespace_split = True
    command = []
    for token in lexer:
        if all(char in ';|&\n' for char in token):
            if command:
                yield from unwrap(command)
            command = []
        else:
            command.append(token)
    if command:
        yield from unwrap(command)


def unwrap(command):
    while command and (command[0] in ('env', 'command', 'exec', '!', 'if', 'then', 'do', '{')
                       or re.match(r'^[A-Za-z_][A-Za-z0-9_]*=', command[0])):
        command = command[1:]
    if command and command[0] == 'timeout':
        command = command[1:]
        while command and (command[0].startswith('-') or re.fullmatch(r'[0-9.]+[smhd]?', command[0])):
            command = command[1:]
    if command and command[0] in ('bash', 'sh') and '-c' in command:
        index = command.index('-c')
        if index + 1 < len(command):
            yield from commands(command[index + 1])
    elif command:
        yield command


def check(release, ci):
    ci_tasks = {target for workflow in ci for step in steps(workflow)
                for command in commands(step.get('run', '')) if command[0] == 'task'
                for target in command[1:] if not target.startswith('-')}
    errors = []
    for step in steps(release):
        for command in commands(step.get('run', '')):
            if command[:2] == ['go', 'test']:
                errors.append('release must not run go test')
            if command[0] == 'task' and ci_tasks.intersection(command[1:]):
                errors.append('release duplicates a CI task: ' + ' '.join(command))
            args = [arg for arg in command[1:] if not arg.startswith('-')]
            script = args[0] if command[0] in ('bash', 'sh', 'python3') and args else command[0]
            if script.removeprefix('./').startswith('scripts/test-'):
                errors.append('offline release battery belongs in CI: ' + script)
    gates = release['jobs']['goreleaser']['steps']
    for workflow, flag in (('race-full.yml', []), ('release-first-hour.yml', ['--tree-equal-ok'])):
        expected = f'bash scripts/require-green-run.sh {workflow} "$GITHUB_SHA"' + ''.join(' ' + arg for arg in flag)
        matching = [step for step in gates if step.get('run', '').strip() == expected]
        if (len(matching) != 1 or matching[0].get('continue-on-error', False)
                or 'if' in matching[0] or release['jobs']['goreleaser'].get('continue-on-error', False)):
            errors.append('missing or advisory release evidence gate: ' + workflow)
    return errors


def main():
    try:
        files = ['release.yml', 'pr-ci.yml', 'mainline-ci.yml']
        workflows = [yaml.safe_load((ROOT / '.github/workflows' / name).read_text()) for name in files]
        errors = check(workflows[0], workflows[1:])
    except (OSError, ValueError, TypeError, KeyError, AttributeError, yaml.YAMLError) as error:
        print('release gate: COULD NOT CHECK: ' + str(error), file=sys.stderr)
        return 2
    for error in errors:
        print('release gate: FAIL: ' + error, file=sys.stderr)
    if not errors:
        print('release gate: PASS — CI owns batteries; release requires qualification')
    return int(bool(errors))


if __name__ == '__main__':
    sys.exit(main())
