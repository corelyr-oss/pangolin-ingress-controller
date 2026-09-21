package controller

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/vinzenz/pangolin-ingress-controller/api/v1alpha1"
	"github.com/vinzenz/pangolin-ingress-controller/internal/pangolin"
)

// ---------------------------------------------------------------------------
// A stateful fake of Pangolin's public-resource API, reproducing what the live
// test instance was observed to do: raw create takes no niceId and assigns a
// random one, update enforces niceId uniqueness with a 409, a second resource
// on a taken proxy port is accepted, the listing paginates by page/pageSize and
// rejects limit/offset, and there is no niceId point route.

type fakePublicPangolin struct {
	mu sync.Mutex

	nextID    int
	resources map[int]*pangolin.Resource
	targets   map[int]*pangolin.Target
	targetOf  map[int]int // target ID -> resource ID

	// sites maps a site nice ID to its numeric ID.
	sites map[string]int

	// listFails makes the listing fail.
	listFails bool
	// updateFails makes every update fail, standing in for the naming update
	// failing after a successful create.
	updateFails bool
	// pageSize caps the listing's page size below what the client asks for.
	pageSize int

	creates, updates, deletes   int
	targetCreates, targetDelete int
}

func newFakePublicPangolin() *fakePublicPangolin {
	return &fakePublicPangolin{
		nextID:    200,
		resources: map[int]*pangolin.Resource{},
		targets:   map[int]*pangolin.Target{},
		targetOf:  map[int]int{},
		sites:     map[string]int{"test-site": 1, "second-site": 2},
	}
}

func (f *fakePublicPangolin) seed(res pangolin.Resource) *pangolin.Resource {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.nextID++
	res.ID = f.nextID
	f.resources[res.ID] = &res
	return &res
}

func (f *fakePublicPangolin) seedTarget(resourceID int, t pangolin.Target) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.nextID++
	t.ID = f.nextID
	f.targets[t.ID] = &t
	f.targetOf[t.ID] = resourceID
}

func (f *fakePublicPangolin) writes() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.creates + f.updates + f.deletes + f.targetCreates + f.targetDelete
}

func (f *fakePublicPangolin) resourceByNiceID(niceID string) *pangolin.Resource {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, r := range f.resources {
		if r.NiceID == niceID {
			c := *r
			return &c
		}
	}
	return nil
}

func (f *fakePublicPangolin) targetsFor(resourceID int) []pangolin.Target {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []pangolin.Target
	for id, t := range f.targets {
		if f.targetOf[id] == resourceID {
			out = append(out, *t)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].SiteID < out[j].SiteID })
	return out
}

func writeStatus(w http.ResponseWriter, status int, data interface{}, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]interface{}{
		"data": data, "success": status < 300, "error": status >= 300, "message": message, "status": status,
	})
}

func (f *fakePublicPangolin) handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()

		parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/v1/"), "/")
		body, _ := io.ReadAll(r.Body)

		switch {
		// GET /org/{org}/site/{niceID}
		case len(parts) == 4 && parts[0] == "org" && parts[2] == "site" && r.Method == http.MethodGet:
			id, ok := f.sites[parts[3]]
			if !ok {
				writeStatus(w, http.StatusNotFound, nil, "site not found")
				return
			}
			writeData(w, map[string]interface{}{"siteId": id, "niceId": parts[3]})

		// PUT /org/{org}/resource -- raw create
		case len(parts) == 3 && parts[0] == "org" && parts[2] == "resource" && r.Method == http.MethodPut:
			var req pangolin.CreateRawResourceRequest
			_ = json.Unmarshal(body, &req)
			f.creates++
			f.nextID++
			res := &pangolin.Resource{
				ID: f.nextID, Name: req.Name, Mode: req.Mode, ProxyPort: req.ProxyPort,
				NiceID: fmt.Sprintf("random-nice-%d", f.nextID), Enabled: true,
			}
			f.resources[res.ID] = res
			writeStatus(w, http.StatusCreated, res, "Non-http resource created successfully")

		// GET /org/{org}/resources?page=&pageSize=
		case len(parts) == 3 && parts[0] == "org" && parts[2] == "resources" && r.Method == http.MethodGet:
			q := r.URL.Query()
			if q.Has("limit") || q.Has("offset") {
				writeStatus(w, http.StatusBadRequest, nil, `Validation error: Unrecognized keys: "limit", "offset"`)
				return
			}
			if f.listFails {
				writeStatus(w, http.StatusInternalServerError, nil, "upstream exploded")
				return
			}
			ids := make([]int, 0, len(f.resources))
			for id := range f.resources {
				ids = append(ids, id)
			}
			sort.Ints(ids)

			page, _ := strconv.Atoi(q.Get("page"))
			if page < 1 {
				page = 1
			}
			size, _ := strconv.Atoi(q.Get("pageSize"))
			if size <= 0 {
				size = 20
			}
			if f.pageSize > 0 && f.pageSize < size {
				size = f.pageSize
			}
			start := (page - 1) * size
			if start > len(ids) {
				start = len(ids)
			}
			end := start + size
			if end > len(ids) {
				end = len(ids)
			}
			items := make([]*pangolin.Resource, 0, end-start)
			for _, id := range ids[start:end] {
				items = append(items, f.resources[id])
			}
			writeData(w, map[string]interface{}{
				"resources":  items,
				"pagination": map[string]int{"total": len(ids), "pageSize": size, "page": page},
			})

		// /resource/{id}
		case len(parts) == 2 && parts[0] == "resource":
			id, _ := strconv.Atoi(parts[1])
			res, ok := f.resources[id]
			if !ok {
				writeStatus(w, http.StatusNotFound, nil, fmt.Sprintf("Resource with ID %d not found", id))
				return
			}
			switch r.Method {
			case http.MethodGet:
				writeData(w, res)
			case http.MethodPost:
				if f.updateFails {
					writeStatus(w, http.StatusInternalServerError, nil, "update exploded")
					return
				}
				var req pangolin.UpdateRawResourceRequest
				_ = json.Unmarshal(body, &req)
				if req.NiceID != "" {
					for otherID, other := range f.resources {
						if otherID != id && other.NiceID == req.NiceID {
							writeStatus(w, http.StatusConflict, nil,
								fmt.Sprintf("A resource with niceId %q already exists", req.NiceID))
							return
						}
					}
					res.NiceID = req.NiceID
				}
				if req.Name != "" {
					res.Name = req.Name
				}
				if req.ProxyPort != nil {
					res.ProxyPort = *req.ProxyPort
				}
				if req.Enabled != nil {
					res.Enabled = *req.Enabled
				}
				f.updates++
				writeData(w, res)
			case http.MethodDelete:
				delete(f.resources, id)
				for tid, rid := range f.targetOf {
					if rid == id {
						delete(f.targets, tid)
						delete(f.targetOf, tid)
					}
				}
				f.deletes++
				writeData(w, nil)
			}

		// PUT /resource/{id}/target
		case len(parts) == 3 && parts[0] == "resource" && parts[2] == "target" && r.Method == http.MethodPut:
			id, _ := strconv.Atoi(parts[1])
			var req pangolin.CreateTargetRequest
			_ = json.Unmarshal(body, &req)
			f.nextID++
			t := &pangolin.Target{ID: f.nextID, SiteID: req.SiteID, IP: req.IP, Port: req.Port, Enabled: req.Enabled}
			f.targets[t.ID] = t
			f.targetOf[t.ID] = id
			f.targetCreates++
			writeStatus(w, http.StatusCreated, t, "")

		// GET /resource/{id}/targets
		case len(parts) == 3 && parts[0] == "resource" && parts[2] == "targets" && r.Method == http.MethodGet:
			id, _ := strconv.Atoi(parts[1])
			list := []*pangolin.Target{}
			for tid, rid := range f.targetOf {
				if rid == id {
					list = append(list, f.targets[tid])
				}
			}
			writeData(w, map[string]interface{}{"targets": list})

		// DELETE /target/{id}
		case len(parts) == 2 && parts[0] == "target" && r.Method == http.MethodDelete:
			id, _ := strconv.Atoi(parts[1])
			delete(f.targets, id)
			delete(f.targetOf, id)
			f.targetDelete++
			writeData(w, nil)

		default:
			http.Error(w, "Cannot "+r.Method+" "+r.URL.Path, http.StatusNotFound)
		}
	})
}

// ---------------------------------------------------------------------------
// Test environment

type publicEnv struct {
	t          *testing.T
	reconciler *PangolinEndpointReconciler
	k8s        client.Client
	pangolin   *fakePublicPangolin
}

const publicNiceID = "pangolin-controller-parley-grpc"

func newPublicEnv(t *testing.T, objs ...client.Object) *publicEnv {
	t.Helper()

	fp := newFakePublicPangolin()
	srv := httptest.NewServer(fp.handler())
	t.Cleanup(srv.Close)

	s := endpointScheme(t)
	k8s := fake.NewClientBuilder().
		WithScheme(s).
		WithObjects(objs...).
		WithStatusSubresource(&v1alpha1.PangolinEndpoint{}).
		Build()

	pc := pangolin.NewClient(srv.URL, "test-key", "test-org")
	r := &PangolinEndpointReconciler{
		Client:                   k8s,
		Scheme:                   s,
		ResourcePrefix:           "pangolin-controller",
		PangolinClient:           pc,
		OrgID:                    "test-org",
		SiteNiceID:               "test-site",
		NameCacheRefreshInterval: time.Minute,
		principals:               newPrincipalResolver(pc, time.Minute),
	}
	return &publicEnv{t: t, reconciler: r, k8s: k8s, pangolin: fp}
}

func (e *publicEnv) reconcile(ep *v1alpha1.PangolinEndpoint) (ctrl.Result, error) {
	e.t.Helper()
	return e.reconciler.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: ep.Name, Namespace: ep.Namespace},
	})
}

func (e *publicEnv) mustReconcile(ep *v1alpha1.PangolinEndpoint) {
	e.t.Helper()
	if _, err := e.reconcile(ep); err != nil {
		e.t.Fatal(err)
	}
}

func (e *publicEnv) get(ep *v1alpha1.PangolinEndpoint) *v1alpha1.PangolinEndpoint {
	e.t.Helper()
	out := &v1alpha1.PangolinEndpoint{}
	if err := e.k8s.Get(context.Background(), types.NamespacedName{Name: ep.Name, Namespace: ep.Namespace}, out); err != nil {
		e.t.Fatal(err)
	}
	return out
}

func (e *publicEnv) mutate(ep *v1alpha1.PangolinEndpoint, fn func(*v1alpha1.PangolinEndpoint)) {
	e.t.Helper()
	cur := e.get(ep)
	fn(cur)
	cur.Generation++
	if err := e.k8s.Update(context.Background(), cur); err != nil {
		e.t.Fatal(err)
	}
}

func nodesService() *corev1.Service {
	return testService("parley-nodes", "parley",
		corev1.ServicePort{Name: "grpc", Port: 7443, Protocol: corev1.ProtocolTCP},
		corev1.ServicePort{Name: "enroll", Port: 7444, Protocol: corev1.ProtocolTCP},
	)
}

func publicEndpoint(mutate ...func(*v1alpha1.PangolinEndpoint)) *v1alpha1.PangolinEndpoint {
	ep := &v1alpha1.PangolinEndpoint{
		ObjectMeta: metav1.ObjectMeta{Name: "grpc", Namespace: "parley", Generation: 1},
		Spec: v1alpha1.PangolinEndpointSpec{
			BackendRef: v1alpha1.BackendReference{Name: "parley-nodes"},
			Public:     &v1alpha1.PublicEndpointSpec{Protocol: v1alpha1.ProtocolTCP, ProxyPort: 7443},
		},
	}
	for _, m := range mutate {
		m(ep)
	}
	return ep
}

func int32p(v int32) *int32 { return &v }

const nodesHost = "parley-nodes.parley.svc.cluster.local"

// ---------------------------------------------------------------------------
// Happy path

func TestPublic_CreatesRawResourceAndTarget(t *testing.T) {
	ep := publicEndpoint()
	env := newPublicEnv(t, nodesService(), ep)
	env.mustReconcile(ep)

	res := env.pangolin.resourceByNiceID(publicNiceID)
	if res == nil {
		t.Fatalf("no resource carries nice ID %q", publicNiceID)
	}
	if res.Mode != "tcp" || res.ProxyPort != 7443 || res.Name != publicNiceID || !res.Enabled {
		t.Fatalf("resource = %+v", res)
	}

	targets := env.pangolin.targetsFor(res.ID)
	if len(targets) != 1 || targets[0].SiteID != 1 || targets[0].IP != nodesHost || targets[0].Port != 7443 || !targets[0].Enabled {
		t.Fatalf("targets = %+v", targets)
	}

	got := env.get(ep)
	if got.Status.ResourceID != strconv.Itoa(res.ID) {
		t.Fatalf("status.resourceId = %q want %d", got.Status.ResourceID, res.ID)
	}
	if got.Status.SiteResourceID != "" {
		t.Fatalf("status.siteResourceId = %q, want empty for a public endpoint", got.Status.SiteResourceID)
	}
	if got.Status.ResolvedPorts == nil || got.Status.ResolvedPorts.TCP != "7443" || got.Status.ResolvedPorts.UDP != "" {
		t.Fatalf("status.resolvedPorts = %+v", got.Status.ResolvedPorts)
	}
	assertCondition(t, got, v1alpha1.ConditionProgrammed, metav1.ConditionTrue, v1alpha1.ReasonReconciled)
	assertCondition(t, got, v1alpha1.ConditionReady, metav1.ConditionTrue, v1alpha1.ReasonReconciled)
}

func TestPublic_UnchangedReconcileIssuesNoWrites(t *testing.T) {
	ep := publicEndpoint()
	env := newPublicEnv(t, nodesService(), ep)
	env.mustReconcile(ep)

	before := env.pangolin.writes()
	env.mustReconcile(ep)
	if after := env.pangolin.writes(); after != before {
		t.Fatalf("steady-state reconcile issued %d writes", after-before)
	}
}

func TestPublic_UDPEndpoint(t *testing.T) {
	ep := publicEndpoint(func(ep *v1alpha1.PangolinEndpoint) {
		ep.Spec.Public.Protocol = v1alpha1.ProtocolUDP
		ep.Spec.Public.ProxyPort = 5353
	})
	svc := testService("parley-nodes", "parley", corev1.ServicePort{Port: 5353, Protocol: corev1.ProtocolUDP})
	env := newPublicEnv(t, svc, ep)
	env.mustReconcile(ep)

	res := env.pangolin.resourceByNiceID(publicNiceID)
	if res == nil || res.Mode != "udp" || res.ProxyPort != 5353 {
		t.Fatalf("resource = %+v", res)
	}
	if got := env.get(ep); got.Status.ResolvedPorts.UDP != "5353" || got.Status.ResolvedPorts.TCP != "" {
		t.Fatalf("status.resolvedPorts = %+v", got.Status.ResolvedPorts)
	}
}

func TestPublic_DisabledEndpointIsSentDisabled(t *testing.T) {
	disabled := false
	ep := publicEndpoint(func(ep *v1alpha1.PangolinEndpoint) { ep.Spec.Enabled = &disabled })
	env := newPublicEnv(t, nodesService(), ep)
	env.mustReconcile(ep)

	if res := env.pangolin.resourceByNiceID(publicNiceID); res == nil || res.Enabled {
		t.Fatalf("resource = %+v, want disabled", res)
	}
}

func TestPublic_ProxyPortChangeUpdatesInPlace(t *testing.T) {
	ep := publicEndpoint(func(ep *v1alpha1.PangolinEndpoint) { ep.Spec.Public.ServicePort = int32p(7443) })
	env := newPublicEnv(t, nodesService(), ep)
	env.mustReconcile(ep)
	first := env.pangolin.resourceByNiceID(publicNiceID)

	env.mutate(ep, func(ep *v1alpha1.PangolinEndpoint) { ep.Spec.Public.ProxyPort = 17443 })
	env.mustReconcile(ep)

	res := env.pangolin.resourceByNiceID(publicNiceID)
	if res.ID != first.ID || res.ProxyPort != 17443 {
		t.Fatalf("resource = %+v, want id %d on port 17443", res, first.ID)
	}
	if env.pangolin.creates != 1 {
		t.Fatalf("creates = %d want 1", env.pangolin.creates)
	}
	if targets := env.pangolin.targetsFor(res.ID); len(targets) != 1 || targets[0].Port != 7443 {
		t.Fatalf("targets = %+v, want the service port unchanged", targets)
	}
}

func TestPublic_ServicePortChangeReplacesTarget(t *testing.T) {
	ep := publicEndpoint()
	env := newPublicEnv(t, nodesService(), ep)
	env.mustReconcile(ep)
	res := env.pangolin.resourceByNiceID(publicNiceID)

	env.mutate(ep, func(ep *v1alpha1.PangolinEndpoint) { ep.Spec.Public.ServicePort = int32p(7444) })
	env.mustReconcile(ep)

	targets := env.pangolin.targetsFor(res.ID)
	if len(targets) != 1 || targets[0].Port != 7444 {
		t.Fatalf("targets = %+v, want a single target on 7444", targets)
	}
}

func TestPublic_StaleTargetsAreRemoved(t *testing.T) {
	ep := publicEndpoint()
	env := newPublicEnv(t, nodesService(), ep)
	own := env.pangolin.seed(pangolin.Resource{Name: publicNiceID, NiceID: publicNiceID, Mode: "tcp", ProxyPort: 7443, Enabled: true})
	env.pangolin.seedTarget(own.ID, pangolin.Target{SiteID: 1, IP: nodesHost, Port: 7443, Enabled: true})
	env.pangolin.seedTarget(own.ID, pangolin.Target{SiteID: 1, IP: "10.1.2.3", Port: 7443, Enabled: true})
	env.pangolin.seedTarget(own.ID, pangolin.Target{SiteID: 1, IP: nodesHost, Port: 7443, Enabled: true}) // duplicate

	env.mustReconcile(ep)

	targets := env.pangolin.targetsFor(own.ID)
	if len(targets) != 1 || targets[0].IP != nodesHost {
		t.Fatalf("targets = %+v, want only the desired one", targets)
	}
	if env.pangolin.targetCreates != 0 {
		t.Fatalf("targetCreates = %d, want the matching target kept", env.pangolin.targetCreates)
	}
}

func TestPublic_OneTargetPerSite(t *testing.T) {
	ep := publicEndpoint(func(ep *v1alpha1.PangolinEndpoint) { ep.Spec.SiteRefs = []string{"test-site", "second-site"} })
	env := newPublicEnv(t, nodesService(), ep)
	env.mustReconcile(ep)

	res := env.pangolin.resourceByNiceID(publicNiceID)
	targets := env.pangolin.targetsFor(res.ID)
	if len(targets) != 2 || targets[0].SiteID != 1 || targets[1].SiteID != 2 {
		t.Fatalf("targets = %+v, want one on each site", targets)
	}
}

// ---------------------------------------------------------------------------
// Identity

func TestPublic_RecoversByNiceIDWhenStatusIsLost(t *testing.T) {
	ep := publicEndpoint()
	env := newPublicEnv(t, nodesService(), ep)
	own := env.pangolin.seed(pangolin.Resource{Name: publicNiceID, NiceID: publicNiceID, Mode: "tcp", ProxyPort: 7443, Enabled: true})

	env.mustReconcile(ep)

	if env.pangolin.creates != 0 {
		t.Fatalf("creates = %d, want recovery", env.pangolin.creates)
	}
	if got := env.get(ep); got.Status.ResourceID != strconv.Itoa(own.ID) {
		t.Fatalf("status.resourceId = %q want %d", got.Status.ResourceID, own.ID)
	}
}

func TestPublic_RecoversByNameAfterInterruptedNaming(t *testing.T) {
	ep := publicEndpoint()
	env := newPublicEnv(t, nodesService(), ep)
	orphan := env.pangolin.seed(pangolin.Resource{Name: publicNiceID, NiceID: "legal-plains-viscacha-rat", Mode: "tcp", ProxyPort: 7443, Enabled: true})

	env.mustReconcile(ep)

	if env.pangolin.creates != 0 {
		t.Fatalf("creates = %d, want the orphan recovered", env.pangolin.creates)
	}
	if res := env.pangolin.resourceByNiceID(publicNiceID); res == nil || res.ID != orphan.ID {
		t.Fatalf("resource with nice ID = %+v, want the orphan %d renamed", res, orphan.ID)
	}
}

func TestPublic_NameMatchOfOtherProtocolIsNotRecovered(t *testing.T) {
	ep := publicEndpoint()
	env := newPublicEnv(t, nodesService(), ep)
	env.pangolin.seed(pangolin.Resource{Name: publicNiceID, NiceID: "someone-else", Mode: "udp", ProxyPort: 9000, Enabled: true})

	env.mustReconcile(ep)

	if env.pangolin.creates != 1 {
		t.Fatalf("creates = %d, want a fresh tcp resource", env.pangolin.creates)
	}
	if res := env.pangolin.resourceByNiceID("someone-else"); res == nil || res.Mode != "udp" {
		t.Fatalf("the udp resource was modified: %+v", res)
	}
}

func TestPublic_FailedNamingKeepsTheResourceID(t *testing.T) {
	ep := publicEndpoint()
	env := newPublicEnv(t, nodesService(), ep)
	env.pangolin.updateFails = true

	if _, err := env.reconcile(ep); err == nil {
		t.Fatal("reconcile succeeded although the naming update failed")
	}
	recorded := env.get(ep).Status.ResourceID
	if recorded == "" {
		t.Fatal("status.resourceId was not recorded after a successful create")
	}

	env.pangolin.updateFails = false
	env.mustReconcile(ep)

	if env.pangolin.creates != 1 {
		t.Fatalf("creates = %d want 1", env.pangolin.creates)
	}
	res := env.pangolin.resourceByNiceID(publicNiceID)
	if res == nil || strconv.Itoa(res.ID) != recorded {
		t.Fatalf("resource with nice ID = %+v, want recorded id %s", res, recorded)
	}
}

func TestPublic_FailedListingCreatesNothing(t *testing.T) {
	ep := publicEndpoint()
	env := newPublicEnv(t, nodesService(), ep)
	env.pangolin.listFails = true

	if _, err := env.reconcile(ep); err == nil {
		t.Fatal("reconcile succeeded although the listing failed")
	}
	if env.pangolin.creates != 0 {
		t.Fatalf("creates = %d, want none after a failed lookup", env.pangolin.creates)
	}
}

func TestPublic_RecoveryFollowsPagination(t *testing.T) {
	ep := publicEndpoint()
	env := newPublicEnv(t, nodesService(), ep)
	env.pangolin.pageSize = 2
	for i := 0; i < 5; i++ {
		env.pangolin.seed(pangolin.Resource{Name: fmt.Sprintf("other-%d", i), NiceID: fmt.Sprintf("other-%d", i), Mode: "http"})
	}
	own := env.pangolin.seed(pangolin.Resource{Name: publicNiceID, NiceID: publicNiceID, Mode: "tcp", ProxyPort: 7443, Enabled: true})

	env.mustReconcile(ep)

	if env.pangolin.creates != 0 {
		t.Fatalf("creates = %d, want the resource on page 3 found", env.pangolin.creates)
	}
	if got := env.get(ep); got.Status.ResourceID != strconv.Itoa(own.ID) {
		t.Fatalf("status.resourceId = %q want %d", got.Status.ResourceID, own.ID)
	}
}

func TestPublic_WrongKindNiceIDMatchIsRefused(t *testing.T) {
	ep := publicEndpoint()
	env := newPublicEnv(t, nodesService(), ep)
	env.pangolin.seed(pangolin.Resource{Name: "web", NiceID: publicNiceID, Mode: "http", Enabled: true})

	res, err := env.reconcile(ep)
	if err != nil {
		t.Fatalf("reconcile returned %v, want an operator-fixable condition", err)
	}
	if res.RequeueAfter == 0 {
		t.Fatal("no requeue scheduled")
	}
	if w := env.pangolin.writes(); w != 0 {
		t.Fatalf("writes = %d, want none", w)
	}
	assertCondition(t, env.get(ep), v1alpha1.ConditionProgrammed, metav1.ConditionFalse, v1alpha1.ReasonIdentityAmbiguous)
}

// ---------------------------------------------------------------------------
// Port exclusivity

func TestPublic_TakenPortIsReportedNotAdopted(t *testing.T) {
	ep := publicEndpoint()
	env := newPublicEnv(t, nodesService(), ep)
	env.pangolin.seed(pangolin.Resource{Name: "someone-elses", NiceID: "someone-elses", Mode: "tcp", ProxyPort: 7443, Enabled: true})

	res, err := env.reconcile(ep)
	if err != nil {
		t.Fatalf("reconcile returned %v, want an operator-fixable condition", err)
	}
	if res.RequeueAfter == 0 {
		t.Fatal("no requeue scheduled")
	}
	if w := env.pangolin.writes(); w != 0 {
		t.Fatalf("writes = %d, want none", w)
	}
	got := env.get(ep)
	assertCondition(t, got, v1alpha1.ConditionProgrammed, metav1.ConditionFalse, v1alpha1.ReasonProxyPortInUse)
	if msg := conditionOf(t, got, v1alpha1.ConditionProgrammed).Message; !strings.Contains(msg, "someone-elses") {
		t.Fatalf("message %q does not name the holder", msg)
	}
}

func TestPublic_MovingToTakenPortIsRefused(t *testing.T) {
	ep := publicEndpoint(func(ep *v1alpha1.PangolinEndpoint) { ep.Spec.Public.ServicePort = int32p(7443) })
	env := newPublicEnv(t, nodesService(), ep)
	env.mustReconcile(ep)
	env.pangolin.seed(pangolin.Resource{Name: "holder", NiceID: "holder", Mode: "tcp", ProxyPort: 8443, Enabled: true})

	env.mutate(ep, func(ep *v1alpha1.PangolinEndpoint) { ep.Spec.Public.ProxyPort = 8443 })
	if _, err := env.reconcile(ep); err != nil {
		t.Fatalf("reconcile returned %v, want an operator-fixable condition", err)
	}

	if res := env.pangolin.resourceByNiceID(publicNiceID); res.ProxyPort != 7443 {
		t.Fatalf("proxy port = %d, want 7443 kept", res.ProxyPort)
	}
	assertCondition(t, env.get(ep), v1alpha1.ConditionProgrammed, metav1.ConditionFalse, v1alpha1.ReasonProxyPortInUse)
}

func TestPublic_SamePortOnOtherProtocolIsAllowed(t *testing.T) {
	ep := publicEndpoint()
	env := newPublicEnv(t, nodesService(), ep)
	env.pangolin.seed(pangolin.Resource{Name: "dns", NiceID: "dns", Mode: "udp", ProxyPort: 7443, Enabled: true})

	env.mustReconcile(ep)

	if res := env.pangolin.resourceByNiceID(publicNiceID); res == nil || res.Mode != "tcp" {
		t.Fatalf("resource = %+v, want a tcp resource created", res)
	}
}

// ---------------------------------------------------------------------------
// Backend and lifecycle

func TestPublic_ServicePortNotExposedIsRefused(t *testing.T) {
	ep := publicEndpoint(func(ep *v1alpha1.PangolinEndpoint) { ep.Spec.Public.ProxyPort = 9999 })
	env := newPublicEnv(t, nodesService(), ep)

	res, err := env.reconcile(ep)
	if err != nil {
		t.Fatalf("reconcile returned %v, want an operator-fixable condition", err)
	}
	if res.RequeueAfter == 0 {
		t.Fatal("no requeue scheduled")
	}
	if w := env.pangolin.writes(); w != 0 {
		t.Fatalf("writes = %d, want none", w)
	}
	assertCondition(t, env.get(ep), v1alpha1.ConditionResolvedRefs, metav1.ConditionFalse, v1alpha1.ReasonBackendUnsupported)
}

func TestPublic_ServicePortWithOtherProtocolIsRefused(t *testing.T) {
	ep := publicEndpoint(func(ep *v1alpha1.PangolinEndpoint) { ep.Spec.Public.Protocol = v1alpha1.ProtocolUDP })
	env := newPublicEnv(t, nodesService(), ep)

	if _, err := env.reconcile(ep); err != nil {
		t.Fatal(err)
	}
	assertCondition(t, env.get(ep), v1alpha1.ConditionResolvedRefs, metav1.ConditionFalse, v1alpha1.ReasonBackendUnsupported)
}

func TestPublic_DeletionRemovesResourceAndFinalizer(t *testing.T) {
	ep := publicEndpoint()
	env := newPublicEnv(t, nodesService(), ep)
	env.mustReconcile(ep)

	if err := env.k8s.Delete(context.Background(), env.get(ep)); err != nil {
		t.Fatal(err)
	}
	env.mustReconcile(ep)

	if env.pangolin.deletes != 1 || env.pangolin.resourceByNiceID(publicNiceID) != nil {
		t.Fatalf("deletes = %d; resource still present", env.pangolin.deletes)
	}
	out := &v1alpha1.PangolinEndpoint{}
	if err := env.k8s.Get(context.Background(), types.NamespacedName{Name: ep.Name, Namespace: ep.Namespace}, out); err == nil {
		t.Fatalf("object still present with finalizers %v", out.Finalizers)
	}
}

func TestPublic_DeletionOfAlreadyAbsentResourceSucceeds(t *testing.T) {
	ep := publicEndpoint()
	env := newPublicEnv(t, nodesService(), ep)
	env.mustReconcile(ep)

	res := env.pangolin.resourceByNiceID(publicNiceID)
	env.pangolin.mu.Lock()
	delete(env.pangolin.resources, res.ID)
	env.pangolin.mu.Unlock()

	if err := env.k8s.Delete(context.Background(), env.get(ep)); err != nil {
		t.Fatal(err)
	}
	env.mustReconcile(ep)

	out := &v1alpha1.PangolinEndpoint{}
	if err := env.k8s.Get(context.Background(), types.NamespacedName{Name: ep.Name, Namespace: ep.Namespace}, out); err == nil {
		t.Fatalf("object still present with finalizers %v", out.Finalizers)
	}
}

func TestPublic_ResourceDeletedOutOfBandIsRecreated(t *testing.T) {
	ep := publicEndpoint()
	env := newPublicEnv(t, nodesService(), ep)
	env.mustReconcile(ep)

	res := env.pangolin.resourceByNiceID(publicNiceID)
	env.pangolin.mu.Lock()
	delete(env.pangolin.resources, res.ID)
	env.pangolin.mu.Unlock()

	env.mustReconcile(ep)

	again := env.pangolin.resourceByNiceID(publicNiceID)
	if again == nil || again.ID == res.ID {
		t.Fatalf("resource = %+v, want a new one", again)
	}
	if got := env.get(ep); got.Status.ResourceID != strconv.Itoa(again.ID) {
		t.Fatalf("status.resourceId = %q want %d", got.Status.ResourceID, again.ID)
	}
}
