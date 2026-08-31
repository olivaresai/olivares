// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package agent365

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/olivaresai/olivares/connectors/internal/redact"
	"github.com/olivaresai/olivares/sdk"
	"github.com/olivaresai/olivares/sdk/model"
)

const (
	findingBlockedDeployed         = "agent365_blocked_deployed"
	findingExternalBroadDeployment = "agent365_external_broad_deployment"
	findingSubjectKindIdentity     = "identity"
	deploymentStatusSome           = "some"
	deploymentStatusAll            = "all"
	packageTypeExternal            = "external"
	packageTypeShared              = "shared"
)

// Gather emits registry-hygiene findings from the same v1.0 package list used
// for Snapshot. It does not emit EdgeObservations: the roster itself remains
// reference data and travels through Snapshot.
func (s *Source) Gather(ctx context.Context, sink sdk.Sink) error {
	if s.offline() {
		return nil
	}
	client, err := s.graphClient(ctx)
	if err != nil {
		return err
	}
	packages, err := s.listPackages(ctx, client)
	if err != nil {
		return err
	}
	now := s.clock().UTC()
	for _, p := range packages {
		if p.ID == "" {
			continue
		}
		deployedTo := strings.ToLower(p.DeployedTo)
		if p.IsBlocked && (deployedTo == deploymentStatusSome || deployedTo == deploymentStatusAll) {
			if err := sink.Emit(ctx, blockedDeployedFinding(p, deployedTo, now)); err != nil {
				return err
			}
		}
		packageType := strings.ToLower(p.Type)
		if (packageType == packageTypeExternal || packageType == packageTypeShared) && deployedTo == deploymentStatusAll {
			if err := sink.Emit(ctx, externalBroadDeploymentFinding(p, now)); err != nil {
				return err
			}
		}
	}
	return nil
}

func blockedDeployedFinding(p copilotPackage, deployedTo string, now time.Time) model.FindingReport {
	return model.FindingReport{
		Kind:        findingBlockedDeployed,
		Severity:    model.SeverityMedium,
		SubjectKind: findingSubjectKindIdentity,
		SubjectRef:  p.ID,
		Title:       fmt.Sprintf("blocked package still deployed: %s", packageLabel(p)),
		DetailHash:  redact.Hash(findingBlockedDeployed + "|agent365|" + p.ID + "|" + deployedTo),
		OccurredAt:  now,
	}
}

func externalBroadDeploymentFinding(p copilotPackage, now time.Time) model.FindingReport {
	return model.FindingReport{
		Kind:        findingExternalBroadDeployment,
		Severity:    model.SeverityLow,
		SubjectKind: findingSubjectKindIdentity,
		SubjectRef:  p.ID,
		Title:       fmt.Sprintf("external package deployed to all users: %s", packageLabel(p)),
		DetailHash:  redact.Hash(findingExternalBroadDeployment + "|agent365|" + p.ID),
		OccurredAt:  now,
	}
}

func packageLabel(p copilotPackage) string {
	if p.DisplayName != "" {
		return p.DisplayName
	}
	return p.ID
}
