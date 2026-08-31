// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package agentcore

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/olivaresai/olivares/connectors/internal/redact"
	"github.com/olivaresai/olivares/sdk"
	"github.com/olivaresai/olivares/sdk/model"
)

// Subject-kind constants for registry observations and findings. The
// SubjectKind/SubjectRef pair is the consumer's dedup key for posture
// findings and the resource identity for edge observations.
const (
	subjectRegistry       = "agentcore.registry"
	subjectRegistryRecord = "agentcore.registry_record"
	subjectRegistryHealth = "agentcore.registry"
	resourceKindRegistry  = "agentcore.registry"
	resourceKindRecord    = "agentcore.registry_record"
)

// gatherRegistry reads AgentCore registries and their records, emitting:
//   - one EdgeObservation per APPROVED registry record (the agent is
//     declared/registered in the account's agent directory)
//   - one FindingReport per non-APPROVED record (posture gap: error, deprecated,
//     pending_approval)
//   - one FindingReport when no registries exist (absence is a posture signal)
//
// Every edge is (observed=false, permitted=true): a registry record is a
// DECLARED agent, not an observed access — it rides the SignalAgentCore path
//.
func (s *Source) gatherRegistry(ctx context.Context, sink sdk.Sink, c *client, at time.Time) error {
	registries, err := s.listRegistries(ctx, c)
	if err != nil {
		return err
	}
	scope := s.accountScope()

	if len(registries) == 0 {
		return sink.Emit(ctx, model.FindingReport{
			Kind:        "registry_posture",
			Severity:    model.SeverityInfo,
			SubjectKind: subjectRegistry,
			SubjectRef:  redact.Clean(scope),
			Title:       "No AgentCore registries in region " + s.region,
			DetailHash:  redact.Hash("agentcore.registry account=" + scope + " registries=0"),
			OccurredAt:  at,
		})
	}

	for i, reg := range registries {
		if i >= s.maxRegistries {
			if err := sink.Emit(ctx, model.FindingReport{
				Kind:        "registry_posture",
				Severity:    model.SeverityLow,
				SubjectKind: subjectRegistry,
				SubjectRef:  redact.Clean(scope),
				Title:       "AgentCore registry scan PARTIAL — raise max_registries for full coverage",
				DetailHash:  redact.Hash(fmt.Sprintf("agentcore.registry account=%s coverage=partial read=%d total=%d", scope, i, len(registries))),
				OccurredAt:  at,
			}); err != nil {
				return err
			}
			break
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := s.gatherRegistryRecords(ctx, sink, c, reg, at); err != nil {
			return err
		}
	}
	return nil
}

// gatherRegistryRecords lists records in one registry, emitting an
// EdgeObservation for each APPROVED record and a FindingReport for each
// record in a non-healthy state.
func (s *Source) gatherRegistryRecords(ctx context.Context, sink sdk.Sink, c *client, reg registryItem, at time.Time) error {
	records, err := s.listRegistryRecords(ctx, c, reg.RegistryID)
	if err != nil {
		return err
	}

	bounded := records
	if len(bounded) > s.maxRecords {
		bounded = bounded[:s.maxRecords]
		if err := sink.Emit(ctx, model.FindingReport{
			Kind:        "registry_posture",
			Severity:    model.SeverityLow,
			SubjectKind: subjectRegistry,
			SubjectRef:  redact.Clean(reg.RegistryArn),
			Title:       fmt.Sprintf("Registry %s scan PARTIAL — %d records read of %d", redact.Clean(reg.Name), s.maxRecords, len(records)),
			DetailHash:  redact.Hash(fmt.Sprintf("agentcore.registry_record registry=%s coverage=partial read=%d total=%d", reg.RegistryID, s.maxRecords, len(records))),
			OccurredAt:  at,
		}); err != nil {
			return err
		}
	}

	for _, rec := range bounded {
		if err := ctx.Err(); err != nil {
			return err
		}
		status := strings.ToUpper(rec.Status)
		switch status {
		case "APPROVED":
			if err := sink.Emit(ctx, model.EdgeObservation{
				OriginKind:   "agent",
				OriginRef:    redact.Clean(rec.Name),
				ResourceKind: resourceKindRecord,
				ResourceRef:  redact.Clean(rec.RecordArn),
				Mode:         model.ModeRead,
				Source:       model.SignalAgentCore,
				Confidence:   model.ConfidenceAttributed,
				ToolRef:      rec.DescriptorType,
				ObservedAt:   at,
				Labels: map[string]string{
					"registry":        redact.Clean(reg.Name),
					"descriptor_type": rec.DescriptorType,
					"version":         rec.RecordVersion,
				},
			}); err != nil {
				return err
			}
		default:
			sev := registryRecordSeverity(status)
			if err := sink.Emit(ctx, model.FindingReport{
				Kind:        "registry_posture",
				Severity:    sev,
				SubjectKind: subjectRegistryRecord,
				SubjectRef:  redact.Clean(rec.RecordArn),
				Title:       fmt.Sprintf("Registry record %s is %s", redact.Clean(rec.Name), status),
				DetailHash:  redact.Hash(fmt.Sprintf("agentcore.registry_record record=%s status=%s type=%s registry=%s", rec.RecordID, status, rec.DescriptorType, reg.RegistryID)),
				OccurredAt:  at,
			}); err != nil {
				return err
			}
		}
	}
	return nil
}

// registryRecordSeverity maps a registry record status to the appropriate
// severity for posture findings.
func registryRecordSeverity(status string) model.Severity {
	switch status {
	case "CREATE_FAILED", "UPDATE_FAILED":
		return model.SeverityHigh
	case "DEPRECATED":
		return model.SeverityMedium
	case "REJECTED":
		return model.SeverityMedium
	case "DRAFT", "PENDING_APPROVAL", "CREATING", "UPDATING":
		return model.SeverityLow
	default:
		return model.SeverityInfo
	}
}
