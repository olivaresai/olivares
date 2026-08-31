// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

// Package sdk defines the stable public contract that connectors and modules
// build against: the SourceConnector and OutputConnector interfaces, the Module
// and Host interfaces, the Descriptor/Config self-description, and (via the
// sibling sdk/model and sdk/event packages) the wire vocabulary they exchange.
// It is licensed Apache-2.0 so the ecosystem can extend Olivares AI without
// copyleft friction.
//
// # Dependency boundary
//
// This module imports only the standard library. It must never import the AGPL
// engine (github.com/olivaresai/olivares/core); that boundary is the clean
// AGPL/Apache frontier (LICENSING.md) and is enforced in CI by scripts/check-boundary.sh
// (the real go list -deps build graph). An author who only writes an in-process
// connector therefore pulls in zero third-party dependencies.
//
// The gRPC/protobuf wire contract and the hashicorp/go-plugin transport that let
// a connector or module ship as a separate process live in the SEPARATE module
// github.com/olivaresai/olivares/sdk/plugin, so the gRPC dependency tree is
// opt-in: it reaches an author's go.sum only if they choose to ship a plugin.
//
// # Packages
//
//	sdk         the component interfaces (SourceConnector/OutputConnector/Module/Host)
//	            plus Descriptor and Config.
//	sdk/model   the wire DTOs a source emits (EdgeObservation/CostSample/
//	            FindingReport) and the shared enums (AccessMode/SignalSource/
//	            Confidence/Severity), with a sealed Observation sum type.
//	sdk/event   the Event envelope, its Type discriminator and the Handler a
//	            subscriber registers, with typed payload helpers.
//
// # How the pieces fit
//
// A SourceConnector gathers facts and Emits model.Observation values to an
// engine-provided Sink. The engine lifts each observation onto the event bus as
// an event.Event. A Module subscribes to events through its Host and reacts,
// optionally publishing derived events; an OutputConnector delivers events to
// external systems as Notifications. See the connector- and module-authoring
// guides in docs/ for worked examples.
package sdk
