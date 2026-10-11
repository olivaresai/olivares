#!/usr/bin/env python3
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
"""Check artifact consumers before allowing evidence uploads to be non-blocking."""

import fnmatch
from pathlib import Path
import re
import sys

import yaml

# These outputs are delivered to operators, rather than another job in this run.
DELIVERABLES = {
    ('docs-site-artifact.yml', 'docs-site-dist-${{ github.sha }}'),
    ('appliance-image.yml', 'appliance-image-artifacts-${{ github.run_id }}-${{ github.run_attempt }}'),
    ('security-feed.yml', 'security-feed'),
}
PRESERVE = './.github/actions/ci-evidence'
VEHICLE = 'python3 ci-evidence-vehicle/.github/actions/ci-evidence/preserve.py'


def required_upload(workflow: dict, filename: str, name: str) -> bool:
    if (filename, name) in DELIVERABLES:
        return True
    for job in workflow['jobs'].values():
        for step in job.get('steps', []):
            if not step.get('uses', '').startswith('actions/download-artifact@'):
                continue
            inputs = step.get('with', {})
            target = inputs.get('name')
            if target:
                match = re.fullmatch(r'\$\{\{\s*needs\.([\w-]+)\.outputs\.([\w-]+)\s*\}\}', target)
                if match:
                    target = workflow['jobs'][match[1]]['outputs'][match[2]]
                if target == name:
                    return True
            elif fnmatch.fnmatchcase(name, inputs.get('pattern', '*')):
                return True
    return False


def helper_available(steps: list[dict], action: str) -> bool:
    path = 'ci-evidence-vehicle' if action == VEHICLE else ''
    for step in steps:
        if not step.get('uses', '').startswith('actions/checkout@'):
            continue
        inputs = step.get('with', {})
        if inputs.get('path', '').rstrip('/') != path:
            continue
        if inputs.get('ref') not in (None, '${{ github.sha }}', '${{ github.workflow_sha }}',
                                     '${{ github.event.pull_request.head.sha }}'):
            continue
        sparse = inputs.get('sparse-checkout')
        if sparse and '.github/actions/ci-evidence' not in sparse.splitlines():
            continue
        return True
    return False


def check(workflow: dict, filename: str) -> list[str]:
    errors = []
    for job, config in workflow['jobs'].items():
        steps = config.get('steps', [])
        for index, step in enumerate(steps):
            if not step.get('uses', '').startswith('actions/upload-artifact@'):
                continue
            label = f"{filename}:{job}:{step['with']['name']}"
            if required_upload(workflow, filename, step['with']['name']):
                if step.get('continue-on-error') is True:
                    errors.append(f'{label}: required payload upload must remain blocking')
                continue
            if step.get('continue-on-error') is not True:
                errors.append(f'{label}: evidence upload needs continue-on-error: true')
            if 'always()' not in step.get('if', ''):
                errors.append(f'{label}: evidence upload needs if: always()')
            previous = steps[index - 1] if index else {}
            transport = previous.get('uses', previous.get('run', ''))
            copied = (previous.get('with') if transport == PRESERVE else
                      {key: previous.get('env', {}).get(env)
                       for key, env in (('name', 'EVIDENCE_NAME'), ('path', 'EVIDENCE_PATHS'))})
            conditions = (step.get('if'), f"{step.get('if')} && runner.environment == 'self-hosted'")
            if (transport not in (PRESERVE, VEHICLE) or
                    previous.get('continue-on-error') is not True or
                    previous.get('if') not in conditions or
                    copied != {k: step['with'][k] for k in ('name', 'path')}):
                errors.append(f'{label}: preserve the same evidence before upload, with the same condition')
            if not helper_available(steps[:index - 1], transport):
                errors.append(f'{label}: evidence helper must be checked out from the workflow revision')
    return errors


def main() -> int:
    directory = Path(sys.argv[1]) if len(sys.argv) > 1 else Path(__file__).resolve().parent.parent / '.github/workflows'
    paths = sorted([*directory.glob('*.yml'), *directory.glob('*.yaml')])
    if not paths:
        print(f'ci-evidence: cannot check: no workflows in {directory}', file=sys.stderr)
        return 2
    try:
        errors = [error for path in paths
                  for error in check(yaml.safe_load(path.read_text()), path.name)]
    except (OSError, yaml.YAMLError, KeyError, TypeError, AttributeError) as error:
        print(f'ci-evidence: cannot check: {error}', file=sys.stderr)
        return 2
    if errors:
        print('\n'.join(errors), file=sys.stderr)
        return 1
    print(f'ci-evidence: PASS ({len(paths)} workflows; evidence non-blocking, required payloads blocking)')
    return 0


if __name__ == '__main__':
    raise SystemExit(main())
