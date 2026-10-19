#!/usr/bin/env python3
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
"""Keep Business implementation (Compliance Packs, fleet managed settings, the inbound SCIM server) out of the Community source distribution."""
from pathlib import Path
import re
import unittest

ROOT = Path(__file__).resolve().parents[1]


class CommunitySourceTests(unittest.TestCase):
    def test_no_business_report_providers_or_pump(self):
        forbidden = re.compile(
            r'func NewStore(?:Scheduler|Branding|CustomTemplates)\(|'
            r'type reportSchedulePump struct|func newReportSchedulePump\('
        )
        leaks = []
        for relative in ('modules/reporting', 'cmd/olivares'):
            for path in (ROOT / relative).glob('*.go'):
                if not path.name.endswith('_test.go') and forbidden.search(path.read_text()):
                    leaks.append(str(path.relative_to(ROOT)))
        self.assertEqual(leaks, [], 'Business report providers and pump belong in the private overlay')

    def test_no_compliance_packs_implementation(self):
        forbidden = re.compile(
            r'var catalog = \[\]Framework\{|func oscalDocument\(|'
            r'func RenderPDF\(|//go:embed templates'
        )
        leaks = []
        for relative in ('compliance.catalog.json', 'modules/reporting/templates', 'modules/reporting/i18n', 'web/src/features/reporting/reporting-view.tsx', 'web/src/features/compliance/capability-catalog.tsx', 'web/src/features/compliance/calendar-view.tsx', 'web/src/features/compliance/hipaa-view.tsx'):
            if (ROOT / relative).exists():
                leaks.append(relative)
        for package in ('compliance', 'reporting'):
            for path in (ROOT / 'modules' / package).rglob('*.go'):
                if path.name.endswith('_test.go'):
                    continue
                if forbidden.search(path.read_text()):
                    leaks.append(str(path.relative_to(ROOT)))
        self.assertEqual(leaks, [], 'paid implementation must live in the private overlay')

    def test_no_fleet_managed_settings_implementation(self):
        forbidden = re.compile(
            r'type PolicyArtifactDistributor struct|type PolicyObservedStore struct|'
            r'func \(c \*PolicyConsole\) handle(?:Publish|ListVersions|GetVersion|Artifact|Checkin|Distribution)\('
        )
        leaks = []
        for path in (ROOT / 'modules' / 'governance').glob('*.go'):
            if not path.name.endswith('_test.go') and forbidden.search(path.read_text()):
                leaks.append(str(path.relative_to(ROOT)))
        self.assertEqual(leaks, [], 'fleet managed settings belong in the private overlay')

    def test_no_inbound_scim_server_implementation(self):
        # The SCIM protocol layer (codecs, filter and PATCH parsers, discovery documents, the
        # Security Event Token codec) and its HTTP handlers are Business; Community keeps the
        # error envelope, the admission and the 501 stubs.
        leaks = []
        for name in ('events', 'filter', 'group', 'grouppatch', 'list', 'patch', 'scim', 'user'):
            path = ROOT / 'core' / 'api' / 'scim' / (name + '.go')
            if path.exists():
                leaks.append(str(path.relative_to(ROOT)))
        for name in ('handlers_scim', 'handlers_scim_groups', 'handlers_scim_events'):
            path = ROOT / 'core' / 'api' / (name + '.go')
            if path.exists():
                leaks.append(str(path.relative_to(ROOT)))
        forbidden = re.compile(r'\bscim\.(?:ParseFilter|ApplyPatch|ApplyGroupPatch|DecodeSET|ParseCompactJWS|EncodeUser|EncodeGroup)\(')
        for path in (ROOT / 'core' / 'api').glob('*.go'):
            if not path.name.endswith('_test.go') and forbidden.search(path.read_text()):
                leaks.append(str(path.relative_to(ROOT)))
        self.assertEqual(leaks, [], 'the inbound SCIM server belongs in the private overlay')


if __name__ == '__main__':
    unittest.main()
