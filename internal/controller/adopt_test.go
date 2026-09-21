package controller

import (
	"context"
	"strconv"
	"strings"
	"testing"

	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/vinzenz/pangolin-ingress-controller/internal/pangolin"
)

// seedResource puts a resource into the fake as if an earlier controller, or a
// person, had created it.
func seedResource(p *statefulPangolin, subdomain string) *pangolin.Resource {
	p.nextID++
	res := &pangolin.Resource{ID: p.nextID, Name: "pre-existing " + fakeFullDomain(subdomain),
		Subdomain: subdomain, DomainID: "dom-1", FullDomain: fakeFullDomain(subdomain), HTTP: true, Mode: "http"}
	p.resources[res.ID] = res
	return res
}

// A host whose annotation was lost but whose resource still exists must be
// adopted on the create conflict. The listing carries no subdomain, so the old
// (subdomain, domainId) match never found a subdomain host.
func TestAdopt_SubdomainHostIsAdopted(t *testing.T) {
	ing := mhIngress(nil, mhRule("app.example.com", mhPath("/", "app", 80)))
	r, c, p := newMultiRouteReconciler(t, ing, mhService("app", 80))
	existing := seedResource(p, "app")
	key := client.ObjectKeyFromObject(ing)

	reconcileTimes(t, r, key, 2)

	if len(p.resources) != 1 {
		t.Errorf("Pangolin holds %d resources, want the adopted one only", len(p.resources))
	}
	if ids := readResourceIDs(getIngress(t, c, key)); ids["app.example.com"] != itoa(existing.ID) {
		t.Errorf("resource-ids = %v, want app.example.com adopted as %d", ids, existing.ID)
	}
}

// An apex host has subdomain "", which every listed resource also decoded to.
// The old match therefore adopted whichever resource on the domain came first
// -- here a sibling the Ingress does not own.
func TestAdopt_ApexHostDoesNotAdoptASibling(t *testing.T) {
	ing := mhIngress(nil, mhRule("example.com", mhPath("/", "web", 80)))
	r, c, p := newMultiRouteReconciler(t, ing, mhService("web", 80))
	sibling := seedResource(p, "nav")
	apex := seedResource(p, "")
	key := client.ObjectKeyFromObject(ing)

	reconcileTimes(t, r, key, 2)

	if ids := readResourceIDs(getIngress(t, c, key)); ids["example.com"] != itoa(apex.ID) {
		t.Errorf("resource-ids = %v, want example.com adopted as %d", ids, apex.ID)
	}
	if got := p.resources[sibling.ID]; got.FullDomain != "nav.example.com" || got.Name != sibling.Name {
		t.Errorf("sibling was modified: %+v", got)
	}
	for _, tgt := range p.targets {
		if tgt.resourceID == sibling.ID {
			t.Errorf("sibling received a target: %+v", tgt.Target)
		}
	}
}

func TestAdopt_ResourceBeyondFirstPageIsAdopted(t *testing.T) {
	ing := mhIngress(nil, mhRule("app.example.com", mhPath("/", "app", 80)))
	r, c, p := newMultiRouteReconciler(t, ing, mhService("app", 80))
	p.pageCap = 2
	for _, sub := range []string{"a", "b", "c", "d", "e"} {
		seedResource(p, sub)
	}
	existing := seedResource(p, "app")
	key := client.ObjectKeyFromObject(ing)

	reconcileTimes(t, r, key, 1)

	if ids := readResourceIDs(getIngress(t, c, key)); ids["app.example.com"] != itoa(existing.ID) {
		t.Errorf("resource-ids = %v, want app.example.com adopted as %d from page 3", ids, existing.ID)
	}
	if len(p.resources) != 6 {
		t.Errorf("Pangolin holds %d resources, want no new one", len(p.resources))
	}
}

func TestAdopt_AmbiguousMatchIsRefused(t *testing.T) {
	ing := mhIngress(nil, mhRule("app.example.com", mhPath("/", "app", 80)))
	r, c, p := newMultiRouteReconciler(t, ing, mhService("app", 80))
	first := seedResource(p, "app")
	second := seedResource(p, "app")
	key := client.ObjectKeyFromObject(ing)

	_, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: key})
	if err == nil || !strings.Contains(err.Error(), "refusing to adopt") {
		t.Fatalf("reconcile error = %v, want an ambiguity refusal", err)
	}
	if ids := readResourceIDs(getIngress(t, c, key)); len(ids) != 0 {
		t.Errorf("resource-ids = %v, want nothing recorded", ids)
	}
	if p.createdTargets != 0 {
		t.Errorf("createdTargets = %d, want none", p.createdTargets)
	}
	for _, res := range []*pangolin.Resource{first, second} {
		if p.resources[res.ID].Name != res.Name {
			t.Errorf("resource %d was modified", res.ID)
		}
	}
}

func itoa(n int) string { return strconv.Itoa(n) }
