#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only

# Run from the heavy-job builder with the exact Community and assembled Business trees.
# Both commercial tag sets must compile against the same shared archive/export source.
set -euo pipefail
if [ "$#" -ne 2 ]; then
  echo 'usage: test-s3archive-edition-pair.sh COMMUNITY_TREE BUSINESS_TREE' >&2
  exit 2
fi
community=$(cd -- "$1" && pwd)
business=$(cd -- "$2" && pwd)
(cd -- "$community" && go test -p 2 ./cmd/olivares -run '^TestCommunityS3Archive' -count=1 -v)
(cd -- "$business" && go test -p 2 -tags enterprise ./cmd/olivares -run '^Test(S3ArchiveUnavailableWithoutRegulatedPack|EnterpriseEditionFillsEveryPort)$' -count=1 -v)
(cd -- "$business" && go test -p 2 -tags enterprise,addon_reg ./cmd/olivares -run '^Test(BusinessS3Archive|EnterpriseEditionFillsEveryPort)' -count=1 -v)
# Full-runtime CLI probes boot once per process to preserve the license-source guard.
(cd -- "$business" && go test -p 2 -tags enterprise,addon_reg ./cmd/olivares -run '^TestAuditArchiveExportVerifyCLI$' -count=1 -v)
(cd -- "$business" && go test -p 2 -tags enterprise,addon_reg ./cmd/olivares -run '^TestAuditVerifyReadsWithoutRuntimeBoot$' -count=1 -v)
