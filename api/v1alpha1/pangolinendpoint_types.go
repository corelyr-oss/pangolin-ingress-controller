package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// Protocol is the L4 protocol of an exposed port.
// +kubebuilder:validation:Enum=TCP;UDP
type Protocol string

const (
	ProtocolTCP Protocol = "TCP"
	ProtocolUDP Protocol = "UDP"
)

// Condition types reported on a PangolinEndpoint. The vocabulary is borrowed
// from the Gateway API because it maps cleanly onto the failure classes this
// controller already distinguishes: an unusable spec, a reference that cannot
// be resolved, and a write Pangolin has not accepted.
const (
	// ConditionAccepted is False when the spec itself cannot be acted on --
	// no configured alias suffix, an over-long derived identity, or a Pangolin
	// instance that does not implement private resources.
	ConditionAccepted = "Accepted"

	// ConditionResolvedRefs is False when something the spec points at is
	// missing or ambiguous: the backing Service, a site, a role, a user, or a
	// client.
	ConditionResolvedRefs = "ResolvedRefs"

	// ConditionProgrammed is False when Pangolin has rejected, or has not yet
	// accepted, the desired configuration.
	ConditionProgrammed = "Programmed"

	// ConditionReady summarises whether the endpoint is usable. It is False
	// with reason NoPrincipalsGranted for an endpoint that exists but grants
	// access to nobody.
	ConditionReady = "Ready"
)

// Condition reasons.
const (
	ReasonReconciled               = "Reconciled"
	ReasonAliasSuffixNotConfigured = "AliasSuffixNotConfigured"
	ReasonIdentityInvalid          = "IdentityInvalid"
	ReasonIdentityTooLong          = "IdentityTooLong"
	ReasonUnsupportedByServer      = "UnsupportedByServer"
	ReasonBackendNotFound          = "BackendNotFound"
	ReasonBackendUnsupported       = "BackendUnsupported"
	ReasonSiteNotFound             = "SiteNotFound"
	ReasonPrincipalNotFound        = "PrincipalNotFound"
	ReasonPrincipalAmbiguous       = "PrincipalAmbiguous"
	ReasonNoPrincipalsGranted      = "NoPrincipalsGranted"
	ReasonPangolinError            = "PangolinError"
)

// BackendReference selects the Kubernetes Service that backs the endpoint.
// The Service must live in the same namespace as the PangolinEndpoint.
type BackendReference struct {
	// Name is the name of the backing Service.
	// +kubebuilder:validation:MinLength=1
	Name string `json:"name"`
}

// EndpointPort declares one port, one inclusive range of ports, or every port
// for a protocol. Exactly one of the three forms must be used.
//
// +kubebuilder:validation:XValidation:rule="(has(self.port) ? 1 : 0) + ((has(self.from) || has(self.to)) ? 1 : 0) + ((has(self.all) && self.all) ? 1 : 0) == 1",message="exactly one of port, (from and to), or all must be set"
// +kubebuilder:validation:XValidation:rule="has(self.from) == has(self.to)",message="from and to must be set together"
// +kubebuilder:validation:XValidation:rule="!has(self.from) || !has(self.to) || self.from <= self.to",message="from must not be greater than to"
type EndpointPort struct {
	// Protocol of the exposed port.
	// +kubebuilder:default=TCP
	// +optional
	Protocol Protocol `json:"protocol,omitempty"`

	// Port exposes a single port.
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=65535
	// +optional
	Port *int32 `json:"port,omitempty"`

	// From is the first port of an inclusive range. Must be set with To.
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=65535
	// +optional
	From *int32 `json:"from,omitempty"`

	// To is the last port of an inclusive range. Must be set with From.
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=65535
	// +optional
	To *int32 `json:"to,omitempty"`

	// All exposes every port for the protocol.
	// +optional
	All *bool `json:"all,omitempty"`
}

// AccessSpec names the Pangolin principals permitted to reach the endpoint.
// Principals are named rather than identified by Pangolin's internal IDs; the
// controller resolves the names and reports an unknown or ambiguous name on
// the ResolvedRefs condition.
//
// An endpoint that names no principals is created, but is reachable by nobody
// and is reported Ready=False with reason NoPrincipalsGranted.
type AccessSpec struct {
	// Clients are Pangolin client names.
	// +optional
	Clients []string `json:"clients,omitempty"`

	// Roles are Pangolin role names.
	// +optional
	Roles []string `json:"roles,omitempty"`

	// Users are Pangolin usernames.
	// +optional
	Users []string `json:"users,omitempty"`
}

// PrivateEndpointSpec describes an endpoint with no public entrypoint,
// reachable only by clients connected to the Pangolin mesh.
type PrivateEndpointSpec struct {
	// Alias is the internal FQDN that mesh clients dial. When unset it is
	// derived as <name>.<namespace>.<suffix> from the controller's configured
	// alias suffix; the controller refuses to derive an alias when no suffix
	// is configured.
	// +optional
	Alias string `json:"alias,omitempty"`

	// Ports exposed on the endpoint. When empty, the ports are taken from the
	// backing Service on every reconcile -- meaning an unset Ports field
	// tracks the Service, and adding a Service port widens the endpoint
	// without a change to this object.
	// +optional
	Ports []EndpointPort `json:"ports,omitempty"`

	// Access names the principals permitted to reach the endpoint.
	// +optional
	Access *AccessSpec `json:"access,omitempty"`

	// DisableICMP suppresses ICMP to the destination.
	// +optional
	DisableICMP *bool `json:"disableIcmp,omitempty"`
}

// PublicEndpointSpec is reserved for the public raw TCP/UDP branch and is
// rejected in v1alpha1.
//
// Pangolin's public-resource create accepts no caller-supplied niceId, so a
// resource whose recorded ID is lost can only be re-found by matching its
// proxy port -- which is indistinguishable from another owner having taken
// that port. Claiming a resource on that basis would be a hijack, so the
// branch is deferred until it has a safe identity model.
type PublicEndpointSpec struct{}

// PangolinEndpointSpec defines the desired state of a PangolinEndpoint.
//
// +kubebuilder:validation:XValidation:rule="has(self.private)",message="spec.private is required: v1alpha1 implements the private branch only"
// +kubebuilder:validation:XValidation:rule="!has(self.public)",message="spec.public is reserved: the public raw TCP/UDP branch is not implemented in v1alpha1"
type PangolinEndpointSpec struct {
	// BackendRef selects the Service that backs this endpoint.
	BackendRef BackendReference `json:"backendRef"`

	// SiteRefs are the Pangolin site nice IDs to attach the endpoint to. When
	// empty, the controller's configured default site is used.
	// +optional
	SiteRefs []string `json:"siteRefs,omitempty"`

	// Enabled controls whether Pangolin serves the endpoint. Defaults to true.
	// +optional
	Enabled *bool `json:"enabled,omitempty"`

	// Private declares a mesh-only endpoint. Required in v1alpha1.
	// +optional
	Private *PrivateEndpointSpec `json:"private,omitempty"`

	// Public is reserved and rejected in v1alpha1.
	// +optional
	Public *PublicEndpointSpec `json:"public,omitempty"`
}

// ResolvedPorts reports the port range strings that were sent to Pangolin, so
// that a Ports field defaulted from the backing Service is visible on the
// object rather than only inferable from the Service.
type ResolvedPorts struct {
	// TCP is the resolved TCP port range string, e.g. "5432,8000-9000" or "*".
	// +optional
	TCP string `json:"tcp,omitempty"`

	// UDP is the resolved UDP port range string.
	// +optional
	UDP string `json:"udp,omitempty"`
}

// PangolinEndpointStatus defines the observed state of a PangolinEndpoint.
type PangolinEndpointStatus struct {
	// SiteResourceID is the Pangolin identifier of the private resource.
	// +optional
	SiteResourceID string `json:"siteResourceId,omitempty"`

	// NiceID is the deterministic Pangolin nice ID derived from this object's
	// namespace and name. It is the controller's identity for the endpoint and
	// is how a lost SiteResourceID is recovered without creating a duplicate.
	// +optional
	NiceID string `json:"niceId,omitempty"`

	// Address is the effective alias -- what mesh clients dial.
	// +optional
	Address string `json:"address,omitempty"`

	// ResolvedPorts is the port set that was sent to Pangolin.
	// +optional
	ResolvedPorts *ResolvedPorts `json:"resolvedPorts,omitempty"`

	// ObservedGeneration is the generation of the spec this status reflects.
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// Conditions report Accepted, ResolvedRefs, Programmed and Ready.
	// +optional
	// +patchMergeKey=type
	// +patchStrategy=merge
	// +listType=map
	// +listMapKey=type
	Conditions []metav1.Condition `json:"conditions,omitempty" patchStrategy:"merge" patchMergeKey:"type"`
}

// PangolinEndpoint is a Pangolin resource that cannot be expressed as an
// Ingress: it has no public hostname, no TLS to terminate and no HTTP
// semantics, and its access control is mandatory rather than optional.
//
// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:shortName=pgep
// +kubebuilder:printcolumn:name="Address",type=string,JSONPath=`.status.address`
// +kubebuilder:printcolumn:name="Ports",type=string,JSONPath=`.status.resolvedPorts.tcp`
// +kubebuilder:printcolumn:name="Ready",type=string,JSONPath=`.status.conditions[?(@.type=="Ready")].status`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`
type PangolinEndpoint struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   PangolinEndpointSpec   `json:"spec,omitempty"`
	Status PangolinEndpointStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// PangolinEndpointList contains a list of PangolinEndpoint.
type PangolinEndpointList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []PangolinEndpoint `json:"items"`
}

func init() {
	SchemeBuilder.Register(&PangolinEndpoint{}, &PangolinEndpointList{})
}
