#!/usr/bin/env python3
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
"""Keep operation exporters out of the Community source, even behind build tags."""
from pathlib import Path
import unittest

ROOT = Path(__file__).resolve().parents[1]


class CommunityOperationsSource(unittest.TestCase):
    def test_no_external_otel_exporter_in_community_source(self):
        exporters = []
        for source in (ROOT / "core/observability/trace").glob("*.go"):
            if not source.name.endswith("_test.go") and "go.opentelemetry.io/otel/exporters/" in source.read_text():
                exporters.append(str(source.relative_to(ROOT)))
        self.assertEqual(exporters, [], "External telemetry delivery belongs in the private overlay")

    def test_no_push_implementation_in_community_source(self):
        directories = [
            "connectors/" + kind for kind in (
                "siem", "syslog", "splunkhec", "otlplog", "chronicle",
                "datadog", "elastic", "snmp", "filelog", "jira", "pagerduty", "opsgenie",
                "internal/siemfmt", "shared/siemfmt",
            )
        ] + ["web/src/features/posture-export", "web/src/features/observability/business-export"]
        remaining = [str(source.relative_to(ROOT)) for directory in directories
                     for source in (ROOT / directory).rglob("*") if source.is_file()]
        implementations = [
            "modules/siemforward/forwarder.go", "modules/siemforward/renderer.go",
            "modules/posture-export/project.go", "modules/observability/export.go",
            "connectors/servicenow/servicenow.go", "connectors/siemsink/notification.go",
            "cmd/olivares/ledgerforwardpump.go", "cmd/olivares/cmd_posture.go",
        ]
        remaining += [path for path in implementations if (ROOT / path).exists()]
        self.assertEqual(remaining, [], "Push and download implementations must be private")
        self.assertNotIn("Export one trace as OTLP-compatible JSON", (ROOT / "cmd/olivares/cmd_observability.go").read_text())
        self.assertNotIn("exportTrace:", (ROOT / "web/src/features/observability/api.ts").read_text())


if __name__ == "__main__":
    unittest.main()
