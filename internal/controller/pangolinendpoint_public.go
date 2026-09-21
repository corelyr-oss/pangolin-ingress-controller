package controller

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/log"

	"github.com/vinzenz/pangolin-ingress-controller/api/v1alpha1"
	"github.com/vinzenz/pangolin-ingress-controller/internal/pangolin"
)

// reconcilePublic converges a spec.public endpoint onto one Pangolin raw
// resource and its targets.
//
// Identity is the same derived value the private branch uses. Raw-resource
// create accepts no niceId, so the resource is created with that value as its
// name, its ID is recorded, and only then is the niceId set by update. See
// findPublicResource for how each of those states is recovered.
func (r *PangolinEndpointReconciler) reconcilePublic(ctx context.Context, ep *v1alpha1.PangolinEndpoint) error {
	public := ep.Spec.Public

	niceID, err := r.niceIDFor(ep)
	if err != nil {
		return err
	}
	ep.Status.NiceID = niceID

	protocol := public.Protocol
	if protocol == "" {
		protocol = v1alpha1.ProtocolTCP
	}
	mode := strings.ToLower(string(protocol))

	host, svc, err := r.resolveDestination(ctx, ep)
	if err != nil {
		return err
	}

	servicePort := public.ProxyPort
	if public.ServicePort != nil {
		servicePort = *public.ServicePort
	}
	if !serviceExposes(svc, protocol, servicePort) {
		return issuef(v1alpha1.ConditionResolvedRefs, v1alpha1.ReasonBackendUnsupported,
			"Service %s/%s exposes no %s port %d to forward to", svc.Namespace, svc.Name, protocol, servicePort)
	}

	proxyPort := int(public.ProxyPort)
	ep.Status.Address = ""
	ep.Status.AssignedAddress = ""
	if protocol == v1alpha1.ProtocolUDP {
		ep.Status.ResolvedPorts = &v1alpha1.ResolvedPorts{UDP: strconv.Itoa(proxyPort)}
	} else {
		ep.Status.ResolvedPorts = &v1alpha1.ResolvedPorts{TCP: strconv.Itoa(proxyPort)}
	}

	siteIDs, err := r.resolveSites(ctx, ep)
	if err != nil {
		return err
	}

	listing := &resourceListing{client: r.PangolinClient}
	existing, err := r.findPublicResource(ctx, ep, niceID, mode, listing)
	if err != nil {
		return err
	}

	if existing == nil {
		if err := proxyPortFree(ctx, listing, mode, proxyPort, 0); err != nil {
			return err
		}
		created, err := r.PangolinClient.CreateRawResource(ctx, &pangolin.CreateRawResourceRequest{
			Name:      niceID,
			Mode:      mode,
			ProxyPort: proxyPort,
		})
		if err != nil {
			return fmt.Errorf("failed to create Pangolin raw resource: %w", err)
		}
		// Recorded before anything else can fail: the error path persists
		// status, so a failure in the naming update below still leaves the
		// next reconcile holding this resource's ID instead of creating
		// another.
		existing = created
	}
	ep.Status.ResourceID = strconv.Itoa(existing.ID)

	desired := desiredPublicResource{
		niceID:    niceID,
		proxyPort: proxyPort,
		enabled:   ep.Spec.Enabled == nil || *ep.Spec.Enabled,
	}
	if err := r.updatePublicResourceIfChanged(ctx, existing, desired, mode, listing); err != nil {
		return err
	}

	return r.reconcilePublicTargets(ctx, ep.Status.ResourceID, siteIDs, host, int(servicePort))
}

type desiredPublicResource struct {
	niceID    string
	proxyPort int
	enabled   bool
}

// resourceListing fetches the organisation's resources at most once per
// reconcile, and only if something needs them.
type resourceListing struct {
	client    *pangolin.Client
	resources []pangolin.Resource
	loaded    bool
}

func (l *resourceListing) get(ctx context.Context) ([]pangolin.Resource, error) {
	if l.loaded {
		return l.resources, nil
	}
	resources, err := l.client.ListResources(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to list Pangolin resources: %w", err)
	}
	l.resources, l.loaded = resources, true
	return resources, nil
}

// findPublicResource locates the raw resource this endpoint owns, in order of
// how strongly each clue identifies it:
//
//  1. the recorded ID -- written by this controller, so ours by construction;
//  2. the derived niceId, which Pangolin keeps unique;
//  3. the derived name, for a resource created but not yet given its niceId
//     (a crash between create and the status write). The name is the only
//     caller-chosen value create accepts, and it is deterministic, which is
//     the standard the private branch applies to its niceId.
//
// The proxy port is never used to identify a resource. A port held by another
// owner and an orphan of this endpoint look the same, and claiming the former
// would be a hijack; proxyPortFree reports that case instead.
//
// A failed listing is an error and never "absent", so a lookup that did not
// work cannot lead to a duplicate create.
func (r *PangolinEndpointReconciler) findPublicResource(ctx context.Context, ep *v1alpha1.PangolinEndpoint, niceID, mode string, listing *resourceListing) (*pangolin.Resource, error) {
	if id := ep.Status.ResourceID; id != "" {
		existing, err := r.PangolinClient.GetResource(ctx, id)
		if err == nil {
			if existing.Mode != mode {
				return nil, issuef(v1alpha1.ConditionProgrammed, v1alpha1.ReasonIdentityAmbiguous,
					"recorded Pangolin resource %s has mode %q, not %q; delete and recreate the endpoint", id, existing.Mode, mode)
			}
			return existing, nil
		}
		if !pangolin.IsNotFound(err) {
			return nil, fmt.Errorf("failed to get Pangolin resource %s: %w", id, err)
		}
		// Deleted out from under us; fall through to the identity lookups.
	}

	resources, err := listing.get(ctx)
	if err != nil {
		return nil, err
	}

	var byNiceID, byName []pangolin.Resource
	for _, res := range resources {
		switch {
		case res.NiceID == niceID:
			byNiceID = append(byNiceID, res)
		case res.Name == niceID && res.Mode == mode:
			byName = append(byName, res)
		}
	}

	if len(byNiceID) > 0 {
		if len(byNiceID) > 1 {
			return nil, issuef(v1alpha1.ConditionProgrammed, v1alpha1.ReasonIdentityAmbiguous,
				"%d Pangolin resources carry nice ID %q; resolve the duplicate in Pangolin", len(byNiceID), niceID)
		}
		found := byNiceID[0]
		if found.Mode != mode {
			return nil, issuef(v1alpha1.ConditionProgrammed, v1alpha1.ReasonIdentityAmbiguous,
				"Pangolin resource %d carries this endpoint's nice ID %q but has mode %q, not %q; it is not modified",
				found.ID, niceID, found.Mode, mode)
		}
		return &found, nil
	}

	switch len(byName) {
	case 0:
		return nil, nil
	case 1:
		log.FromContext(ctx).Info("Recovered Pangolin raw resource by name; its nice ID will be set",
			"resourceID", byName[0].ID, "niceID", niceID)
		return &byName[0], nil
	default:
		return nil, issuef(v1alpha1.ConditionProgrammed, v1alpha1.ReasonIdentityAmbiguous,
			"%d %s resources are named %q and none carries the nice ID; resolve the duplicate in Pangolin",
			len(byName), mode, niceID)
	}
}

// proxyPortFree refuses a (mode, port) another raw resource already holds.
// Pangolin accepts the duplicate; the entrypoint can route to only one of
// them, so the second would silently black-hole or steal traffic.
func proxyPortFree(ctx context.Context, listing *resourceListing, mode string, port, ownID int) error {
	resources, err := listing.get(ctx)
	if err != nil {
		return err
	}
	for _, res := range resources {
		if res.ID != ownID && res.Mode == mode && res.ProxyPort == port {
			return issuef(v1alpha1.ConditionProgrammed, v1alpha1.ReasonProxyPortInUse,
				"%s proxy port %d is already held by Pangolin resource %q (id %d)",
				strings.ToUpper(mode), port, res.NiceID, res.ID)
		}
	}
	return nil
}

// updatePublicResourceIfChanged sends only the fields that differ. niceId in
// particular is sent only when it changes: Pangolin enforces its uniqueness,
// and re-sending an unchanged one buys nothing but a chance of a conflict.
func (r *PangolinEndpointReconciler) updatePublicResourceIfChanged(ctx context.Context, existing *pangolin.Resource, d desiredPublicResource, mode string, listing *resourceListing) error {
	req := &pangolin.UpdateRawResourceRequest{}
	changed := false

	if existing.Name != d.niceID {
		req.Name, changed = d.niceID, true
	}
	if existing.NiceID != d.niceID {
		req.NiceID, changed = d.niceID, true
	}
	if existing.ProxyPort != d.proxyPort {
		if err := proxyPortFree(ctx, listing, mode, d.proxyPort, existing.ID); err != nil {
			return err
		}
		port := d.proxyPort
		req.ProxyPort, changed = &port, true
	}
	if existing.Enabled != d.enabled {
		enabled := d.enabled
		req.Enabled, changed = &enabled, true
	}

	if !changed {
		log.FromContext(ctx).V(1).Info("Pangolin raw resource already matches desired state", "niceID", d.niceID)
		return nil
	}

	if _, err := r.PangolinClient.UpdateRawResource(ctx, strconv.Itoa(existing.ID), req); err != nil {
		if pangolin.IsConflict(err) && req.NiceID != "" {
			return issuef(v1alpha1.ConditionProgrammed, v1alpha1.ReasonIdentityAmbiguous,
				"another Pangolin resource already carries nice ID %q: %v", d.niceID, err)
		}
		return fmt.Errorf("failed to update Pangolin raw resource: %w", err)
	}
	return nil
}

// reconcilePublicTargets keeps exactly one enabled target per site, pointing at
// host:port. Every field the controller sets is part of the match, so there is
// nothing to update in place: a target either matches or is replaced. Missing
// targets are created before stale ones are deleted, so a port change never
// leaves the resource with no backend at all.
func (r *PangolinEndpointReconciler) reconcilePublicTargets(ctx context.Context, resourceID string, siteIDs []int, host string, port int) error {
	current, err := r.PangolinClient.ListTargets(ctx, resourceID)
	if err != nil {
		return fmt.Errorf("failed to list targets of Pangolin resource %s: %w", resourceID, err)
	}

	wanted := make(map[int]bool, len(siteIDs))
	for _, id := range siteIDs {
		wanted[id] = true
	}

	matched := make(map[int]bool, len(siteIDs))
	var stale []pangolin.Target
	for _, t := range current {
		if wanted[t.SiteID] && !matched[t.SiteID] && t.IP == host && t.Port == port && t.Enabled {
			matched[t.SiteID] = true
			continue
		}
		stale = append(stale, t)
	}

	for _, siteID := range siteIDs {
		if matched[siteID] {
			continue
		}
		matched[siteID] = true
		if _, err := r.PangolinClient.CreateTarget(ctx, resourceID, &pangolin.CreateTargetRequest{
			SiteID:  siteID,
			IP:      host,
			Port:    port,
			Enabled: true,
		}); err != nil {
			return fmt.Errorf("failed to create target on Pangolin resource %s: %w", resourceID, err)
		}
	}

	for _, t := range stale {
		if err := r.PangolinClient.DeleteTarget(ctx, strconv.Itoa(t.ID)); err != nil {
			return fmt.Errorf("failed to delete stale target %d on Pangolin resource %s: %w", t.ID, resourceID, err)
		}
	}
	return nil
}

func (r *PangolinEndpointReconciler) deletePublicResource(ctx context.Context, ep *v1alpha1.PangolinEndpoint) error {
	log := log.FromContext(ctx)

	id := ep.Status.ResourceID
	if err := r.PangolinClient.DeleteResource(ctx, id); err != nil {
		if pangolin.IsNotFound(err) {
			log.Info("Pangolin raw resource already absent", "resourceID", id)
			return nil
		}
		return fmt.Errorf("failed to delete Pangolin raw resource %s: %w", id, err)
	}

	log.Info("Deleted Pangolin raw resource", "resourceID", id)
	return nil
}

// serviceExposes reports whether svc has port with the given protocol. An
// unset Service port protocol means TCP, as the API server defaults it.
func serviceExposes(svc *corev1.Service, protocol v1alpha1.Protocol, port int32) bool {
	for _, p := range svc.Spec.Ports {
		proto := p.Protocol
		if proto == "" {
			proto = corev1.ProtocolTCP
		}
		if p.Port == port && string(proto) == string(protocol) {
			return true
		}
	}
	return false
}
