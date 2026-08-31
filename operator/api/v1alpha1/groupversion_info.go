// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

// Package v1alpha1 contains the API types for the ops.olivares.ai/v1alpha1 group.
//
// This is the control plane's OWN lifecycle CRD (Kind: ControlPlane). It is the
// declarative form of what the Helm chart at deploy/helm/olivares
// renders imperatively (a single-writer StatefulSet + headless Service + an
// optional backup CronJob). Reconciling a ControlPlane object materializes those
// same shapes from spec.
//
// This package is deliberately self-contained: it imports neither the engine
// (/core) nor the SDK (/sdk). The operator is a SEPARATE Go module that keeps the
// controller-runtime / client-go dependency tree out of core's SBOM, exactly as
// terraform-provider-olivares keeps the terraform-plugin-framework tree out.
//
// +kubebuilder:object:generate=true
// +groupName=ops.olivares.ai
package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

var (
	// GroupVersion is the group/version for the ControlPlane API.
	GroupVersion = schema.GroupVersion{Group: "ops.olivares.ai", Version: "v1alpha1"}

	// SchemeBuilder registers the types in this package with a runtime.Scheme. It
	// uses apimachinery's runtime.SchemeBuilder directly (not the deprecated
	// controller-runtime scheme.Builder), keeping this api package dependency-light.
	SchemeBuilder = runtime.NewSchemeBuilder(addKnownTypes)

	// AddToScheme adds the types in this group/version to the given scheme.
	AddToScheme = SchemeBuilder.AddToScheme
)

// addKnownTypes registers the ControlPlane types with the scheme.
func addKnownTypes(s *runtime.Scheme) error {
	s.AddKnownTypes(GroupVersion, &ControlPlane{}, &ControlPlaneList{})
	metav1.AddToGroupVersion(s, GroupVersion)
	return nil
}
