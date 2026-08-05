// Package v1alpha1 contains the API types served by the Pangolin ingress
// controller.
//
// The API is alpha: it may change in backwards-incompatible ways between
// releases. The CRD is therefore shipped in the Helm chart's templates/
// directory rather than crds/, so that `helm upgrade` applies schema changes.
//
// +kubebuilder:object:generate=true
// +groupName=pangolin.ingress.k8s.io
package v1alpha1

import (
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/scheme"
)

var (
	// GroupVersion is group version used to register these objects.
	GroupVersion = schema.GroupVersion{Group: "pangolin.ingress.k8s.io", Version: "v1alpha1"}

	// SchemeBuilder is used to add go types to the GroupVersionKind scheme.
	SchemeBuilder = &scheme.Builder{GroupVersion: GroupVersion}

	// AddToScheme adds the types in this group-version to the given scheme.
	AddToScheme = SchemeBuilder.AddToScheme
)
