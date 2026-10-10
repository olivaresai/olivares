#!/usr/bin/env python3
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
"""Evidence transport must never change the verdict of a test job."""

from pathlib import Path
import copy
import importlib.util
import os
import subprocess
import sys
import tempfile
import unittest
from types import ModuleType

import yaml

ROOT = Path(__file__).resolve().parent.parent

# Required payloads: later-job downloads or release/build outputs.
REQUIRED = {
    ('main-suite.yml', 'modules'), ('main-suite.yml', 'postgres'),
    ('main-suite.yml', 'invariants'), ('mainline-ci.yml', 'web'),
    ('pr-ci.yml', 'pr-web'), ('race-full.yml', 'setup'),
    ('docs-site-artifact.yml', 'build'), ('security-feed.yml', 'package-and-verify'),
    ('release-candidate.yml', 'build'),
}
REQUIRED_NAMES = {'cli-development-verification',
                  'appliance-release-publication-${{ github.run_id }}-${{ github.run_attempt }}',
                  'appliance-image-artifacts-${{ github.run_id }}-${{ github.run_attempt }}'}


class EvidenceWorkflowTests(unittest.TestCase):
    def test_uploads_expire_after_one_day(self) -> None:
        publication = 'appliance-release-publication-${{ github.run_id }}-${{ github.run_attempt }}'
        uploads = 0
        for path in sorted((ROOT / '.github/workflows').glob('*.y*ml')):
            workflow = yaml.safe_load(path.read_text())
            for job, config in workflow['jobs'].items():
                for step in config.get('steps', []):
                    if not step.get('uses', '').startswith('actions/upload-artifact@'):
                        continue
                    uploads += 1
                    # Enterprise assembly adds the appliance publication workflow.
                    release = (step['with']['name'] == publication and
                               CHECK.required_upload(workflow, path.name, publication))
                    with self.subTest(workflow=path.name, job=job, name=step['with']['name']):
                        self.assertEqual(step['with'].get('retention-days'), 7 if release else 1)
        self.assertGreater(uploads, 0)

    def test_evidence_uploads_do_not_override_test_verdicts(self) -> None:
        evidence = 0
        for path in sorted((ROOT / '.github/workflows').glob('*.y*ml')):
            workflow = yaml.safe_load(path.read_text())
            for job, config in workflow['jobs'].items():
                for step in config.get('steps', []):
                    if not step.get('uses', '').startswith('actions/upload-artifact@'):
                        continue
                    required = ((path.name, job) in REQUIRED or
                                step['with']['name'] in REQUIRED_NAMES)
                    with self.subTest(workflow=path.name, job=job, name=step.get('name')):
                        if required:
                            self.assertIsNot(step.get('continue-on-error'), True)
                        else:
                            evidence += 1
                            self.assertIs(step.get('continue-on-error'), True,
                                          'quota errors must not fail evidence-only jobs')
                            self.assertIn('always()', step.get('if', ''))
        self.assertGreater(evidence, 0)


def load_module(name: str, path: Path) -> ModuleType:
    spec = importlib.util.spec_from_file_location(name, path)
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


CHECK = load_module('ci_evidence_check', ROOT / 'scripts/check-ci-evidence.py')
COPY = load_module('ci_evidence_copy', ROOT / '.github/actions/ci-evidence/preserve.py')


class EvidenceLintTests(unittest.TestCase):
    def setUp(self) -> None:
        self.workflow = yaml.safe_load((ROOT / '.github/workflows/release-first-hour.yml').read_text())

    def test_real_workflow_and_each_missing_transport_guard(self) -> None:
        self.assertEqual(CHECK.check(self.workflow, 'release-first-hour.yml'), [])
        steps = self.workflow['jobs']['release-first-hour']['steps']
        for field in ('continue-on-error', 'if'):
            changed = copy.deepcopy(self.workflow)
            del changed['jobs']['release-first-hour']['steps'][-1][field]
            self.assertTrue(CHECK.check(changed, 'release-first-hour.yml'), field)
        changed = copy.deepcopy(self.workflow)
        del changed['jobs']['release-first-hour']['steps'][-2]
        self.assertTrue(CHECK.check(changed, 'release-first-hour.yml'))
        self.assertEqual(steps[-2]['with'], {k: steps[-1]['with'][k] for k in ('name', 'path')})

    def test_required_payloads_match_download_names_patterns_and_job_outputs(self) -> None:
        for path in sorted((ROOT / '.github/workflows').glob('*.y*ml')):
            filename = path.name
            workflow = yaml.safe_load(path.read_text())
            for job, config in workflow['jobs'].items():
                for step in config.get('steps', []):
                    if not step.get('uses', '').startswith('actions/upload-artifact@'):
                        continue
                    name = step['with']['name']
                    expected = (filename, job) in REQUIRED or name in REQUIRED_NAMES
                    self.assertEqual(CHECK.required_upload(workflow, filename, name), expected, name)
                    if expected:
                        step['continue-on-error'] = True
                        self.assertTrue(CHECK.check(workflow, filename), name)
                        del step['continue-on-error']

    def test_helper_is_available_with_older_target_or_no_target_checkout(self) -> None:
        for path in sorted((ROOT / '.github/workflows').glob('*.y*ml')):
            name = path.name
            workflow = yaml.safe_load(path.read_text())
            self.assertEqual(CHECK.check(workflow, name), [])
            for job, config in workflow['jobs'].items():
                vehicle = next((s for s in config.get('steps', [])
                                if s.get('with', {}).get('path') == 'ci-evidence-vehicle'), None)
                if vehicle:
                    with self.subTest(workflow=name, job=job):
                        original = vehicle['with']['ref']
                        vehicle['with']['ref'] = '${{ inputs.twin_sha }}'
                        self.assertTrue(CHECK.check(workflow, name))
                        vehicle['with']['ref'] = original

    def test_empty_and_invalid_workflows_cannot_pass(self) -> None:
        with tempfile.TemporaryDirectory(dir=ROOT) as directory:
            result = subprocess.run([sys.executable, str(ROOT / 'scripts/check-ci-evidence.py'), directory],
                                    capture_output=True, text=True)
            self.assertEqual(result.returncode, 2)
            Path(directory, 'broken.yaml').write_text('jobs: [')
            result = subprocess.run([sys.executable, str(ROOT / 'scripts/check-ci-evidence.py'), directory],
                                    capture_output=True, text=True)
            self.assertEqual(result.returncode, 2)


class EvidenceCopyTests(unittest.TestCase):
    def test_composite_copies_on_self_hosted_even_after_a_failed_stage(self) -> None:
        action = yaml.safe_load((ROOT / '.github/actions/ci-evidence/action.yml').read_text())
        step = action['runs']['steps'][0]
        self.assertIn('always()', step['if'])
        self.assertIn("runner.environment == 'self-hosted'", step['if'])
        self.assertEqual(step['env']['EVIDENCE_PATHS'], '${{ inputs.path }}')
        self.assertEqual(step['env']['EVIDENCE_NAME'], '${{ inputs.name }}')

    def test_directory_and_multiline_globs_preserve_visible_files_without_symlinks(self) -> None:
        with tempfile.TemporaryDirectory(dir=ROOT) as directory:
            root = Path(directory)
            source = root / 'captures with spaces'
            source.mkdir()
            (source / 'nested').mkdir()
            (source / 'capture.png').write_bytes(b'capture')
            (source / 'nested' / 'engine.log').write_text('redacted log')
            (source / '.key').write_text('private fixture')
            (source / '.hidden').mkdir()
            (source / '.hidden' / 'trace.log').write_text('private fixture')
            (root / 'outside.txt').write_text('private fixture')
            (source / 'link.log').symlink_to(root / 'outside.txt')
            (source / 'linked-directory').symlink_to(root, target_is_directory=True)
            output = root / 'copy'
            self.assertEqual(COPY.preserve(str(source), output), 2)
            self.assertEqual((output / 'nested/engine.log').read_text(), 'redacted log')
            self.assertEqual((output / 'capture.png').read_bytes(), b'capture')
            self.assertEqual(sorted(p.relative_to(output).as_posix() for p in output.rglob('*') if p.is_file()),
                             ['capture.png', 'nested/engine.log'])
            self.assertEqual((output / 'capture.png').stat().st_mode & 0o777, 0o600)
            output2 = root / 'glob-copy'
            self.assertEqual(COPY.preserve(f'{source}/**/*.log\n{source}/*.png', output2), 2)
            self.assertEqual((output2 / 'nested/engine.log').read_text(), 'redacted log')

    def test_missing_evidence_warns_and_copy_errors_propagate(self) -> None:
        with tempfile.TemporaryDirectory(dir=ROOT) as directory:
            root = Path(directory)
            self.assertEqual(COPY.preserve(str(root / 'missing'), root / 'copy'), 0)
            source = root / 'log.txt'
            source.write_text('evidence')
            destination = root / 'not-a-directory'
            destination.write_text('occupied')
            with self.assertRaises(OSError):
                COPY.preserve(str(source), destination)

    def test_summary_reports_the_run_attempt_job_and_artifact_location(self) -> None:
        from unittest.mock import patch
        with tempfile.TemporaryDirectory(dir=ROOT) as directory:
            root = Path(directory)
            source = root / 'log.txt'
            source.write_text('evidence')
            summary = root / 'summary'
            env = {'GITHUB_WORKFLOW_REF': 'owner/repo/.github/workflows/release-first-hour.yml@refs/heads/task',
                   'GITHUB_RUN_ID': '123', 'GITHUB_RUN_ATTEMPT': '2', 'GITHUB_JOB': 'first-hour',
                   'EVIDENCE_NAME': 'captures-variant-1', 'EVIDENCE_PATHS': str(source),
                   'GITHUB_STEP_SUMMARY': str(summary)}
            with patch.dict(os.environ, env), patch.object(COPY.Path, 'home', return_value=root):
                COPY.main()
            expected = root / 'ci-evidence/release-first-hour/123-2/first-hour/captures-variant-1'
            self.assertIn(str(expected), summary.read_text())
            self.assertEqual((expected / 'log.txt').read_text(), 'evidence')


if __name__ == '__main__':
    unittest.main()
