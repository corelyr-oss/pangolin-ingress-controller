// Package v1alpha1 contains the API types served by the Pangolin ingress
// controller.
//
// The API is alpha: it may change in backwards-incompatible ways between
// releases. The CRD is therefore shipped in the Helm chart's templates/
// directory rather than crds/, so that `helm upgrade` applies schema changes.
//
// The API group is pangolin.corelyr.com rather than a *.k8s.io name: the
// apiserver treats any group under k8s.io/kubernetes.io as a protected
// community group and rejects a CRD in one unless it carries an
// api-approved.kubernetes.io annotation pointing at a Kubernetes API review.
//
// +kubebuilder:object:generate=true
// +groupName=pangolin.corelyr.com
package v1alpha1

import (
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/scheme"
)

var (
	// GroupVersion is group version used to register these objects.
	GroupVersion = schema.GroupVersion{Group: "pangolin.corelyr.com", Version: "v1alpha1"}

	// SchemeBuilder is used to add go types to the GroupVersionKind scheme.
	SchemeBuilder = &scheme.Builder{GroupVersion: GroupVersion}

	// AddToScheme adds the types in this group-version to the given scheme.
	AddToScheme = SchemeBuilder.AddToScheme
)
