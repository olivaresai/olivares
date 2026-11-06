#!/usr/bin/env python3
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repo root.

# Replay the overloaded hosted root shard without running its slow Go tests.
import importlib.util
import json
from pathlib import Path

ROOT = Path(__file__).resolve().parents[1]
helper = importlib.util.spec_from_file_location("test_name_shard", ROOT / "scripts/lib/test_name_shard.py")
sharder = importlib.util.module_from_spec(helper)
helper.loader.exec_module(sharder)

# Hosted race-full run 41335478990, attempts 1 and 2, root-i.
# Upper bounds from consecutive top-level completion timestamps, keeping the slower
# observation. The first completion also includes compilation; retaining that cost
# is conservative. The retry supplies the 37 tests missing from the timed-out run.
COST_SECONDS = {
    "TestARefusedPublicAddressNamesTheSourceAndNotTheValue": 14.495,
    "TestATokenOwnedSubscriptionBlocksReadmissionUntilResolved": 72.496,
    "TestAutomaticProviderCatalogDecodingPaginationAndRedaction": 0.014,
    "TestBootWiresTheGrantListSeam": 0.017,
    "TestBudgetGateAdaptersFailClosedOnError": 0.052,
    "TestBusinessScheduledRunFromAnOlderUnitRefuses": 4.599,
    "TestChatBlockSpoolRefusesAndWritesNothing": 9.167,
    "TestCheckinToolsRejectsDamagedManagedExecutable": 0.812,
    "TestCommunicationContentSealerLoaderAbsentIsInert": 0.058,
    "TestConsoleActivationThatNeedsARestartIsNotAppliedLive": 50.715,
    "TestDRBackupPostgresInTheSplitNamesTheOwnerFlag": 106.632,
    "TestDRDrillDoesNotAnnounceADrillWhoseReportCouldNotBeRendered": 736.156,
    "TestDRDrillPostgresPublishesItsRTONextToSQLite": 1772.557,
    "TestDROfflineBackupAndVerifyDifferentModuleCensus": 90.52,
    "TestDRPayloadListingCopyKeepsMetadataBound": 0.118,
    "TestDRRestoreNamesSealerKeysTheBundleDoesNotCarry": 431.466,
    "TestDRSchedulePumpProbeErrorNeverThrottles": 0.004,
    "TestDirectoryResolverFenceFiresOnARealEpochBump": 0.0,
    "TestDoctorEditionOverridesNamesTheVariableAndNeverItsValue": 0.009,
    "TestEngineLogLevelHonoursTheDocumentedVariable": 0.0,
    "TestGitpublishRejectedIntentRendersAndExitsConflict": 0.093,
    "TestGovernanceRichIsPlainPlusColour": 0.019,
    "TestGrokObserverSurvivesADeadBus": 0.019,
    "TestHandOverRefusesADigestThatIsNotTheArtifacts": 997.82,
    "TestHookPEP_AskDeniesClosedWithoutBridge": 280.866,
    "TestInstalledSessionProgramFallsBackToThePath": 0.001,
    "TestLoadRateLimitConfigDefaults": 0.006,
    "TestMCPEnableKeepsTheServerAndSaysWhenSessionsDoNotUseIt": 0.052,
    "TestMCPManagementRuntimeTestEnableAudienceAndDisable": 8.074,
    "TestMCPTestAndListSayWhyALocalServerFailed": 0.148,
    "TestModuleProfileRefusesAnUnknownModule": 0.0,
    "TestModuleProfileRunsTheSelectionItsRequirementsAndTheKernel": 0.004,
    "TestOperatorConfigLoadersFailClosed": 0.194,
    "TestOperatorInlineJSONConfigFailsClosed": 0.002,
    "TestOwnerStandingReadCannotDelayEmergencyStopCheck": 69.199,
    "TestPdpActiveNeverClaimsGrantsInForceWithoutAnAppliedSnapshot": 0.055,
    "TestPrivilegedCommandsRefuseWithoutAttribution": 361.137,
    "TestProviderAddDeepSeekServiceUsesTheExistingCredentialInput": 0.274,
    "TestPruneOldBundles": 0.008,
    "TestPutAndDeleteSourceApplyLive": 7.783,
    "TestRedactionPlaceholderStillMatchesCatalog": 0.028,
    "TestRetirementLegalHoldStoreNamesNonUserHolds": 73.049,
    "TestRetrievalUpstreamConstruction": 0.002,
    "TestRunLineageRepairCountsAConcurrentWriterAsAConflictOnPostgres": 5.032,
    "TestScopedTransportLoopAndDestructuredWrites": 0.344,
    "TestServeListenerRegistryInvalidAddressRefusesBeforeBinding": 0.003,
    "TestSessionIdleSettingsFailedAuditKeepsThePreviousChoice": 103.498,
    "TestSessionLaunchGate_ContextFailClosedOnReadError": 0.007,
    "TestSessionLaunchPlanBindsRemoteNode": 0.002,
    "TestSessionMCPWorkLeaseEnvelope": 0.007,
    "TestSessionRemoveSaysWhichFlagConfirmsAWorktreeRefusal": 0.13,
    "TestSessionStartFullPrintsTheEnginesRefusal": 0.087,
    "TestSessionStartRefusesWhenTheEditsAndCommandsTemplateCannotBeResolved": 1.601,
    "TestSignInTextBesideTheLinkNamesNoOtherAddress": 0.019,
    "TestSkillsCLIRemovalReadsCurrentVersion": 0.078,
    "TestStatusCommandNeverSendsCredentials": 0.108,
    "TestToolAccountStatusOffersAValidInstallCommand": 0.084,
    "TestToolInstallFromCatalogJourney": 44.238,
    "TestToolsScreensSayAnInstallMayStillRunWhenItsJobCannotBeFollowed": 4.275,
    "TestToolsScreensSayWhenASignInWasLeftBeforeTheEngineAnswered": 3.428,
    "TestUpdateRefusesAnEndpointThatCarriesCredentials": 0.014,
    "TestUpgradeInContainerCheckAndTargetLeaveTheVolumeAlone": 8.434,
    "TestUpgradeRefusesWhenTheTargetIsReplacedMidDownload": 35.569,
    "TestVersionPreservesMonthlyReleaseStamp": 0.018,
    "TestWikiInitAPIKeepsUserSchemaAndSourceBytes": 227.602,
    "TestWireSourcesResolvesStoreReference": 0.003,
    "TestWiredRedactorRemovesTheShapesTheBuiltinScrubMissed": 0.009,
    "TestWorkEventSinkUsesRealDurableEventingIntake": 8.388,
    "TestWorkReplayEventUsesStableAdminRoute": 0.018,
    "TestWorkspaceSnapshotFolderForbid": 50.085
}

spec = json.loads((ROOT / "scripts/race-groups.json").read_text())
names = [s["name"] for s in spec["root_shards"]]
assigned = sharder.assign("race:" + spec["root_package"], names, list(COST_SECONDS))
assert sorted(t for tests in assigned.values() for t in tests) == sorted(COST_SECONDS)
# Reserve another 17 minutes for a cold race build on each new runner.
worst = max(sum(COST_SECONDS[t] for t in tests) / 60 + 17 for tests in assigned.values())
assert worst < 60, f"overloaded root tests still need {worst:.1f} min including build allowance"
print(f"root race budget: all 70 historical tests covered once; worst {worst:.1f} min < 60 min")
