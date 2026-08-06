package controller

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/predicate"

	"github.com/vinzenz/pangolin-ingress-controller/api/v1alpha1"
	"github.com/vinzenz/pangolin-ingress-controller/internal/pangolin"
)

const (
	// defaultClusterDomain is the cluster DNS suffix used to build a Service's
	// fully qualified name.
	defaultClusterDomain = "svc.cluster.local"

	// privateResourceMode is the only Pangolin private-resource mode this
	// controller emits. A backendRef always denotes exactly one Service, which
	// is exactly one destination host; "cidr" describes a subnet and "http"/
	// "ssh" pull in protocol handling that has no expression in this API.
	privateResourceMode = "host"

	// maxNiceIDLength is Pangolin's limit on a resource nice ID.
	maxNiceIDLength = 255
)

// niceIDPattern is the character set Pangolin accepts for a nice ID.
var niceIDPattern = regexp.MustCompile(`^[a-zA-Z0-9-]+$`)

// aliasLabelPattern matches one label of the alias suffix.
var aliasLabelPattern = regexp.MustCompile(`^[a-zA-Z0-9]([a-zA-Z0-9-]{0,61}[a-zA-Z0-9])?$`)

// endpointIssue is a condition an operator can fix: a spec that cannot be
// acted on, a reference that does not resolve, or a Pangolin instance that
// cannot serve the request.
//
// It is deliberately not an error return from Reconcile. Riding
// controller-runtime's exponential backoff would make recovery time after the
// fix unpredictable, and would count operator configuration against
// controller_runtime_reconcile_errors_total, which should measure controller
// faults.
type endpointIssue struct {
	condition string
	reason    string
	message   string
}

func (e *endpointIssue) Error() string { return e.message }

func issuef(condition, reason, format string, args ...interface{}) *endpointIssue {
	return &endpointIssue{condition: condition, reason: reason, message: fmt.Sprintf(format, args...)}
}

// PangolinEndpointReconciler reconciles a PangolinEndpoint into a Pangolin
// private resource.
type PangolinEndpointReconciler struct {
	client.Client
	Scheme          *runtime.Scheme
	ResourcePrefix  string
	PangolinClient  *pangolin.Client
	PangolinBaseURL string
	APIKeySecret    string
	APIKeyNamespace string
	OrgID           string
	SiteNiceID      string
	Recorder        record.EventRecorder

	// AliasSuffix is appended to <name>.<namespace> to derive the internal
	// FQDN clients dial. It has no default: aliases are unique org-wide, so a
	// shipped default would make two clusters sharing one Pangolin
	// organization collide by default.
	AliasSuffix string

	// ClusterDomain is the cluster DNS suffix. Defaults to svc.cluster.local.
	ClusterDomain string

	// NameCacheRefreshInterval bounds how often role and client lists are
	// refetched after a name fails to resolve.
	NameCacheRefreshInterval time.Duration

	principals *principalResolver

	siteMu sync.RWMutex
	sites  map[string]*pangolin.Site
}

//+kubebuilder:rbac:groups=pangolin.corelyr.com,resources=pangolinendpoints,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=pangolin.corelyr.com,resources=pangolinendpoints/status,verbs=get;update;patch
//+kubebuilder:rbac:groups=pangolin.corelyr.com,resources=pangolinendpoints/finalizers,verbs=update
//+kubebuilder:rbac:groups="",resources=services,verbs=get;list;watch
//+kubebuilder:rbac:groups="",resources=secrets,verbs=get;list;watch
//+kubebuilder:rbac:groups="",resources=events,verbs=create;patch

// Reconcile drives a PangolinEndpoint towards its desired state.
func (r *PangolinEndpointReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := log.FromContext(ctx)

	if r.PangolinClient == nil {
		if err := r.initPangolinClient(ctx); err != nil {
			log.Error(err, "Failed to initialize Pangolin client")
			return ctrl.Result{}, err
		}
	}

	ep := &v1alpha1.PangolinEndpoint{}
	if err := r.Get(ctx, req.NamespacedName, ep); err != nil {
		if apierrors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}

	if !ep.DeletionTimestamp.IsZero() {
		if !controllerutil.ContainsFinalizer(ep, pangolinFinalizerName) {
			return ctrl.Result{}, nil
		}
		if err := r.deleteSiteResource(ctx, ep); err != nil {
			log.Error(err, "Failed to delete Pangolin private resource")
			return ctrl.Result{}, err
		}
		controllerutil.RemoveFinalizer(ep, pangolinFinalizerName)
		if err := r.Update(ctx, ep); err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{}, nil
	}

	if !controllerutil.ContainsFinalizer(ep, pangolinFinalizerName) {
		controllerutil.AddFinalizer(ep, pangolinFinalizerName)
		if err := r.Update(ctx, ep); err != nil {
			return ctrl.Result{}, err
		}
	}

	reconcileErr := r.reconcileEndpoint(ctx, ep)

	var issue *endpointIssue
	if errors.As(reconcileErr, &issue) {
		r.applyIssue(ep, issue)
		if err := r.updateStatus(ctx, ep); err != nil {
			return ctrl.Result{}, err
		}
		if r.Recorder != nil {
			r.Recorder.Event(ep, corev1.EventTypeWarning, issue.reason, issue.message)
		}
		requeue := r.requeueAfter()
		log.Info("PangolinEndpoint not ready; will retry",
			"reason", issue.reason, "message", issue.message, "requeueAfter", requeue)
		return ctrl.Result{RequeueAfter: requeue}, nil
	}

	if reconcileErr != nil {
		// Record what is known before surfacing the failure, so the object
		// still shows why it is not programmed.
		setCondition(ep, v1alpha1.ConditionProgrammed, metav1.ConditionFalse, v1alpha1.ReasonPangolinError, reconcileErr.Error())
		setCondition(ep, v1alpha1.ConditionReady, metav1.ConditionFalse, v1alpha1.ReasonPangolinError, reconcileErr.Error())
		if err := r.updateStatus(ctx, ep); err != nil {
			log.Error(err, "Failed to update status after reconcile error")
		}
		return ctrl.Result{}, reconcileErr
	}

	if err := r.updateStatus(ctx, ep); err != nil {
		return ctrl.Result{}, err
	}

	log.Info("Successfully reconciled PangolinEndpoint", "name", ep.Name, "address", ep.Status.Address)
	return ctrl.Result{}, nil
}

// reconcileEndpoint resolves the desired state and converges Pangolin onto it,
// recording observed values on ep.Status. Conditions are set by the caller.
func (r *PangolinEndpointReconciler) reconcileEndpoint(ctx context.Context, ep *v1alpha1.PangolinEndpoint) error {
	private := ep.Spec.Private
	if private == nil {
		// The CRD's CEL validation rejects this, so reaching it means the CRD
		// in the cluster is older than this binary.
		return issuef(v1alpha1.ConditionAccepted, v1alpha1.ReasonUnsupportedByServer,
			"spec.private is required; the installed CRD may predate this controller version")
	}

	niceID, err := r.niceIDFor(ep)
	if err != nil {
		return err
	}
	ep.Status.NiceID = niceID

	alias, err := r.aliasFor(ep)
	if err != nil {
		return err
	}
	ep.Status.Address = alias

	destination, svc, err := r.resolveDestination(ctx, ep)
	if err != nil {
		return err
	}

	tcp, udp, err := r.resolvePorts(private, svc)
	if err != nil {
		return err
	}
	ep.Status.ResolvedPorts = &v1alpha1.ResolvedPorts{TCP: tcp.String(), UDP: udp.String()}

	siteIDs, err := r.resolveSites(ctx, ep)
	if err != nil {
		return err
	}

	roleIDs, userIDs, clientIDs, err := r.resolvePrincipals(ctx, private.Access)
	if err != nil {
		return err
	}

	existing, err := r.findSiteResource(ctx, ep, siteIDs[0])
	if err != nil {
		return err
	}

	desired := desiredSiteResource{
		name:        niceID,
		niceID:      niceID,
		alias:       alias,
		destination: destination,
		tcp:         tcp,
		udp:         udp,
		siteIDs:     siteIDs,
		enabled:     ep.Spec.Enabled == nil || *ep.Spec.Enabled,
		disableICMP: private.DisableICMP != nil && *private.DisableICMP,
	}
	if port, ok := singleTCPPort(tcp); ok {
		desired.destinationPort = int(port)
	}

	if existing == nil {
		created, err := r.createSiteResource(ctx, desired, roleIDs, userIDs, clientIDs)
		if err != nil {
			return err
		}
		ep.Status.SiteResourceID = strconv.Itoa(created.ID)
		ep.Status.AssignedAddress = created.AliasAddress
		return nil
	}

	ep.Status.SiteResourceID = strconv.Itoa(existing.ID)
	ep.Status.AssignedAddress = existing.AliasAddress
	if err := r.updateSiteResourceIfChanged(ctx, existing, desired); err != nil {
		return err
	}
	return r.reconcilePrincipals(ctx, strconv.Itoa(existing.ID), roleIDs, userIDs, clientIDs)
}

// desiredSiteResource is the state a PangolinEndpoint asks for.
type desiredSiteResource struct {
	name            string
	niceID          string
	alias           string
	destination     string
	destinationPort int
	tcp             portSet
	udp             portSet
	siteIDs         []int
	enabled         bool
	disableICMP     bool
}

func (r *PangolinEndpointReconciler) createSiteResource(ctx context.Context, d desiredSiteResource, roleIDs []int, userIDs []string, clientIDs []int) (*pangolin.SiteResource, error) {
	req := &pangolin.CreateSiteResourceRequest{
		Name:            d.name,
		NiceID:          d.niceID,
		Mode:            privateResourceMode,
		SiteID:          d.siteIDs[0],
		Destination:     d.destination,
		DestinationPort: d.destinationPort,
		Alias:           d.alias,
		TCPPortRange:    d.tcp.String(),
		UDPPortRange:    d.udp.String(),
		DisableICMP:     d.disableICMP,
		RoleIDs:         roleIDs,
		UserIDs:         userIDs,
		ClientIDs:       clientIDs,
	}
	if len(d.siteIDs) > 1 {
		req.SiteIDs = d.siteIDs
	}

	created, err := r.PangolinClient.CreateSiteResource(ctx, req)
	if err != nil {
		if pangolin.IsNotImplemented(err) {
			return nil, issuef(v1alpha1.ConditionAccepted, v1alpha1.ReasonUnsupportedByServer,
				"this Pangolin instance does not implement private resources: %v", err)
		}
		return nil, fmt.Errorf("failed to create Pangolin private resource: %w", err)
	}
	return created, nil
}

// updateSiteResourceIfChanged issues an update only when the observed state
// differs from the desired state. Port ranges are compared by the ports they
// contain rather than by their text, so Pangolin normalising what it stores
// does not produce an update on every reconcile.
func (r *PangolinEndpointReconciler) updateSiteResourceIfChanged(ctx context.Context, existing *pangolin.SiteResource, d desiredSiteResource) error {
	log := log.FromContext(ctx)

	currentTCP, tcpErr := parsePortSet(existing.TCPPortRange)
	currentUDP, udpErr := parsePortSet(existing.UDPPortRange)
	if tcpErr != nil || udpErr != nil {
		// Unreadable means "cannot prove it matches", so converge rather than
		// assume. Logged because it also means this parser and Pangolin
		// disagree about the syntax.
		log.Info("Could not parse Pangolin port ranges; forcing update",
			"tcp", existing.TCPPortRange, "udp", existing.UDPPortRange)
	}

	unchanged := tcpErr == nil && udpErr == nil &&
		existing.Name == d.name &&
		existing.Alias == d.alias &&
		existing.Destination == d.destination &&
		existing.DestinationPort == d.destinationPort &&
		existing.DisableICMP == d.disableICMP &&
		existing.Enabled == d.enabled &&
		currentTCP.Equal(d.tcp) &&
		currentUDP.Equal(d.udp) &&
		(len(d.siteIDs) != 1 || existing.SiteID == d.siteIDs[0])

	if unchanged {
		log.V(1).Info("Pangolin private resource already matches desired state", "niceID", d.niceID)
		return nil
	}

	destination := d.destination
	alias := d.alias
	req := &pangolin.UpdateSiteResourceRequest{
		Name:         d.name,
		Mode:         privateResourceMode,
		SiteID:       d.siteIDs[0],
		Destination:  &destination,
		Alias:        &alias,
		TCPPortRange: d.tcp.String(),
		UDPPortRange: d.udp.String(),
		DisableICMP:  &d.disableICMP,
		Enabled:      &d.enabled,
	}
	if len(d.siteIDs) > 1 {
		req.SiteIDs = d.siteIDs
	}
	if d.destinationPort != 0 {
		req.DestinationPort = &d.destinationPort
	}
	// A nil DestinationPort is sent as an explicit null, clearing a port that
	// an earlier single-port configuration set.

	if _, err := r.PangolinClient.UpdateSiteResource(ctx, strconv.Itoa(existing.ID), req); err != nil {
		if pangolin.IsNotImplemented(err) {
			return issuef(v1alpha1.ConditionAccepted, v1alpha1.ReasonUnsupportedByServer,
				"this Pangolin instance does not implement private resources: %v", err)
		}
		return fmt.Errorf("failed to update Pangolin private resource: %w", err)
	}
	return nil
}

// reconcilePrincipals converges role, user and client assignments through the
// dedicated sub-endpoints, which is the only way to observe what is currently
// assigned. Each is read first and written only on a difference.
// serverOwnedRoleIDs collects the roles Pangolin grants on its own.
//
// Pangolin attaches the organisation's admin role to every private resource
// and keeps it attached through any attempt to remove it. Comparing it against
// a spec that never asked for it reports a difference the controller cannot
// resolve: it writes the role set without the admin role, Pangolin retains it,
// and the next reconcile writes again -- a write per reconcile, forever.
//
// The role is recognised by the server's own IsAdmin flag rather than by name
// or identifier: a name match breaks the moment the role is renamed, and a
// fixed identifier assumes an ordering the API does not promise. Either would
// resume the loop silently.
func serverOwnedRoleIDs(roles []pangolin.Role) map[int]struct{} {
	owned := make(map[int]struct{})
	for _, role := range roles {
		if role.IsAdmin {
			owned[role.ID] = struct{}{}
		}
	}
	return owned
}

// managedRoleIDs is the part of a role set the controller is responsible for.
//
// Both sides of the comparison are filtered, not just the observed one. An
// operator is free to name the admin role explicitly, and if only the observed
// side were filtered that spec would read as a permanent difference -- the same
// loop, reached from the other direction.
func managedRoleIDs(ids []int, serverOwned map[int]struct{}) []int {
	managed := make([]int, 0, len(ids))
	for _, id := range ids {
		if _, owned := serverOwned[id]; owned {
			continue
		}
		managed = append(managed, id)
	}
	return managed
}

func roleIDsOf(roles []pangolin.Role) []int {
	ids := make([]int, 0, len(roles))
	for _, role := range roles {
		ids = append(ids, role.ID)
	}
	return ids
}

func (r *PangolinEndpointReconciler) reconcilePrincipals(ctx context.Context, siteResourceID string, roleIDs []int, userIDs []string, clientIDs []int) error {
	currentRoles, err := r.PangolinClient.ListSiteResourceRoles(ctx, siteResourceID)
	if err != nil {
		if !pangolin.IsNotImplemented(err) {
			return fmt.Errorf("failed to list private resource roles: %w", err)
		}
	} else {
		serverOwned := serverOwnedRoleIDs(currentRoles)
		current := managedRoleIDs(roleIDsOf(currentRoles), serverOwned)
		desired := managedRoleIDs(roleIDs, serverOwned)

		if !intSetsEqual(current, desired) {
			// Only the managed roles are written. A server-owned role is left
			// out rather than withdrawn: Pangolin would keep it regardless, and
			// asking is what turned a no-op into a write on every reconcile.
			if err := r.PangolinClient.SetSiteResourceRoles(ctx, siteResourceID, desired); err != nil && !pangolin.IsNotImplemented(err) {
				return fmt.Errorf("failed to set private resource roles: %w", err)
			}
		}
	}

	currentUsers, err := r.PangolinClient.ListSiteResourceUsers(ctx, siteResourceID)
	if err != nil {
		if !pangolin.IsNotImplemented(err) {
			return fmt.Errorf("failed to list private resource users: %w", err)
		}
	} else if !stringSetsEqual(currentUsers, userIDs) {
		if err := r.PangolinClient.SetSiteResourceUsers(ctx, siteResourceID, userIDs); err != nil && !pangolin.IsNotImplemented(err) {
			return fmt.Errorf("failed to set private resource users: %w", err)
		}
	}

	currentClients, err := r.PangolinClient.ListSiteResourceClients(ctx, siteResourceID)
	if err != nil {
		if !pangolin.IsNotImplemented(err) {
			return fmt.Errorf("failed to list private resource clients: %w", err)
		}
	} else if !intSetsEqual(currentClients, clientIDs) {
		if err := r.PangolinClient.SetSiteResourceClients(ctx, siteResourceID, clientIDs); err != nil && !pangolin.IsNotImplemented(err) {
			return fmt.Errorf("failed to set private resource clients: %w", err)
		}
	}

	return nil
}

// findSiteResource locates the private resource this endpoint owns: by the
// recorded identifier, else by the deterministic nice ID.
//
// There is no match-on-attributes fallback. The nice ID is a pure function of
// the object's namespace and name, so identity never has to be guessed -- and
// a resource that merely looks similar is never claimed.
//
// Both lookups read the site listing, and only a complete listing that holds no
// match reports absence. Any other failure is returned as an error, so the
// caller cannot mistake "the lookup did not work" for "there is nothing there"
// and create a second resource alongside the first.
func (r *PangolinEndpointReconciler) findSiteResource(ctx context.Context, ep *v1alpha1.PangolinEndpoint, siteID int) (*pangolin.SiteResource, error) {
	site := strconv.Itoa(siteID)

	if id := ep.Status.SiteResourceID; id != "" {
		existing, err := r.PangolinClient.GetSiteResource(ctx, site, id)
		if err == nil {
			return existing, nil
		}
		if !pangolin.IsNotFound(err) {
			return nil, fmt.Errorf("failed to get Pangolin private resource %s: %w", id, err)
		}
		// Deleted out from under us; fall through to the identity lookup.
	}

	existing, err := r.PangolinClient.GetSiteResourceByNiceID(ctx, site, ep.Status.NiceID)
	if err == nil {
		return existing, nil
	}
	if pangolin.IsNotFound(err) {
		return nil, nil
	}
	if pangolin.IsAmbiguous(err) {
		// Pangolin does not enforce niceId uniqueness, so more than one match
		// is possible and none of them is identifiably this endpoint's. Picking
		// one would reprogram a resource the controller may not own; an
		// operator has to resolve the collision.
		return nil, issuef(v1alpha1.ConditionProgrammed, v1alpha1.ReasonIdentityAmbiguous,
			"cannot identify this endpoint's private resource: %v", err)
	}
	return nil, fmt.Errorf("failed to look up Pangolin private resource by nice ID %q: %w", ep.Status.NiceID, err)
}

func (r *PangolinEndpointReconciler) deleteSiteResource(ctx context.Context, ep *v1alpha1.PangolinEndpoint) error {
	log := log.FromContext(ctx)

	id := ep.Status.SiteResourceID
	if id == "" {
		// Nothing was ever recorded. Recovering the identity here would need a
		// site lookup that can fail for reasons unrelated to deletion, so an
		// endpoint that never programmed anything is released immediately.
		log.Info("No Pangolin private resource recorded; nothing to delete", "name", ep.Name)
		return nil
	}

	if err := r.PangolinClient.DeleteSiteResource(ctx, id); err != nil {
		if pangolin.IsNotFound(err) || pangolin.IsNotImplemented(err) {
			log.Info("Pangolin private resource already absent", "siteResourceID", id)
			return nil
		}
		return fmt.Errorf("failed to delete Pangolin private resource %s: %w", id, err)
	}

	log.Info("Deleted Pangolin private resource", "siteResourceID", id)
	return nil
}

// niceIDFor derives the controller's identity for an endpoint.
//
// The value must survive as a Pangolin nice ID, so a name that cannot be
// expressed is refused rather than mangled: truncating or substituting
// characters could make two distinct endpoints collide on one identity, which
// would have them fight over a single Pangolin resource.
func (r *PangolinEndpointReconciler) niceIDFor(ep *v1alpha1.PangolinEndpoint) (string, error) {
	prefix := r.ResourcePrefix
	if prefix == "" {
		prefix = "pangolin-controller"
	}

	niceID := fmt.Sprintf("%s-%s-%s", prefix, ep.Namespace, ep.Name)
	if !niceIDPattern.MatchString(niceID) {
		return "", issuef(v1alpha1.ConditionAccepted, v1alpha1.ReasonIdentityInvalid,
			"cannot derive a Pangolin identity from %q: %q contains characters outside [a-zA-Z0-9-]",
			ep.Namespace+"/"+ep.Name, niceID)
	}
	if len(niceID) > maxNiceIDLength {
		return "", issuef(v1alpha1.ConditionAccepted, v1alpha1.ReasonIdentityTooLong,
			"derived Pangolin identity %q is %d characters; the limit is %d",
			niceID, len(niceID), maxNiceIDLength)
	}
	return niceID, nil
}

// aliasFor returns the internal FQDN clients dial.
func (r *PangolinEndpointReconciler) aliasFor(ep *v1alpha1.PangolinEndpoint) (string, error) {
	if alias := strings.TrimSpace(ep.Spec.Private.Alias); alias != "" {
		return alias, nil
	}

	suffix := strings.Trim(strings.TrimSpace(r.AliasSuffix), ".")
	if suffix == "" {
		return "", issuef(v1alpha1.ConditionAccepted, v1alpha1.ReasonAliasSuffixNotConfigured,
			"no alias suffix is configured: set --private-alias-suffix, or set spec.private.alias on this object")
	}
	if !strings.Contains(suffix, ".") {
		return "", issuef(v1alpha1.ConditionAccepted, v1alpha1.ReasonAliasSuffixNotConfigured,
			"configured alias suffix %q is not a domain: Pangolin requires a fully qualified alias", suffix)
	}
	for _, label := range strings.Split(suffix, ".") {
		if !aliasLabelPattern.MatchString(label) {
			return "", issuef(v1alpha1.ConditionAccepted, v1alpha1.ReasonAliasSuffixNotConfigured,
				"configured alias suffix %q has an invalid label %q", suffix, label)
		}
	}

	return fmt.Sprintf("%s.%s.%s", ep.Name, ep.Namespace, suffix), nil
}

// resolveDestination resolves the backing Service to the address Pangolin
// should forward to.
//
// SPIKE (task 1.3): this sends the Service's cluster DNS name, mirroring what
// the Ingress path already does for targets. Private resources travel a
// different data path, so if Pangolin cannot resolve cluster DNS there, this
// must fall back to the Service's ClusterIP -- which would require watching
// Services and reconciling on ClusterIP change.
func (r *PangolinEndpointReconciler) resolveDestination(ctx context.Context, ep *v1alpha1.PangolinEndpoint) (string, *corev1.Service, error) {
	svc := &corev1.Service{}
	err := r.Get(ctx, types.NamespacedName{Name: ep.Spec.BackendRef.Name, Namespace: ep.Namespace}, svc)
	if err != nil {
		if apierrors.IsNotFound(err) {
			return "", nil, issuef(v1alpha1.ConditionResolvedRefs, v1alpha1.ReasonBackendNotFound,
				"Service %s/%s not found", ep.Namespace, ep.Spec.BackendRef.Name)
		}
		return "", nil, fmt.Errorf("failed to get backing Service %s/%s: %w", ep.Namespace, ep.Spec.BackendRef.Name, err)
	}

	if svc.Spec.Type == corev1.ServiceTypeExternalName {
		return "", nil, issuef(v1alpha1.ConditionResolvedRefs, v1alpha1.ReasonBackendUnsupported,
			"Service %s/%s is of type ExternalName and has no cluster-local address", svc.Namespace, svc.Name)
	}
	if strings.EqualFold(svc.Spec.ClusterIP, corev1.ClusterIPNone) {
		return "", nil, issuef(v1alpha1.ConditionResolvedRefs, v1alpha1.ReasonBackendUnsupported,
			"Service %s/%s is headless; its name resolves to individual pod IPs rather than a stable address", svc.Namespace, svc.Name)
	}

	clusterDomain := r.ClusterDomain
	if clusterDomain == "" {
		clusterDomain = defaultClusterDomain
	}
	return fmt.Sprintf("%s.%s.%s", svc.Name, svc.Namespace, clusterDomain), svc, nil
}

// resolvePorts returns the declared ports, or the backing Service's ports when
// none are declared.
func (r *PangolinEndpointReconciler) resolvePorts(private *v1alpha1.PrivateEndpointSpec, svc *corev1.Service) (portSet, portSet, error) {
	if len(private.Ports) > 0 {
		tcp, udp := portSetsFromSpec(private.Ports)
		return tcp, udp, nil
	}

	tcp, udp, skippedSCTP := portSetsFromService(svc)
	if tcp.Empty() && udp.Empty() {
		msg := fmt.Sprintf("Service %s/%s exposes no TCP or UDP ports to derive from", svc.Namespace, svc.Name)
		if skippedSCTP {
			msg += "; SCTP ports cannot be exposed on a Pangolin private resource"
		}
		return portSet{}, portSet{}, issuef(v1alpha1.ConditionResolvedRefs, v1alpha1.ReasonBackendUnsupported, "%s", msg)
	}
	return tcp, udp, nil
}

// resolveSites maps the endpoint's site references to Pangolin site IDs,
// falling back to the controller's configured default site.
func (r *PangolinEndpointReconciler) resolveSites(ctx context.Context, ep *v1alpha1.PangolinEndpoint) ([]int, error) {
	refs := ep.Spec.SiteRefs
	if len(refs) == 0 {
		if r.SiteNiceID == "" {
			return nil, issuef(v1alpha1.ConditionResolvedRefs, v1alpha1.ReasonSiteNotFound,
				"no site configured: set spec.siteRefs, or start the controller with --pangolin-site-nice-id")
		}
		refs = []string{r.SiteNiceID}
	}

	ids := make([]int, 0, len(refs))
	for _, ref := range refs {
		site, err := r.getSite(ctx, ref)
		if err != nil {
			return nil, err
		}
		ids = append(ids, site.ID)
	}
	return ids, nil
}

// getSite returns a site by nice ID, caching successful lookups for the life
// of the process. A site's ID does not change; a renamed or deleted site is
// picked up on restart, matching how the Ingress path treats site data.
func (r *PangolinEndpointReconciler) getSite(ctx context.Context, niceID string) (*pangolin.Site, error) {
	r.siteMu.RLock()
	site, ok := r.sites[niceID]
	r.siteMu.RUnlock()
	if ok {
		return site, nil
	}

	site, err := r.PangolinClient.GetSiteByNiceID(ctx, niceID)
	if err != nil {
		if pangolin.IsNotFound(err) {
			return nil, issuef(v1alpha1.ConditionResolvedRefs, v1alpha1.ReasonSiteNotFound,
				"Pangolin site %q not found", niceID)
		}
		return nil, fmt.Errorf("failed to get Pangolin site %q: %w", niceID, err)
	}

	r.siteMu.Lock()
	if r.sites == nil {
		r.sites = map[string]*pangolin.Site{}
	}
	r.sites[niceID] = site
	r.siteMu.Unlock()

	return site, nil
}

// resolvePrincipals maps the named principals to Pangolin identifiers.
func (r *PangolinEndpointReconciler) resolvePrincipals(ctx context.Context, access *v1alpha1.AccessSpec) ([]int, []string, []int, error) {
	if access == nil {
		return []int{}, []string{}, []int{}, nil
	}

	roleIDs, err := r.principals.resolveRoles(ctx, access.Roles)
	if err != nil {
		return nil, nil, nil, principalIssue(err)
	}
	userIDs, err := r.principals.resolveUsers(ctx, access.Users)
	if err != nil {
		return nil, nil, nil, principalIssue(err)
	}
	clientIDs, err := r.principals.resolveClients(ctx, access.Clients)
	if err != nil {
		return nil, nil, nil, principalIssue(err)
	}

	return roleIDs, userIDs, clientIDs, nil
}

// principalIssue turns a resolution failure into an operator-fixable condition,
// leaving genuine API failures as errors.
func principalIssue(err error) error {
	switch {
	case errors.Is(err, errPrincipalNotFound):
		return issuef(v1alpha1.ConditionResolvedRefs, v1alpha1.ReasonPrincipalNotFound, "%s", err.Error())
	case errors.Is(err, errPrincipalAmbiguous):
		return issuef(v1alpha1.ConditionResolvedRefs, v1alpha1.ReasonPrincipalAmbiguous, "%s", err.Error())
	default:
		return err
	}
}

// applyIssue records a failed reconcile on the object's conditions.
func (r *PangolinEndpointReconciler) applyIssue(ep *v1alpha1.PangolinEndpoint, issue *endpointIssue) {
	setCondition(ep, issue.condition, metav1.ConditionFalse, issue.reason, issue.message)
	setCondition(ep, v1alpha1.ConditionReady, metav1.ConditionFalse, issue.reason, issue.message)

	// A condition earlier in the chain must have held for a later one to have
	// been evaluated at all.
	if issue.condition == v1alpha1.ConditionResolvedRefs {
		setCondition(ep, v1alpha1.ConditionAccepted, metav1.ConditionTrue, v1alpha1.ReasonReconciled, "spec accepted")
	}
}

// updateStatus writes observed state, setting the success conditions when the
// reconcile got far enough to program Pangolin.
func (r *PangolinEndpointReconciler) updateStatus(ctx context.Context, ep *v1alpha1.PangolinEndpoint) error {
	ep.Status.ObservedGeneration = ep.Generation

	if ep.Status.SiteResourceID != "" && !hasFalseCondition(ep) {
		setCondition(ep, v1alpha1.ConditionAccepted, metav1.ConditionTrue, v1alpha1.ReasonReconciled, "spec accepted")
		setCondition(ep, v1alpha1.ConditionResolvedRefs, metav1.ConditionTrue, v1alpha1.ReasonReconciled, "all references resolved")
		setCondition(ep, v1alpha1.ConditionProgrammed, metav1.ConditionTrue, v1alpha1.ReasonReconciled, "Pangolin private resource is up to date")

		if grantsNoPrincipals(ep) {
			// Pangolin attaches the organisation's admin role to every private
			// resource, so "no access" would be untrue. What is missing is
			// access for the principals an operator would name.
			setCondition(ep, v1alpha1.ConditionReady, metav1.ConditionFalse, v1alpha1.ReasonNoPrincipalsGranted,
				"endpoint names no client, role or user; only organisation administrators can reach it "+
					"through the role Pangolin grants implicitly")
		} else {
			setCondition(ep, v1alpha1.ConditionReady, metav1.ConditionTrue, v1alpha1.ReasonReconciled, "endpoint is reachable by its principals")
		}
	}

	if err := r.Status().Update(ctx, ep); err != nil {
		return fmt.Errorf("failed to update PangolinEndpoint status: %w", err)
	}
	return nil
}

func hasFalseCondition(ep *v1alpha1.PangolinEndpoint) bool {
	for _, t := range []string{v1alpha1.ConditionAccepted, v1alpha1.ConditionResolvedRefs, v1alpha1.ConditionProgrammed} {
		if c := meta.FindStatusCondition(ep.Status.Conditions, t); c != nil && c.Status == metav1.ConditionFalse {
			// Only treat it as failing if it reflects the current generation.
			if c.ObservedGeneration == ep.Generation {
				return true
			}
		}
	}
	return false
}

func grantsNoPrincipals(ep *v1alpha1.PangolinEndpoint) bool {
	access := ep.Spec.Private.Access
	if access == nil {
		return true
	}
	return len(access.Clients) == 0 && len(access.Roles) == 0 && len(access.Users) == 0
}

func setCondition(ep *v1alpha1.PangolinEndpoint, conditionType string, status metav1.ConditionStatus, reason, message string) {
	meta.SetStatusCondition(&ep.Status.Conditions, metav1.Condition{
		Type:               conditionType,
		Status:             status,
		Reason:             reason,
		Message:            message,
		ObservedGeneration: ep.Generation,
	})
}

// requeueAfter is the retry delay for an operator-fixable condition. A small
// margin is added over the refresh interval so a requeue never lands
// fractionally before a cache cooldown expires, which would waste a cycle
// skipping the refetch it came back to perform.
func (r *PangolinEndpointReconciler) requeueAfter() time.Duration {
	interval := r.NameCacheRefreshInterval
	if interval <= 0 {
		interval = defaultDomainCacheRefreshInterval
	}
	return interval + interval/10
}

// initPangolinClient initializes the Pangolin API client from the configured
// Secret. There is no per-Secret watch: restart the controller to pick up a
// rotated key, matching the Ingress path.
func (r *PangolinEndpointReconciler) initPangolinClient(ctx context.Context) error {
	secret := &corev1.Secret{}
	err := r.Get(ctx, types.NamespacedName{Name: r.APIKeySecret, Namespace: r.APIKeyNamespace}, secret)
	if err != nil {
		return fmt.Errorf("failed to get API key secret: %w", err)
	}

	apiKey, ok := secret.Data["api-key"]
	if !ok {
		return fmt.Errorf("api-key not found in secret %s/%s", r.APIKeyNamespace, r.APIKeySecret)
	}

	r.PangolinClient = pangolin.NewClient(r.PangolinBaseURL, string(apiKey), r.OrgID)
	r.principals = newPrincipalResolver(r.PangolinClient, r.NameCacheRefreshInterval)
	return nil
}

// SetupWithManager registers the reconciler.
//
// GenerationChangedPredicate is sufficient here, unlike the Ingress path: this
// controller's own writes land in status and in metadata, neither of which
// bumps the generation, so there is no write-watch loop to guard against.
func (r *PangolinEndpointReconciler) SetupWithManager(mgr ctrl.Manager) error {
	if r.Recorder == nil {
		r.Recorder = mgr.GetEventRecorderFor("pangolin-endpoint-controller")
	}
	return ctrl.NewControllerManagedBy(mgr).
		For(&v1alpha1.PangolinEndpoint{}).
		WithEventFilter(predicate.GenerationChangedPredicate{}).
		Complete(r)
}
