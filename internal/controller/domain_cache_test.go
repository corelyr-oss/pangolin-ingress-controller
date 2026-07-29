package controller

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/vinzenz/pangolin-ingress-controller/internal/pangolin"
)

// fakeDomainLister is a domainLister that serves canned results, can be made to
// fail, and counts calls so tests can assert on API traffic.
type fakeDomainLister struct {
	mu      sync.Mutex
	calls   int
	domains []pangolin.Domain
	err     error

	// block, when non-nil, holds every call until it is closed. Used to force
	// concurrent callers to pile up on an in-flight fetch.
	block chan struct{}
}

func (f *fakeDomainLister) ListDomains(ctx context.Context) ([]pangolin.Domain, error) {
	f.mu.Lock()
	f.calls++
	block, err := f.block, f.err
	domains := append([]pangolin.Domain(nil), f.domains...)
	f.mu.Unlock()

	if block != nil {
		<-block
	}
	if err != nil {
		return nil, err
	}
	return domains, nil
}

func (f *fakeDomainLister) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

func (f *fakeDomainLister) setDomains(domains []pangolin.Domain) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.domains = domains
}

func (f *fakeDomainLister) setErr(err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.err = err
}

// warmDomainCache returns a cache pre-populated with domains, as if a fetch had
// already happened. The lister fails if called, so any test using it asserts
// implicitly that no API traffic occurs.
func warmDomainCache(domains []pangolin.Domain, interval time.Duration) *domainCache {
	c := newDomainCache(&fakeDomainLister{err: errors.New("unexpected ListDomains call")}, interval)
	c.entries = domains
	c.fetchedAt = c.now()
	c.lastAttempt = c.fetchedAt
	return c
}

// fixedClock returns a now func reading from a caller-controlled instant, so
// cooldown behaviour is testable without sleeping.
func fixedClock(t *time.Time) func() time.Time {
	return func() time.Time { return *t }
}

func TestDomainCache_HitMakesNoAPICall(t *testing.T) {
	lister := &fakeDomainLister{domains: []pangolin.Domain{{ID: "id-a", BaseDomain: "example.com"}}}
	c := newDomainCache(lister, time.Minute)

	if _, err := c.get(context.Background()); err != nil {
		t.Fatalf("get: %v", err)
	}
	if got := lister.callCount(); got != 1 {
		t.Fatalf("cold fetch calls = %d, want 1", got)
	}

	for i := 0; i < 5; i++ {
		if _, err := c.get(context.Background()); err != nil {
			t.Fatalf("get: %v", err)
		}
	}
	if got := lister.callCount(); got != 1 {
		t.Errorf("calls after warm gets = %d, want 1", got)
	}
}

func TestResolveHostDomain_RefreshOnMissFindsNewDomain(t *testing.T) {
	// The cache predates the registration of mod.tf — the exact shape of the
	// incident this change exists to fix.
	lister := &fakeDomainLister{domains: []pangolin.Domain{{ID: "id-tunnel", BaseDomain: "tunnel.tf"}}}
	clock := time.Now()
	c := newDomainCache(lister, time.Minute)
	c.now = fixedClock(&clock)
	r := &IngressReconciler{domains: c}

	if _, _, err := r.resolveHostDomain(context.Background(), "mod.tf"); !errors.Is(err, errDomainNotFound) {
		t.Fatalf("expected errDomainNotFound before registration, got %v", err)
	}

	// Domain gets registered in Pangolin, and the cooldown elapses.
	lister.setDomains([]pangolin.Domain{
		{ID: "id-tunnel", BaseDomain: "tunnel.tf"},
		{ID: "id-mod", BaseDomain: "mod.tf"},
	})
	clock = clock.Add(2 * time.Minute)

	sub, domainID, err := r.resolveHostDomain(context.Background(), "mod.tf")
	if err != nil {
		t.Fatalf("resolve after refresh: %v", err)
	}
	if domainID != "id-mod" {
		t.Errorf("domainID = %q, want %q", domainID, "id-mod")
	}
	if sub != "" {
		t.Errorf("subdomain = %q, want empty", sub)
	}
}

func TestDomainCache_MissWithinIntervalDoesNotRefetch(t *testing.T) {
	lister := &fakeDomainLister{domains: []pangolin.Domain{{ID: "id-a", BaseDomain: "example.com"}}}
	clock := time.Now()
	c := newDomainCache(lister, time.Minute)
	c.now = fixedClock(&clock)

	if _, err := c.get(context.Background()); err != nil {
		t.Fatalf("get: %v", err)
	}
	before := lister.callCount()

	clock = clock.Add(30 * time.Second) // still inside the cooldown
	_, refreshed, err := c.refreshIfStale(context.Background())
	if err != nil {
		t.Fatalf("refreshIfStale: %v", err)
	}
	if refreshed {
		t.Error("refreshed = true, want false within the cooldown")
	}
	if got := lister.callCount(); got != before {
		t.Errorf("calls = %d, want %d (no refetch within cooldown)", got, before)
	}
}

func TestDomainCache_ManyMissesShareOneRefetch(t *testing.T) {
	lister := &fakeDomainLister{domains: []pangolin.Domain{{ID: "id-a", BaseDomain: "example.com"}}}
	clock := time.Now()
	c := newDomainCache(lister, time.Minute)
	c.now = fixedClock(&clock)
	r := &IngressReconciler{domains: c}

	if _, err := c.get(context.Background()); err != nil {
		t.Fatalf("get: %v", err)
	}
	clock = clock.Add(2 * time.Minute)

	for i := 0; i < 10; i++ {
		host := fmt.Sprintf("host%d.unregistered.test", i)
		if _, _, err := r.resolveHostDomain(context.Background(), host); !errors.Is(err, errDomainNotFound) {
			t.Fatalf("host %s: expected errDomainNotFound, got %v", host, err)
		}
	}

	// One cold fetch plus at most one refetch for all ten misses.
	if got := lister.callCount(); got != 2 {
		t.Errorf("calls = %d, want 2 (one cold fetch + one shared refetch)", got)
	}
}

func TestDomainCache_FailedRefreshRetainsEntries(t *testing.T) {
	lister := &fakeDomainLister{domains: []pangolin.Domain{{ID: "id-a", BaseDomain: "example.com"}}}
	clock := time.Now()
	c := newDomainCache(lister, time.Minute)
	c.now = fixedClock(&clock)
	r := &IngressReconciler{domains: c}

	if _, err := c.get(context.Background()); err != nil {
		t.Fatalf("get: %v", err)
	}

	lister.setErr(errors.New("pangolin unavailable"))
	clock = clock.Add(2 * time.Minute)

	if _, _, err := r.resolveHostDomain(context.Background(), "nope.test"); !errors.Is(err, errDomainNotFound) {
		t.Fatalf("expected errDomainNotFound, got %v", err)
	}

	// A cached host must still resolve after the failed refresh.
	sub, domainID, err := r.resolveHostDomain(context.Background(), "app.example.com")
	if err != nil {
		t.Fatalf("cached host failed to resolve after failed refresh: %v", err)
	}
	if domainID != "id-a" || sub != "app" {
		t.Errorf("got (%q, %q), want (%q, %q)", sub, domainID, "app", "id-a")
	}
}

func TestDomainCache_FailedRefreshConsumesRateLimit(t *testing.T) {
	lister := &fakeDomainLister{
		domains: []pangolin.Domain{{ID: "id-a", BaseDomain: "example.com"}},
	}
	clock := time.Now()
	c := newDomainCache(lister, time.Minute)
	c.now = fixedClock(&clock)

	if _, err := c.get(context.Background()); err != nil {
		t.Fatalf("get: %v", err)
	}

	lister.setErr(errors.New("pangolin unavailable"))
	clock = clock.Add(2 * time.Minute)

	if _, _, err := c.refreshIfStale(context.Background()); err == nil {
		t.Fatal("expected refresh error")
	}
	after := lister.callCount()

	// Immediately retrying must not hit the API again: the failed attempt
	// consumed the cooldown, which is what prevents a Pangolin outage from
	// turning into a request storm.
	if _, refreshed, _ := c.refreshIfStale(context.Background()); refreshed {
		t.Error("refreshed = true, want false immediately after a failed attempt")
	}
	if got := lister.callCount(); got != after {
		t.Errorf("calls = %d, want %d (failed attempt must consume the cooldown)", got, after)
	}
}

func TestDomainCache_ZeroIntervalDisablesRefresh(t *testing.T) {
	lister := &fakeDomainLister{domains: []pangolin.Domain{{ID: "id-a", BaseDomain: "example.com"}}}
	c := newDomainCache(lister, 0)

	if _, err := c.get(context.Background()); err != nil {
		t.Fatalf("get: %v", err)
	}
	before := lister.callCount()

	_, refreshed, err := c.refreshIfStale(context.Background())
	if err != nil {
		t.Fatalf("refreshIfStale: %v", err)
	}
	if refreshed {
		t.Error("refreshed = true, want false when refresh is disabled")
	}
	if got := lister.callCount(); got != before {
		t.Errorf("calls = %d, want %d", got, before)
	}
}

func TestDomainCache_ConcurrentMissesCollapseToOneFetch(t *testing.T) {
	lister := &fakeDomainLister{
		domains: []pangolin.Domain{{ID: "id-a", BaseDomain: "example.com"}},
		block:   make(chan struct{}),
	}
	c := newDomainCache(lister, time.Minute)

	const goroutines = 16
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			_, _ = c.get(context.Background())
		}()
	}

	close(start)
	// Let the goroutines pile up on the in-flight fetch before releasing it.
	time.Sleep(50 * time.Millisecond)
	close(lister.block)
	wg.Wait()

	if got := lister.callCount(); got != 1 {
		t.Errorf("calls = %d, want 1 (concurrent misses must collapse)", got)
	}
}

func TestDomainCache_StoresSortedLongestFirst(t *testing.T) {
	lister := &fakeDomainLister{domains: []pangolin.Domain{
		{ID: "id-co-uk", BaseDomain: "co.uk"},
		{ID: "id-example-co-uk", BaseDomain: "example.co.uk"},
		{ID: "id-a", BaseDomain: "a.io"},
	}}
	c := newDomainCache(lister, time.Minute)

	entries, err := c.get(context.Background())
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	for i := 1; i < len(entries); i++ {
		if len(entries[i-1].BaseDomain) < len(entries[i].BaseDomain) {
			t.Fatalf("entries not sorted longest-first: %v", baseDomains(entries))
		}
	}

	// The sort is what makes longest-suffix matching correct.
	_, domainID, ok := matchHost("app.example.co.uk", entries)
	if !ok || domainID != "id-example-co-uk" {
		t.Errorf("matchHost = (%q, %v), want (%q, true)", domainID, ok, "id-example-co-uk")
	}
}

func TestDomainCache_ColdFetchErrorIsReported(t *testing.T) {
	lister := &fakeDomainLister{err: errors.New("boom")}
	c := newDomainCache(lister, time.Minute)
	r := &IngressReconciler{domains: c}

	_, _, err := r.resolveHostDomain(context.Background(), "app.example.com")
	if err == nil {
		t.Fatal("expected error on cold fetch failure")
	}
	if errors.Is(err, errDomainNotFound) {
		t.Error("cold fetch failure must not be reported as a domain-not-found condition")
	}
}

func TestDescribe_ReportsFreshnessWithoutListingDomains(t *testing.T) {
	domains := []pangolin.Domain{
		{ID: "id-a", BaseDomain: "example.com"},
		{ID: "id-b", BaseDomain: "tunnel.tf"},
	}
	c := warmDomainCache(domains, time.Minute)
	r := &IngressReconciler{domains: c}

	count, lastRefresh := c.describe()
	if count != 2 {
		t.Errorf("count = %d, want 2", count)
	}
	if lastRefresh == "" {
		t.Error("lastRefresh must be populated")
	}

	_, _, err := r.resolveHostDomain(context.Background(), "unknown.test")
	if err == nil {
		t.Fatal("expected resolution error")
	}
	msg := err.Error()
	if !strings.Contains(msg, "unknown.test") || !strings.Contains(msg, "2 Pangolin domains known") {
		t.Errorf("message missing host or domain count: %q", msg)
	}
	// Events carry this text; it must not enumerate the org's domains.
	if strings.Contains(msg, "example.com") || strings.Contains(msg, "tunnel.tf") {
		t.Errorf("message must not list known domains: %q", msg)
	}
}

func TestDomainRequeueAfter(t *testing.T) {
	tests := []struct {
		name     string
		interval time.Duration
		wantMin  time.Duration
	}{
		{name: "default when unset", interval: 0, wantMin: defaultDomainCacheRefreshInterval},
		{name: "configured interval", interval: 5 * time.Minute, wantMin: 5 * time.Minute},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := &IngressReconciler{DomainCacheRefreshInterval: tt.interval}
			got := r.domainRequeueAfter()
			// Must exceed the cooldown, or a requeue could land just before it
			// expires and skip the refetch it came back to perform.
			if got <= tt.wantMin {
				t.Errorf("domainRequeueAfter() = %v, want > %v", got, tt.wantMin)
			}
		})
	}
}

// TestReconcile_UnresolvableHostRequeuesWithoutError exercises the sentinel
// through the full Reconcile path. A %v instead of %w anywhere in the wrapping
// chain would silently restore hard-error semantics, so this guards the
// requeue behaviour end to end rather than just the error value.
func TestReconcile_UnresolvableHostRequeuesWithoutError(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(scheme); err != nil {
		t.Fatalf("add scheme: %v", err)
	}

	ingressClassName := "pangolin"
	pathType := networkingv1.PathTypePrefix
	ingress := &networkingv1.Ingress{
		ObjectMeta: metav1.ObjectMeta{
			Name:       "coder",
			Namespace:  "coder",
			Finalizers: []string{pangolinFinalizerName},
		},
		Spec: networkingv1.IngressSpec{
			IngressClassName: &ingressClassName,
			Rules: []networkingv1.IngressRule{{
				Host: "unregistered.test",
				IngressRuleValue: networkingv1.IngressRuleValue{
					HTTP: &networkingv1.HTTPIngressRuleValue{
						Paths: []networkingv1.HTTPIngressPath{{
							Path:     "/",
							PathType: &pathType,
							Backend: networkingv1.IngressBackend{
								Service: &networkingv1.IngressServiceBackend{
									Name: "coder",
									Port: networkingv1.ServiceBackendPort{Number: 80},
								},
							},
						}},
					},
				},
			}},
		},
	}
	service := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Name: "coder", Namespace: "coder"},
		Spec:       corev1.ServiceSpec{Ports: []corev1.ServicePort{{Port: 80}}},
	}

	k8s := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(ingress, service).
		WithStatusSubresource(ingress).
		Build()

	recorder := record.NewFakeRecorder(10)
	r := &IngressReconciler{
		Client:                     k8s,
		Scheme:                     scheme,
		IngressClass:               "pangolin",
		ResourcePrefix:             "test",
		Recorder:                   recorder,
		DomainCacheRefreshInterval: time.Minute,
		// Non-nil so Reconcile skips Secret-based client init; resolution fails
		// before any HTTP call is attempted.
		PangolinClient: pangolin.NewClient("http://127.0.0.1:1", "key", "org"),
		domains:        warmDomainCache([]pangolin.Domain{{ID: "id-a", BaseDomain: "example.com"}}, time.Minute),
	}

	result, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "coder", Namespace: "coder"},
	})
	if err != nil {
		t.Fatalf("Reconcile returned an error; unresolvable hosts must requeue instead: %v", err)
	}
	if result.RequeueAfter <= 0 {
		t.Fatalf("RequeueAfter = %v, want a bounded positive delay", result.RequeueAfter)
	}

	select {
	case ev := <-recorder.Events:
		if !strings.Contains(ev, reasonDomainNotFound) || !strings.Contains(ev, "unregistered.test") {
			t.Errorf("event = %q, want Warning %s naming the host", ev, reasonDomainNotFound)
		}
	default:
		t.Error("expected a Warning event on the Ingress")
	}
}
