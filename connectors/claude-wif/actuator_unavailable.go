//go:build !enterprise || !addon_ids

// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package claudewif

import (
	"context"
	"errors"

	"github.com/olivaresai/olivares/connectors/identitysource"
	"github.com/olivaresai/olivares/connectors/modelprovider"
)

var errActuationUnavailable = errors.New("NHI lifecycle actuation requires Business")

// Actuator preserves the lifecycle API without exposing unavailable operations.
type Actuator struct{}

var _ identitysource.LifecycleActuator = (*Actuator)(nil)

func NewActuator(string, string, modelprovider.Doer) *Actuator      { return &Actuator{} }
func (*Actuator) Capabilities() []identitysource.ActuatorCapability { return nil }
func (*Actuator) Disable(context.Context, identitysource.ActuationRequest) (identitysource.ActuationReceipt, error) {
	return identitysource.ActuationReceipt{}, errActuationUnavailable
}
func (*Actuator) Restore(context.Context, identitysource.ActuationRequest) (identitysource.ActuationReceipt, error) {
	return identitysource.ActuationReceipt{}, errActuationUnavailable
}
func (*Actuator) Finalize(context.Context, identitysource.ActuationRequest) (identitysource.ActuationReceipt, error) {
	return identitysource.ActuationReceipt{}, errActuationUnavailable
}
func (*Actuator) Rotate(context.Context, identitysource.ActuationRequest) (identitysource.RotatedCredential, error) {
	return identitysource.RotatedCredential{}, errActuationUnavailable
}
func (*Actuator) Retire(context.Context, identitysource.ActuationRequest) (identitysource.ActuationReceipt, error) {
	return identitysource.ActuationReceipt{}, errActuationUnavailable
}
