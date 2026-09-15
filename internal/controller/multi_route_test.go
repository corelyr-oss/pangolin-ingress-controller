package controller

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/vinzenz/pangolin-ingress-controller/internal/pangolin"
)

// statefulPangolin is an in-memory Pangolin that keeps what earlier reconciles
// created. The multi-host and multi-path bugs only show across reconciles --
// each pass undid the previous one -- so a fake that forgets between calls
// cannot catch them.
type statefulPangolin struct {
	t *testing.T

	mu             sync.Mutex
	nextID         int
	resources      map[int]*pangolin.Resource
	targets        map[int]*fakeTarget
	createdTargets int
	deletedTargets int
}

type fakeTarget struct {
	resourceID int
	pangolin.Target
}

var (
	fakeReOrgResource     = regexp.MustCompile(`^/v1/org/[^/]+/resource$`)
	fakeReOrgResources    = regexp.MustCompile(`^/v1/org/[^/]+/resources$`)
	fakeReResource        = regexp.MustCompile(`^/v1/resource/(\d+)$`)
	fakeReResourceTargets = regexp.MustCompile(`^/v1/resource/(\d+)/targets$`)
	fakeReResourceTarget  = regexp.MustCompile(`^/v1/resource/(\d+)/target$`)
	fakeReTarget          = regexp.MustCompile(`^/v1/target/(\d+)$`)
)

func fakeFullDomain(subdomain string) string {
	if subdomain == "" {
		return "example.com"
	}
	return subdomain + ".example.com"
}

func fakePathID(re *regexp.Regexp, path string) int {
	id, _ := strconv.Atoi(re.FindStringSubmatch(path)[1])
	return id
}

func writeFakeData(w http.ResponseWriter, v any) {
	_ = json.NewEncoder(w).Encode(map[string]any{"data": v})
}

func writeFakeNotFound(w http.ResponseWriter) {
	w.WriteHeader(http.StatusNotFound)
	_, _ = w.Write([]byte(`{"message":"not found"}`))
}

func targetFromRequest(id int, req *pangolin.CreateTargetRequest) pangolin.Target {
	return pangolin.Target{
		ID:            id,
		SiteID:        req.SiteID,
		IP:            req.IP,
		Method:        req.Method,
		Port:          req.Port,
		Enabled:       req.Enabled,
		Path:          req.Path,
		PathMatchType: req.PathMatchType,
	}
}

func (p *statefulPangolin) decode(r *http.Request, v any) {
	if err := json.NewDecoder(r.Body).Decode(v); err != nil {
		p.t.Errorf("decoding %s %s: %v", r.Method, r.URL.Path, err)
	}
}

func (p *statefulPangolin) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	p.mu.Lock()
	defer p.mu.Unlock()

	w.Header().Set("Content-Type", "application/json")
	path := r.URL.Path

	switch {
	case strings.HasSuffix(path, "/domains"):
		writeFakeData(w, map[string]any{"domains": []pangolin.Domain{{ID: "dom-1", BaseDomain: "example.com"}}})

	case strings.Contains(path, "/site/"):
		writeFakeData(w, pangolin.Site{ID: 1, NiceID: "test-site", Name: "test", ProxyIP: "203.0.113.10"})

	case fakeReOrgResource.MatchString(path) && r.Method == http.MethodPut:
		var req pangolin.CreateResourceRequest
		p.decode(r, &req)
		for _, res := range p.resources {
			if res.Subdomain == req.Subdomain && res.DomainID == req.DomainID {
				w.WriteHeader(http.StatusConflict)
				_, _ = w.Write([]byte(`{"message":"resource already exists"}`))
				return
			}
		}
		p.nextID++
		res := &pangolin.Resource{
			ID:         p.nextID,
			Name:       req.Name,
			Subdomain:  req.Subdomain,
			DomainID:   req.DomainID,
			FullDomain: fakeFullDomain(req.Subdomain),
			HTTP:       true,
		}
		p.resources[res.ID] = res
		writeFakeData(w, res)

	case fakeReOrgResources.MatchString(path) && r.Method == http.MethodGet:
		list := []pangolin.Resource{}
		for _, res := range p.resources {
			list = append(list, *res)
		}
		writeFakeData(w, map[string]any{"resources": list})

	case fakeReResource.MatchString(path):
		id := fakePathID(fakeReResource, path)
		res, ok := p.resources[id]
		if !ok {
			writeFakeNotFound(w)
			return
		}
		switch r.Method {
		case http.MethodGet:
			writeFakeData(w, res)
		case http.MethodPost:
			var req pangolin.UpdateResourceRequest
			p.decode(r, &req)
			if req.Name != "" {
				res.Name = req.Name
			}
			if req.Subdomain != "" {
				res.Subdomain = req.Subdomain
				res.FullDomain = fakeFullDomain(req.Subdomain)
			}
			writeFakeData(w, res)
		case http.MethodDelete:
			delete(p.resources, id)
			for tid, t := range p.targets {
				if t.resourceID == id {
					delete(p.targets, tid)
				}
			}
			writeFakeData(w, nil)
		default:
			p.t.Errorf("unexpected Pangolin call: %s %s", r.Method, path)
		}

	case fakeReResourceTargets.MatchString(path) && r.Method == http.MethodGet:
		id := fakePathID(fakeReResourceTargets, path)
		list := []pangolin.Target{}
		for _, t := range p.targets {
			if t.resourceID == id {
				list = append(list, t.Target)
			}
		}
		slices.SortFunc(list, func(a, b pangolin.Target) int { return a.ID - b.ID })
		writeFakeData(w, map[string]any{"targets": list})

	case fakeReResourceTarget.MatchString(path) && r.Method == http.MethodPut:
		id := fakePathID(fakeReResourceTarget, path)
		if _, ok := p.resources[id]; !ok {
			writeFakeNotFound(w)
			return
		}
		var req pangolin.CreateTargetRequest
		p.decode(r, &req)
		p.nextID++
		t := &fakeTarget{resourceID: id, Target: targetFromRequest(p.nextID, &req)}
		p.targets[t.ID] = t
		p.createdTargets++
		writeFakeData(w, t.Target)

	case fakeReTarget.MatchString(path):
		id := fakePathID(fakeReTarget, path)
		t, ok := p.targets[id]
		if !ok {
			writeFakeNotFound(w)
			return
		}
		switch r.Method {
		case http.MethodPost:
			var req pangolin.CreateTargetRequest
			p.decode(r, &req)
			t.Target = targetFromRequest(id, &req)
			writeFakeData(w, t.Target)
		case http.MethodDelete:
			delete(p.targets, id)
			p.deletedTargets++
			writeFakeData(w, nil)
		default:
			p.t.Errorf("unexpected Pangolin call: %s %s", r.Method, path)
		}

	default:
		p.t.Errorf("unexpected Pangolin call: %s %s", r.Method, path)
		w.WriteHeader(http.StatusNotImplemented)
		_, _ = w.Write([]byte(`{"data":null}`))
	}
}

// routes renders what Pangolin would serve: for each resource's domain, its
// targets as "matchType path -> ip:port", sorted.
func (p *statefulPangolin) routes() map[string][]string {
	p.mu.Lock()
	defer p.mu.Unlock()

	out := map[string][]string{}
	for _, res := range p.resources {
		out[res.FullDomain] = []string{}
	}
	for _, t := range p.targets {
		domain := p.resources[t.resourceID].FullDomain
		out[domain] = append(out[domain], fmt.Sprintf("%s %s -> %s:%d", t.PathMatchType, t.Path, t.IP, t.Port))
	}
	for domain := range out {
		slices.Sort(out[domain])
	}
	return out
}

func newMultiRouteReconciler(t *testing.T, objs ...client.Object) (*IngressReconciler, client.Client, *statefulPangolin) {
	t.Helper()

	scheme := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	c := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(objs...).
		WithStatusSubresource(&networkingv1.Ingress{}).
		Build()

	p := &statefulPangolin{t: t, resources: map[int]*pangolin.Resource{}, targets: map[int]*fakeTarget{}}
	srv := httptest.NewServer(p)
	t.Cleanup(srv.Close)

	r := &IngressReconciler{
		Client:         c,
		Scheme:         scheme,
		IngressClass:   "pangolin",
		ResourcePrefix: "pangolin-controller",
		OrgID:          "test-org",
		SiteNiceID:     "test-site",
	}
	r.setPangolinClient(pangolin.NewClient(srv.URL, "test-api-key", "test-org"))
	return r, c, p
}

func reconcileTimes(t *testing.T, r *IngressReconciler, key client.ObjectKey, n int) {
	t.Helper()
	for i := 1; i <= n; i++ {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		_, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: key})
		cancel()
		if err != nil {
			t.Fatalf("reconcile %d: %v", i, err)
		}
	}
}

func mhPath(path, service string, port int32) networkingv1.HTTPIngressPath {
	pathType := networkingv1.PathTypePrefix
	return networkingv1.HTTPIngressPath{
		Path:     path,
		PathType: &pathType,
		Backend: networkingv1.IngressBackend{
			Service: &networkingv1.IngressServiceBackend{
				Name: service,
				Port: networkingv1.ServiceBackendPort{Number: port},
			},
		},
	}
}

func mhRule(host string, paths ...networkingv1.HTTPIngressPath) networkingv1.IngressRule {
	return networkingv1.IngressRule{
		Host: host,
		IngressRuleValue: networkingv1.IngressRuleValue{
			HTTP: &networkingv1.HTTPIngressRuleValue{Paths: paths},
		},
	}
}

func mhIngress(annotations map[string]string, rules ...networkingv1.IngressRule) *networkingv1.Ingress {
	className := "pangolin"
	return &networkingv1.Ingress{
		ObjectMeta: metav1.ObjectMeta{Name: "multi", Namespace: "default", Annotations: annotations},
		Spec:       networkingv1.IngressSpec{IngressClassName: &className, Rules: rules},
	}
}

func mhService(name string, port int32) *corev1.Service {
	return &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "default"},
		Spec:       corev1.ServiceSpec{Ports: []corev1.ServicePort{{Port: port}}},
	}
}

func assertRoutes(t *testing.T, p *statefulPangolin, want map[string][]string) {
	t.Helper()
	for domain := range want {
		slices.Sort(want[domain])
	}
	if got := p.routes(); !reflect.DeepEqual(got, want) {
		t.Errorf("Pangolin routes =\n  %v\nwant\n  %v", got, want)
	}
}

func getIngress(t *testing.T, c client.Client, key client.ObjectKey) *networkingv1.Ingress {
	t.Helper()
	ing := &networkingv1.Ingress{}
	if err := c.Get(context.Background(), key, ing); err != nil {
		t.Fatalf("reading back the Ingress: %v", err)
	}
	return ing
}

// The Bifrost shape: two paths on one host, both to the same Service. Before
// the fix each path's reconcile matched the other's target on IP and port,
// rewrote its path, and deleted the rest -- one path routed at a time.
func TestReconcile_OneHostTwoPaths_KeepsATargetPerPath(t *testing.T) {
	ing := mhIngress(nil, mhRule("api.example.com",
		mhPath("/anthropic", "bifrost", 8080),
		mhPath("/openai", "bifrost", 8080),
	))
	r, _, p := newMultiRouteReconciler(t, ing, mhService("bifrost", 8080))

	reconcileTimes(t, r, client.ObjectKeyFromObject(ing), 3)

	assertRoutes(t, p, map[string][]string{"api.example.com": {
		"prefix /anthropic -> bifrost.default.svc.cluster.local:8080",
		"prefix /openai -> bifrost.default.svc.cluster.local:8080",
	}})
	if p.createdTargets != 2 || p.deletedTargets != 0 {
		t.Errorf("targets created=%d deleted=%d, want 2 and 0: repeated reconciles must not churn targets",
			p.createdTargets, p.deletedTargets)
	}
}

func TestReconcile_PathRemoved_DeletesOnlyItsTarget(t *testing.T) {
	ing := mhIngress(nil, mhRule("api.example.com",
		mhPath("/anthropic", "bifrost", 8080),
		mhPath("/openai", "openai-proxy", 9090),
	))
	r, c, p := newMultiRouteReconciler(t, ing, mhService("bifrost", 8080), mhService("openai-proxy", 9090))
	key := client.ObjectKeyFromObject(ing)
	reconcileTimes(t, r, key, 1)

	got := getIngress(t, c, key)
	got.Spec.Rules[0].HTTP.Paths = got.Spec.Rules[0].HTTP.Paths[:1]
	if err := c.Update(context.Background(), got); err != nil {
		t.Fatal(err)
	}
	reconcileTimes(t, r, key, 2)

	assertRoutes(t, p, map[string][]string{"api.example.com": {
		"prefix /anthropic -> bifrost.default.svc.cluster.local:8080",
	}})
	if p.deletedTargets != 1 {
		t.Errorf("deleted %d targets, want exactly the removed path's", p.deletedTargets)
	}
}

// The Coder shape: two hosts on one Ingress. Before the fix the second host
// found the first host's resource-id and renamed that resource to itself.
func TestReconcile_TwoHosts_OneResourceEach(t *testing.T) {
	ing := mhIngress(nil,
		mhRule("coder.example.com", mhPath("/", "coder", 80)),
		mhRule("apps.example.com", mhPath("/", "coder", 80)),
	)
	r, c, p := newMultiRouteReconciler(t, ing, mhService("coder", 80))
	key := client.ObjectKeyFromObject(ing)

	reconcileTimes(t, r, key, 3)

	assertRoutes(t, p, map[string][]string{
		"coder.example.com": {"prefix / -> coder.default.svc.cluster.local:80"},
		"apps.example.com":  {"prefix / -> coder.default.svc.cluster.local:80"},
	})

	got := getIngress(t, c, key)
	ids := readResourceIDs(got)
	if len(ids) != 2 || ids["coder.example.com"] == ids["apps.example.com"] {
		t.Fatalf("resource-ids = %v, want a distinct resource per host", ids)
	}
	if got.Annotations[annotationResourceID] != ids["coder.example.com"] {
		t.Errorf("resource-id = %q, want the first host's id %q", got.Annotations[annotationResourceID], ids["coder.example.com"])
	}
	for id, res := range p.resources {
		if want := "pangolin-controller-" + res.FullDomain; res.Name != want {
			t.Errorf("resource %d is named %q, want %q", id, res.Name, want)
		}
	}
}

func TestReconcile_HostRemoved_DeletesItsResource(t *testing.T) {
	ing := mhIngress(nil,
		mhRule("coder.example.com", mhPath("/", "coder", 80)),
		mhRule("apps.example.com", mhPath("/", "coder", 80)),
	)
	r, c, p := newMultiRouteReconciler(t, ing, mhService("coder", 80))
	key := client.ObjectKeyFromObject(ing)
	reconcileTimes(t, r, key, 1)

	got := getIngress(t, c, key)
	got.Spec.Rules = got.Spec.Rules[:1]
	if err := c.Update(context.Background(), got); err != nil {
		t.Fatal(err)
	}
	reconcileTimes(t, r, key, 2)

	assertRoutes(t, p, map[string][]string{
		"coder.example.com": {"prefix / -> coder.default.svc.cluster.local:80"},
	})
	if ids := readResourceIDs(getIngress(t, c, key)); len(ids) != 1 || ids["coder.example.com"] == "" {
		t.Errorf("resource-ids = %v, want only coder.example.com", ids)
	}
}

// An Ingress programmed by the previous controller carries only resource-id,
// and its target has no path recorded. Upgrading must adopt both in place: a
// new resource or a delete-and-recreate of the target is an outage window on
// every live Ingress the moment the new image starts.
func TestReconcile_LegacyResourceID_AdoptedWithoutChurn(t *testing.T) {
	ing := mhIngress(map[string]string{annotationResourceID: "7"},
		mhRule("app.example.com", mhPath("/", "app", 80)))
	r, c, p := newMultiRouteReconciler(t, ing, mhService("app", 80))
	p.resources[7] = &pangolin.Resource{ID: 7, Name: "pangolin-controller-app.example.com",
		Subdomain: "app", DomainID: "dom-1", FullDomain: "app.example.com", HTTP: true}
	p.targets[8] = &fakeTarget{resourceID: 7, Target: pangolin.Target{ID: 8, SiteID: 1,
		IP: "app.default.svc.cluster.local", Method: "http", Port: 80, Enabled: true}}
	p.nextID = 8
	key := client.ObjectKeyFromObject(ing)

	reconcileTimes(t, r, key, 2)

	if len(p.resources) != 1 {
		t.Errorf("Pangolin holds %d resources, want the adopted one only", len(p.resources))
	}
	if p.createdTargets != 0 || p.deletedTargets != 0 {
		t.Errorf("targets created=%d deleted=%d, want the existing target updated in place",
			p.createdTargets, p.deletedTargets)
	}
	assertRoutes(t, p, map[string][]string{"app.example.com": {"prefix / -> app.default.svc.cluster.local:80"}})
	if ids := readResourceIDs(getIngress(t, c, key)); ids["app.example.com"] != "7" {
		t.Errorf("resource-ids = %v, want app.example.com adopted as 7", ids)
	}
}

// A multi-host Ingress the old controller got wrong: resource-id points at a
// resource the second host renamed to itself. Each host must end up with its
// own resource, the existing one staying with the host it now serves.
func TestReconcile_LegacyClobberedMultiHost_Converges(t *testing.T) {
	ing := mhIngress(map[string]string{annotationResourceID: "7"},
		mhRule("coder.example.com", mhPath("/", "coder", 80)),
		mhRule("apps.example.com", mhPath("/", "coder", 80)),
	)
	r, c, p := newMultiRouteReconciler(t, ing, mhService("coder", 80))
	p.resources[7] = &pangolin.Resource{ID: 7, Name: "pangolin-controller-apps.example.com",
		Subdomain: "apps", DomainID: "dom-1", FullDomain: "apps.example.com", HTTP: true}
	p.nextID = 7
	key := client.ObjectKeyFromObject(ing)

	reconcileTimes(t, r, key, 3)

	assertRoutes(t, p, map[string][]string{
		"coder.example.com": {"prefix / -> coder.default.svc.cluster.local:80"},
		"apps.example.com":  {"prefix / -> coder.default.svc.cluster.local:80"},
	})
	if ids := readResourceIDs(getIngress(t, c, key)); ids["apps.example.com"] != "7" {
		t.Errorf("resource-ids = %v, want apps.example.com to keep resource 7", ids)
	}
}

func TestReconcile_Deletion_DeletesEveryHostsResource(t *testing.T) {
	ing := mhIngress(nil,
		mhRule("coder.example.com", mhPath("/", "coder", 80)),
		mhRule("apps.example.com", mhPath("/", "coder", 80)),
	)
	r, c, p := newMultiRouteReconciler(t, ing, mhService("coder", 80))
	key := client.ObjectKeyFromObject(ing)
	reconcileTimes(t, r, key, 1)

	if err := c.Delete(context.Background(), getIngress(t, c, key)); err != nil {
		t.Fatal(err)
	}
	reconcileTimes(t, r, key, 1)

	if len(p.resources) != 0 {
		t.Errorf("Pangolin still holds %d resources after the Ingress was deleted", len(p.resources))
	}
	if err := c.Get(context.Background(), key, &networkingv1.Ingress{}); !apierrors.IsNotFound(err) {
		t.Errorf("Ingress still present after its finalizer ran: %v", err)
	}
}

// A resource someone already deleted in Pangolin must not wedge the finalizer.
func TestReconcile_Deletion_ToleratesMissingResource(t *testing.T) {
	ing := mhIngress(nil, mhRule("app.example.com", mhPath("/", "app", 80)))
	r, c, p := newMultiRouteReconciler(t, ing, mhService("app", 80))
	key := client.ObjectKeyFromObject(ing)
	reconcileTimes(t, r, key, 1)

	p.mu.Lock()
	clear(p.resources)
	p.mu.Unlock()

	if err := c.Delete(context.Background(), getIngress(t, c, key)); err != nil {
		t.Fatal(err)
	}
	reconcileTimes(t, r, key, 1)

	if err := c.Get(context.Background(), key, &networkingv1.Ingress{}); !apierrors.IsNotFound(err) {
		t.Errorf("Ingress still present: %v", err)
	}
}
