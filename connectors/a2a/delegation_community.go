// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

//go:build !enterprise || !addon_ids

package a2a

import (
	"context"
	"errors"
	"net/http"
)

var errBusinessDelegation = errors.New("a2a: delegation is a Business capability")

func NewClient(EmitConfig) *Client { return &Client{} }
func (*Client) SendMessage(context.Context, SendSpec) (TaskResult, error) {
	return TaskResult{}, errBusinessDelegation
}
func (*Client) SendMessageCapable(context.Context, SendSpec) (TaskResult, error) {
	return TaskResult{}, errBusinessDelegation
}
func NewDelegator(DelegatorConfig) *Delegator { return &Delegator{gate: denyDelegationGate{}} }
func (*Delegator) Test(context.Context, DelegateSpec) (DelegationTestResult, error) {
	return DelegationTestResult{}, errBusinessDelegation
}
func (*Delegator) Delegate(context.Context, DelegateSpec) (TaskResult, error) {
	return TaskResult{}, errBusinessDelegation
}
func (*Delegator) GetTask(context.Context, TaskRef) (TaskResult, error) {
	return TaskResult{}, errBusinessDelegation
}
func (*Delegator) Reconcile(context.Context, TaskResult, TaskRef) (TaskResult, bool, error) {
	return TaskResult{}, false, errBusinessDelegation
}
func (*Delegator) CancelTask(context.Context, TaskRef) (TaskResult, error) {
	return TaskResult{}, errBusinessDelegation
}
func (*Delegator) ListTasks(context.Context, ListSpec) (TaskPage, error) {
	return TaskPage{}, errBusinessDelegation
}
func (*Delegator) GetExtendedAgentCard(context.Context, string, string) (ExtendedCard, error) {
	return ExtendedCard{}, errBusinessDelegation
}
func (*Delegator) DelegateStreaming(context.Context, DelegateSpec, func(StreamEvent) error) error {
	return errBusinessDelegation
}
func (*Delegator) SubscribeToTask(context.Context, TaskRef, func(StreamEvent) error) error {
	return errBusinessDelegation
}
func NewInboundServer(InboundServerConfig) (*InboundServer, error) { return nil, errBusinessDelegation }
func NewPushReceiver(PushReceiverConfig) (*PushReceiver, error)    { return nil, errBusinessDelegation }
func (*InboundServer) ServeHTTP(w http.ResponseWriter, _ *http.Request) {
	http.Error(w, errBusinessDelegation.Error(), http.StatusNotImplemented)
}
func (*PushReceiver) ServeHTTP(w http.ResponseWriter, _ *http.Request) {
	http.Error(w, errBusinessDelegation.Error(), http.StatusNotImplemented)
}
