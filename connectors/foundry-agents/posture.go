// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package foundryagents

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
	findingAppNoAgentIdentity  = "foundry_app_no_agent_identity"
	findingAppDeploymentFailed = "foundry_app_deployment_failed"
	findingSubjectIdentity     = "identity"
)

// Gather emits ARM-derived Foundry application posture findings. It deliberately
// does not read the data plane; the roster travels through Snapshot.
func (s *Source) Gather(ctx context.Context, sink sdk.Sink) error {
	if s.offline() {
		return nil
	}
	client, err := s.armClient(ctx)
	if err != nil {
		return err
	}
	inv, err := s.readARM(ctx, client)
	if err != nil {
		return err
	}
	now := s.clock().UTC()
	for _, acct := range inv {
		for _, proj := range acct.Projects {
			for _, app := range proj.Applications {
				if app.Application.ID == "" {
					continue
				}
				if !hasAgentIdentity(app.Application) {
					if err := sink.Emit(ctx, noAgentIdentityFinding(app.Application, now)); err != nil {
						return err
					}
				}
				if app.Application.Properties.IsEnabled && hasFailedDeployment(app.Deployments) {
					if err := sink.Emit(ctx, failedDeploymentFinding(app.Application, now)); err != nil {
						return err
					}
				}
			}
		}
	}
	return nil
}

func noAgentIdentityFinding(app application, now time.Time) model.FindingReport {
	armID := app.ID
	return model.FindingReport{
		Kind:        findingAppNoAgentIdentity,
		Severity:    model.SeverityMedium,
		SubjectKind: findingSubjectIdentity,
		SubjectRef:  armID,
		Title:       fmt.Sprintf("published agent application without Entra agent identity: %s", applicationLabel(app)),
		DetailHash:  redact.Hash(findingAppNoAgentIdentity + "|foundry-agents|" + armID),
		OccurredAt:  now,
	}
}

func failedDeploymentFinding(app application, now time.Time) model.FindingReport {
	armID := app.ID
	return model.FindingReport{
		Kind:        findingAppDeploymentFailed,
		Severity:    model.SeverityLow,
		SubjectKind: findingSubjectIdentity,
		SubjectRef:  armID,
		Title:       fmt.Sprintf("enabled agent application has a failed deployment: %s", applicationLabel(app)),
		DetailHash:  redact.Hash(findingAppDeploymentFailed + "|foundry-agents|" + armID),
		OccurredAt:  now,
	}
}

func hasAgentIdentity(app application) bool {
	return app.Properties.AgentIdentityBlueprint.ClientID != "" ||
		app.Properties.AgentIdentityBlueprint.PrincipalID != "" ||
		app.Properties.DefaultInstanceIdentity.ClientID != "" ||
		app.Properties.DefaultInstanceIdentity.PrincipalID != ""
}

func hasFailedDeployment(rows []agentDeployment) bool {
	for _, d := range rows {
		if strings.EqualFold(d.Properties.State, "Failed") {
			return true
		}
	}
	return false
}
