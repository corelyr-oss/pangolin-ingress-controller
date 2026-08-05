package controller

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/vinzenz/pangolin-ingress-controller/api/v1alpha1"
	"github.com/vinzenz/pangolin-ingress-controller/internal/pangolin"
)

// ---------------------------------------------------------------------------
// A stateful fake Pangolin instance.
//
// The controller talks to a real *pangolin.Client, so these tests exercise
// request construction and response decoding alongside the reconcile logic.

type fakePangolin struct {
	mu sync.Mutex

	nextID    int
	resources map[int]*pangolin.SiteResource
	roleIDs   map[int][]int
	userIDs   map[int][]string
	clientIDs map[int][]int

	roles   []pangolin.Role
	clients []pangolin.PangolinClient
	users   map[string]string

	// createUnsupported makes the create endpoint answer 404, as a Pangolin
	// build without private resources would.
	createUnsupported bool
	// deleteFails makes deletion fail, standing in for an unreachable API.
	deleteFails bool

	creates  int
	updates  int
	deletes  int
	roleSets int
}

func newFakePangolin() *fakePangolin {
	return &fakePangolin{
		nextID:    100,
		resources: map[int]*pangolin.SiteResource{},
		roleIDs:   map[int][]int{},
		userIDs:   map[int][]string{},
		clientIDs: map[int][]int{},
		roles:     []pangolin.Role{{ID: 3, Name: "developers"}},
		clients:   []pangolin.PangolinClient{{ID: 12, Name: "vinzenz-laptop"}},
		users:     map[string]string{"office@corelyr.com": "u-1"},
	}
}

func (f *fakePangolin) counts() (creates, updates, deletes int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.creates, f.updates, f.deletes
}

func (f *fakePangolin) only() *pangolin.SiteResource {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, r := range f.resources {
		return r
	}
	return nil
}

func writeData(w http.ResponseWriter, data interface{}) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]interface{}{
		"data": data, "success": true, "error": false, "message": "", "status": 200,
	})
}

func (f *fakePangolin) handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()

		path := strings.TrimPrefix(r.URL.Path, "/v1/")
		parts := strings.Split(path, "/")

		switch {
		// GET /org/{org}/site/{niceID}
		case len(parts) == 4 && parts[0] == "org" && parts[2] == "site" && r.Method == http.MethodGet:
			writeData(w, map[string]interface{}{"siteId": 1, "niceId": parts[3], "name": "test site"})

		// GET /org/{org}/site/{siteID}/resource/nice/{niceID}
		case len(parts) == 7 && parts[0] == "org" && parts[4] == "resource" && parts[5] == "nice":
			for _, res := range f.resources {
				if res.NiceID == parts[6] {
					writeData(w, res)
					return
				}
			}
			http.Error(w, `{"message":"not found"}`, http.StatusNotFound)

		// PUT /org/{org}/site-resource
		case len(parts) == 3 && parts[0] == "org" && parts[2] == "site-resource" && r.Method == http.MethodPut:
			if f.createUnsupported {
				http.Error(w, `{"message":"not found"}`, http.StatusNotFound)
				return
			}
			var req pangolin.CreateSiteResourceRequest
			_ = json.NewDecoder(r.Body).Decode(&req)
			f.creates++
			f.nextID++
			res := &pangolin.SiteResource{
				ID: f.nextID, NiceID: req.NiceID, Name: req.Name, Mode: req.Mode,
				SiteID: req.SiteID, Destination: req.Destination, DestinationPort: req.DestinationPort,
				Alias: req.Alias, TCPPortRange: req.TCPPortRange, UDPPortRange: req.UDPPortRange,
				DisableICMP: req.DisableICMP, Enabled: true,
			}
			f.resources[res.ID] = res
			f.roleIDs[res.ID] = req.RoleIDs
			f.userIDs[res.ID] = req.UserIDs
			f.clientIDs[res.ID] = req.ClientIDs
			writeData(w, res)

		// GET /org/{org}/roles
		case len(parts) == 3 && parts[0] == "org" && parts[2] == "roles" && r.Method == http.MethodGet:
			writeData(w, map[string]interface{}{"roles": f.roles})

		// GET /org/{org}/clients
		case len(parts) == 3 && parts[0] == "org" && parts[2] == "clients" && r.Method == http.MethodGet:
			writeData(w, map[string]interface{}{"clients": f.clients})

		// GET /org/{org}/user-by-username?username=
		case len(parts) == 3 && parts[0] == "org" && parts[2] == "user-by-username":
			id, ok := f.users[r.URL.Query().Get("username")]
			if !ok {
				http.Error(w, `{"message":"not found"}`, http.StatusNotFound)
				return
			}
			writeData(w, map[string]interface{}{"userId": id, "username": r.URL.Query().Get("username")})

		// /site-resource/{id}[/sub]
		case parts[0] == "site-resource":
			id, _ := strconv.Atoi(parts[1])
			res, ok := f.resources[id]
			if !ok {
				http.Error(w, `{"message":"not found"}`, http.StatusNotFound)
				return
			}

			if len(parts) == 3 {
				f.handleSub(w, r, id, parts[2])
				return
			}

			switch r.Method {
			case http.MethodGet:
				writeData(w, res)
			case http.MethodPost:
				var req pangolin.UpdateSiteResourceRequest
				_ = json.NewDecoder(r.Body).Decode(&req)
				f.updates++
				if req.Destination != nil {
					res.Destination = *req.Destination
				}
				if req.Alias != nil {
					res.Alias = *req.Alias
				}
				if req.TCPPortRange != nil {
					res.TCPPortRange = *req.TCPPortRange
				}
				if req.UDPPortRange != nil {
					res.UDPPortRange = *req.UDPPortRange
				}
				// destinationPort is nullable and always sent, so a null
				// clears it -- mirroring the API contract the controller
				// relies on to converge.
				res.DestinationPort = 0
				if req.DestinationPort != nil {
					res.DestinationPort = *req.DestinationPort
				}
				if req.Enabled != nil {
					res.Enabled = *req.Enabled
				}
				if req.DisableICMP != nil {
					res.DisableICMP = *req.DisableICMP
				}
				if req.Name != "" {
					res.Name = req.Name
				}
				writeData(w, res)
			case http.MethodDelete:
				if f.deleteFails {
					http.Error(w, `{"message":"boom"}`, http.StatusInternalServerError)
					return
				}
				f.deletes++
				delete(f.resources, id)
				writeData(w, map[string]interface{}{})
			}

		default:
			http.Error(w, fmt.Sprintf(`{"message":"unhandled %s %s"}`, r.Method, r.URL.Path), http.StatusNotFound)
		}
	})
}

func (f *fakePangolin) handleSub(w http.ResponseWriter, r *http.Request, id int, sub string) {
	switch sub {
	case "roles":
		if r.Method == http.MethodGet {
			out := []map[string]int{}
			for _, v := range f.roleIDs[id] {
				out = append(out, map[string]int{"roleId": v})
			}
			writeData(w, map[string]interface{}{"roles": out})
			return
		}
		var body struct {
			RoleIDs []int `json:"roleIds"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		f.roleSets++
		f.roleIDs[id] = body.RoleIDs
		writeData(w, map[string]interface{}{})
	case "users":
		if r.Method == http.MethodGet {
			out := []map[string]string{}
			for _, v := range f.userIDs[id] {
				out = append(out, map[string]string{"userId": v})
			}
			writeData(w, map[string]interface{}{"users": out})
			return
		}
		var body struct {
			UserIDs []string `json:"userIds"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		f.userIDs[id] = body.UserIDs
		writeData(w, map[string]interface{}{})
	case "clients":
		if r.Method == http.MethodGet {
			out := []map[string]int{}
			for _, v := range f.clientIDs[id] {
				out = append(out, map[string]int{"clientId": v})
			}
			writeData(w, map[string]interface{}{"clients": out})
			return
		}
		var body struct {
			ClientIDs []int `json:"clientIds"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		f.clientIDs[id] = body.ClientIDs
		writeData(w, map[string]interface{}{})
	default:
		http.Error(w, `{"message":"unhandled sub"}`, http.StatusNotFound)
	}
}

// ---------------------------------------------------------------------------
// Test environment

type testEnv struct {
	t          *testing.T
	reconciler *PangolinEndpointReconciler
	k8s        client.Client
	pangolin   *fakePangolin
	server     *httptest.Server
}

func endpointScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	s := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(s); err != nil {
		t.Fatal(err)
	}
	if err := v1alpha1.AddToScheme(s); err != nil {
		t.Fatal(err)
	}
	return s
}

func newTestEnv(t *testing.T, objs ...client.Object) *testEnv {
	t.Helper()

	fp := newFakePangolin()
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
		AliasSuffix:              "corp.internal",
		NameCacheRefreshInterval: time.Minute,
		principals:               newPrincipalResolver(pc, time.Minute),
	}

	return &testEnv{t: t, reconciler: r, k8s: k8s, pangolin: fp, server: srv}
}

func (e *testEnv) reconcile(ep *v1alpha1.PangolinEndpoint) (ctrl.Result, error) {
	e.t.Helper()
	return e.reconciler.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: ep.Name, Namespace: ep.Namespace},
	})
}

func (e *testEnv) get(ep *v1alpha1.PangolinEndpoint) *v1alpha1.PangolinEndpoint {
	e.t.Helper()
	out := &v1alpha1.PangolinEndpoint{}
	if err := e.k8s.Get(context.Background(), types.NamespacedName{Name: ep.Name, Namespace: ep.Namespace}, out); err != nil {
		e.t.Fatal(err)
	}
	return out
}

func testService(name, namespace string, ports ...corev1.ServicePort) *corev1.Service {
	if len(ports) == 0 {
		ports = []corev1.ServicePort{{Port: 5432, Protocol: corev1.ProtocolTCP}}
	}
	return &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
		Spec:       corev1.ServiceSpec{ClusterIP: "10.0.0.1", Ports: ports},
	}
}

func testEndpoint(mutate ...func(*v1alpha1.PangolinEndpoint)) *v1alpha1.PangolinEndpoint {
	ep := &v1alpha1.PangolinEndpoint{
		ObjectMeta: metav1.ObjectMeta{Name: "postgres", Namespace: "data", Generation: 1},
		Spec: v1alpha1.PangolinEndpointSpec{
			BackendRef: v1alpha1.BackendReference{Name: "postgres"},
			Private: &v1alpha1.PrivateEndpointSpec{
				Access: &v1alpha1.AccessSpec{
					Roles:   []string{"developers"},
					Clients: []string{"vinzenz-laptop"},
					Users:   []string{"office@corelyr.com"},
				},
			},
		},
	}
	for _, m := range mutate {
		m(ep)
	}
	return ep
}

func conditionOf(t *testing.T, ep *v1alpha1.PangolinEndpoint, condType string) *metav1.Condition {
	t.Helper()
	return meta.FindStatusCondition(ep.Status.Conditions, condType)
}

func assertCondition(t *testing.T, ep *v1alpha1.PangolinEndpoint, condType string, status metav1.ConditionStatus, reason string) {
	t.Helper()
	c := conditionOf(t, ep, condType)
	if c == nil {
		t.Fatalf("condition %s not set; conditions=%+v", condType, ep.Status.Conditions)
	}
	if c.Status != status {
		t.Fatalf("condition %s = %s want %s (reason %q, message %q)", condType, c.Status, status, c.Reason, c.Message)
	}
	if reason != "" && c.Reason != reason {
		t.Fatalf("condition %s reason = %q want %q", condType, c.Reason, reason)
	}
}

// ---------------------------------------------------------------------------
// Happy path

func TestEndpoint_CreatesPrivateResource(t *testing.T) {
	ep := testEndpoint()
	env := newTestEnv(t, testService("postgres", "data"), ep)

	if _, err := env.reconcile(ep); err != nil {
		t.Fatal(err)
	}

	res := env.pangolin.only()
	if res == nil {
		t.Fatal("no private resource was created")
	}
	if res.Mode != "host" {
		t.Fatalf("mode = %q want host", res.Mode)
	}
	if res.Destination != "postgres.data.svc.cluster.local" {
		t.Fatalf("destination = %q", res.Destination)
	}
	if res.Alias != "postgres.data.corp.internal" {
		t.Fatalf("alias = %q", res.Alias)
	}
	if res.TCPPortRange != "5432" {
		t.Fatalf("tcp ports = %q want 5432", res.TCPPortRange)
	}
	if res.NiceID != "pangolin-controller-data-postgres" {
		t.Fatalf("niceId = %q", res.NiceID)
	}

	got := env.get(ep)
	if got.Status.SiteResourceID != strconv.Itoa(res.ID) {
		t.Fatalf("status.siteResourceId = %q want %d", got.Status.SiteResourceID, res.ID)
	}
	if got.Status.Address != "postgres.data.corp.internal" {
		t.Fatalf("status.address = %q", got.Status.Address)
	}
	if got.Status.ResolvedPorts == nil || got.Status.ResolvedPorts.TCP != "5432" {
		t.Fatalf("status.resolvedPorts = %+v", got.Status.ResolvedPorts)
	}
	assertCondition(t, got, v1alpha1.ConditionAccepted, metav1.ConditionTrue, "")
	assertCondition(t, got, v1alpha1.ConditionResolvedRefs, metav1.ConditionTrue, "")
	assertCondition(t, got, v1alpha1.ConditionProgrammed, metav1.ConditionTrue, "")
	assertCondition(t, got, v1alpha1.ConditionReady, metav1.ConditionTrue, "")

	// Principals sent with the create.
	env.pangolin.mu.Lock()
	defer env.pangolin.mu.Unlock()
	if len(env.pangolin.roleIDs[res.ID]) != 1 || env.pangolin.roleIDs[res.ID][0] != 3 {
		t.Fatalf("roles = %v want [3]", env.pangolin.roleIDs[res.ID])
	}
	if len(env.pangolin.clientIDs[res.ID]) != 1 || env.pangolin.clientIDs[res.ID][0] != 12 {
		t.Fatalf("clients = %v want [12]", env.pangolin.clientIDs[res.ID])
	}
	if len(env.pangolin.userIDs[res.ID]) != 1 || env.pangolin.userIDs[res.ID][0] != "u-1" {
		t.Fatalf("users = %v want [u-1]", env.pangolin.userIDs[res.ID])
	}
}

// The regression guard for the semantic port comparison: a steady state must
// not generate writes.
func TestEndpoint_UnchangedReconcileIssuesNoUpdate(t *testing.T) {
	ep := testEndpoint()
	env := newTestEnv(t, testService("postgres", "data"), ep)

	for i := 0; i < 3; i++ {
		if _, err := env.reconcile(ep); err != nil {
			t.Fatalf("reconcile %d: %v", i, err)
		}
	}

	creates, updates, _ := env.pangolin.counts()
	if creates != 1 {
		t.Fatalf("creates = %d want 1", creates)
	}
	if updates != 0 {
		t.Fatalf("expected no updates for an unchanged endpoint, got %d", updates)
	}
}

func TestEndpoint_ServerNormalisedPortsAreNotAChange(t *testing.T) {
	ep := testEndpoint(func(ep *v1alpha1.PangolinEndpoint) {
		ep.Spec.Private.Ports = []v1alpha1.EndpointPort{
			{Protocol: v1alpha1.ProtocolTCP, Port: ptrInt32(5432)},
			{Protocol: v1alpha1.ProtocolTCP, Port: ptrInt32(5433)},
		}
	})
	env := newTestEnv(t, testService("postgres", "data"), ep)

	if _, err := env.reconcile(ep); err != nil {
		t.Fatal(err)
	}

	// Pangolin stores the equivalent range rather than the list it was sent.
	env.pangolin.mu.Lock()
	for _, res := range env.pangolin.resources {
		res.TCPPortRange = "5432-5433"
	}
	env.pangolin.mu.Unlock()

	if _, err := env.reconcile(ep); err != nil {
		t.Fatal(err)
	}

	_, updates, _ := env.pangolin.counts()
	if updates != 0 {
		t.Fatalf("server-side normalisation must not count as a change, got %d updates", updates)
	}
}

func TestEndpoint_PortChangeIssuesExactlyOneUpdate(t *testing.T) {
	ep := testEndpoint(func(ep *v1alpha1.PangolinEndpoint) {
		ep.Spec.Private.Ports = []v1alpha1.EndpointPort{{Protocol: v1alpha1.ProtocolTCP, Port: ptrInt32(5432)}}
	})
	env := newTestEnv(t, testService("postgres", "data"), ep)

	if _, err := env.reconcile(ep); err != nil {
		t.Fatal(err)
	}

	current := env.get(ep)
	current.Spec.Private.Ports = append(current.Spec.Private.Ports,
		v1alpha1.EndpointPort{Protocol: v1alpha1.ProtocolTCP, Port: ptrInt32(9187)})
	current.Generation = 2
	if err := env.k8s.Update(context.Background(), current); err != nil {
		t.Fatal(err)
	}

	if _, err := env.reconcile(ep); err != nil {
		t.Fatal(err)
	}
	if _, err := env.reconcile(ep); err != nil {
		t.Fatal(err)
	}

	_, updates, _ := env.pangolin.counts()
	if updates != 1 {
		t.Fatalf("updates = %d want exactly 1", updates)
	}
	if got := env.pangolin.only().TCPPortRange; got != "5432,9187" {
		t.Fatalf("tcp ports = %q want 5432,9187", got)
	}
}

func TestEndpoint_PortsDefaultToBackingService(t *testing.T) {
	ep := testEndpoint()
	svc := testService("postgres", "data",
		corev1.ServicePort{Port: 5432, Protocol: corev1.ProtocolTCP},
		corev1.ServicePort{Port: 9187, Protocol: corev1.ProtocolTCP},
	)
	env := newTestEnv(t, svc, ep)

	if _, err := env.reconcile(ep); err != nil {
		t.Fatal(err)
	}

	if got := env.pangolin.only().TCPPortRange; got != "5432,9187" {
		t.Fatalf("tcp ports = %q want 5432,9187", got)
	}
}

// Identity is a pure function of namespace and name, so a lost status must not
// produce a second resource.
func TestEndpoint_RecoversByNiceIDWhenStatusIsLost(t *testing.T) {
	ep := testEndpoint()
	env := newTestEnv(t, testService("postgres", "data"), ep)

	if _, err := env.reconcile(ep); err != nil {
		t.Fatal(err)
	}
	originalID := env.pangolin.only().ID

	current := env.get(ep)
	current.Status.SiteResourceID = ""
	if err := env.k8s.Status().Update(context.Background(), current); err != nil {
		t.Fatal(err)
	}

	if _, err := env.reconcile(ep); err != nil {
		t.Fatal(err)
	}

	creates, _, _ := env.pangolin.counts()
	if creates != 1 {
		t.Fatalf("creates = %d want 1: the endpoint was recovered, not recreated", creates)
	}
	if got := env.get(ep).Status.SiteResourceID; got != strconv.Itoa(originalID) {
		t.Fatalf("status.siteResourceId = %q want %d", got, originalID)
	}
}

// ---------------------------------------------------------------------------
// Operator-fixable conditions: reported and requeued, never returned as errors.

func TestEndpoint_MissingServiceRequeuesWithoutError(t *testing.T) {
	ep := testEndpoint()
	env := newTestEnv(t, ep) // no Service

	res, err := env.reconcile(ep)
	if err != nil {
		t.Fatalf("a missing Service must not be a reconcile error, got %v", err)
	}
	if res.RequeueAfter == 0 {
		t.Fatal("expected a requeue")
	}

	got := env.get(ep)
	assertCondition(t, got, v1alpha1.ConditionResolvedRefs, metav1.ConditionFalse, v1alpha1.ReasonBackendNotFound)
	if creates, _, _ := env.pangolin.counts(); creates != 0 {
		t.Fatalf("nothing should have been created, got %d", creates)
	}
}

func TestEndpoint_UnsupportedBackendServices(t *testing.T) {
	tests := []struct {
		name string
		svc  *corev1.Service
	}{
		{
			name: "headless",
			svc: &corev1.Service{
				ObjectMeta: metav1.ObjectMeta{Name: "postgres", Namespace: "data"},
				Spec:       corev1.ServiceSpec{ClusterIP: corev1.ClusterIPNone, Ports: []corev1.ServicePort{{Port: 5432}}},
			},
		},
		{
			name: "external name",
			svc: &corev1.Service{
				ObjectMeta: metav1.ObjectMeta{Name: "postgres", Namespace: "data"},
				Spec:       corev1.ServiceSpec{Type: corev1.ServiceTypeExternalName, ExternalName: "db.example.com"},
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ep := testEndpoint()
			env := newTestEnv(t, tt.svc, ep)

			if _, err := env.reconcile(ep); err != nil {
				t.Fatalf("unexpected error: %v", err)
			}

			assertCondition(t, env.get(ep), v1alpha1.ConditionResolvedRefs, metav1.ConditionFalse, v1alpha1.ReasonBackendUnsupported)
			if creates, _, _ := env.pangolin.counts(); creates != 0 {
				t.Fatalf("nothing should have been created, got %d", creates)
			}
		})
	}
}

func TestEndpoint_AliasSuffixNotConfigured(t *testing.T) {
	ep := testEndpoint()
	env := newTestEnv(t, testService("postgres", "data"), ep)
	env.reconciler.AliasSuffix = ""

	res, err := env.reconcile(ep)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.RequeueAfter == 0 {
		t.Fatal("expected a requeue")
	}

	assertCondition(t, env.get(ep), v1alpha1.ConditionAccepted, metav1.ConditionFalse, v1alpha1.ReasonAliasSuffixNotConfigured)
	if creates, _, _ := env.pangolin.counts(); creates != 0 {
		t.Fatalf("nothing should have been created, got %d", creates)
	}
}

func TestEndpoint_ExplicitAliasOverridesDerivation(t *testing.T) {
	ep := testEndpoint(func(ep *v1alpha1.PangolinEndpoint) {
		ep.Spec.Private.Alias = "db.internal"
	})
	env := newTestEnv(t, testService("postgres", "data"), ep)

	if _, err := env.reconcile(ep); err != nil {
		t.Fatal(err)
	}
	if got := env.pangolin.only().Alias; got != "db.internal" {
		t.Fatalf("alias = %q want db.internal", got)
	}
}

func TestEndpoint_UnknownPrincipalRequeuesWithoutError(t *testing.T) {
	ep := testEndpoint(func(ep *v1alpha1.PangolinEndpoint) {
		ep.Spec.Private.Access.Roles = []string{"does-not-exist"}
	})
	env := newTestEnv(t, testService("postgres", "data"), ep)

	res, err := env.reconcile(ep)
	if err != nil {
		t.Fatalf("an unknown principal must not be a reconcile error, got %v", err)
	}
	if res.RequeueAfter == 0 {
		t.Fatal("expected a requeue")
	}

	assertCondition(t, env.get(ep), v1alpha1.ConditionResolvedRefs, metav1.ConditionFalse, v1alpha1.ReasonPrincipalNotFound)
}

func TestEndpoint_NoPrincipalsIsCreatedButNotReady(t *testing.T) {
	ep := testEndpoint(func(ep *v1alpha1.PangolinEndpoint) {
		ep.Spec.Private.Access = nil
	})
	env := newTestEnv(t, testService("postgres", "data"), ep)

	if _, err := env.reconcile(ep); err != nil {
		t.Fatal(err)
	}

	if env.pangolin.only() == nil {
		t.Fatal("the endpoint should still be created")
	}
	got := env.get(ep)
	assertCondition(t, got, v1alpha1.ConditionProgrammed, metav1.ConditionTrue, "")
	assertCondition(t, got, v1alpha1.ConditionReady, metav1.ConditionFalse, v1alpha1.ReasonNoPrincipalsGranted)
}

func TestEndpoint_ServerWithoutPrivateResources(t *testing.T) {
	ep := testEndpoint()
	env := newTestEnv(t, testService("postgres", "data"), ep)
	env.pangolin.createUnsupported = true

	res, err := env.reconcile(ep)
	if err != nil {
		t.Fatalf("an unsupported server must not be a reconcile error, got %v", err)
	}
	if res.RequeueAfter == 0 {
		t.Fatal("expected a requeue")
	}

	assertCondition(t, env.get(ep), v1alpha1.ConditionAccepted, metav1.ConditionFalse, v1alpha1.ReasonUnsupportedByServer)
}

func TestEndpoint_ServiceWithoutUsablePorts(t *testing.T) {
	svc := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Name: "postgres", Namespace: "data"},
		Spec: corev1.ServiceSpec{
			ClusterIP: "10.0.0.1",
			Ports:     []corev1.ServicePort{{Port: 9999, Protocol: corev1.ProtocolSCTP}},
		},
	}
	ep := testEndpoint()
	env := newTestEnv(t, svc, ep)

	if _, err := env.reconcile(ep); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	assertCondition(t, env.get(ep), v1alpha1.ConditionResolvedRefs, metav1.ConditionFalse, v1alpha1.ReasonBackendUnsupported)
}

// ---------------------------------------------------------------------------
// Identity

func TestNiceIDDerivation(t *testing.T) {
	r := &PangolinEndpointReconciler{ResourcePrefix: "pangolin-controller"}

	got, err := r.niceIDFor(&v1alpha1.PangolinEndpoint{
		ObjectMeta: metav1.ObjectMeta{Name: "postgres", Namespace: "data"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if got != "pangolin-controller-data-postgres" {
		t.Fatalf("niceID = %q", got)
	}
}

// A name Pangolin cannot express must be refused, not mangled: substituting
// characters could collapse two endpoints onto one identity.
func TestNiceIDRefusesUnexpressibleNames(t *testing.T) {
	r := &PangolinEndpointReconciler{ResourcePrefix: "pangolin-controller"}

	_, err := r.niceIDFor(&v1alpha1.PangolinEndpoint{
		ObjectMeta: metav1.ObjectMeta{Name: "db.primary", Namespace: "data"},
	})
	var issue *endpointIssue
	if !asIssue(err, &issue) || issue.reason != v1alpha1.ReasonIdentityInvalid {
		t.Fatalf("got %v want an IdentityInvalid issue", err)
	}
}

func TestNiceIDRefusesOverLongNames(t *testing.T) {
	r := &PangolinEndpointReconciler{ResourcePrefix: "pangolin-controller"}

	_, err := r.niceIDFor(&v1alpha1.PangolinEndpoint{
		ObjectMeta: metav1.ObjectMeta{Name: strings.Repeat("a", 250), Namespace: "data"},
	})
	var issue *endpointIssue
	if !asIssue(err, &issue) || issue.reason != v1alpha1.ReasonIdentityTooLong {
		t.Fatalf("got %v want an IdentityTooLong issue", err)
	}
}

// ---------------------------------------------------------------------------
// Lifecycle

func TestEndpoint_DeletionRemovesResourceAndFinalizer(t *testing.T) {
	ep := testEndpoint()
	env := newTestEnv(t, testService("postgres", "data"), ep)

	if _, err := env.reconcile(ep); err != nil {
		t.Fatal(err)
	}
	if env.pangolin.only() == nil {
		t.Fatal("setup: nothing was created")
	}

	if err := env.k8s.Delete(context.Background(), env.get(ep)); err != nil {
		t.Fatal(err)
	}
	if _, err := env.reconcile(ep); err != nil {
		t.Fatal(err)
	}

	if _, _, deletes := env.pangolin.counts(); deletes != 1 {
		t.Fatalf("deletes = %d want 1", deletes)
	}
	out := &v1alpha1.PangolinEndpoint{}
	err := env.k8s.Get(context.Background(), types.NamespacedName{Name: ep.Name, Namespace: ep.Namespace}, out)
	if err == nil {
		t.Fatalf("object still present with finalizers %v", out.Finalizers)
	}
}

func TestEndpoint_DeletionOfAlreadyAbsentResourceSucceeds(t *testing.T) {
	ep := testEndpoint()
	env := newTestEnv(t, testService("postgres", "data"), ep)

	if _, err := env.reconcile(ep); err != nil {
		t.Fatal(err)
	}

	// Someone removed it in the Pangolin UI.
	env.pangolin.mu.Lock()
	env.pangolin.resources = map[int]*pangolin.SiteResource{}
	env.pangolin.mu.Unlock()

	if err := env.k8s.Delete(context.Background(), env.get(ep)); err != nil {
		t.Fatal(err)
	}
	if _, err := env.reconcile(ep); err != nil {
		t.Fatalf("deleting an already-absent resource must succeed, got %v", err)
	}

	out := &v1alpha1.PangolinEndpoint{}
	if err := env.k8s.Get(context.Background(), types.NamespacedName{Name: ep.Name, Namespace: ep.Namespace}, out); err == nil {
		t.Fatal("finalizer should have been removed")
	}
}

func TestEndpoint_DeletionIsNotFinalizedWhilePangolinFails(t *testing.T) {
	ep := testEndpoint()
	env := newTestEnv(t, testService("postgres", "data"), ep)

	if _, err := env.reconcile(ep); err != nil {
		t.Fatal(err)
	}

	env.pangolin.mu.Lock()
	env.pangolin.deleteFails = true
	env.pangolin.mu.Unlock()

	if err := env.k8s.Delete(context.Background(), env.get(ep)); err != nil {
		t.Fatal(err)
	}
	if _, err := env.reconcile(ep); err == nil {
		t.Fatal("expected the failure to be returned so the delete is retried")
	}

	out := &v1alpha1.PangolinEndpoint{}
	if err := env.k8s.Get(context.Background(), types.NamespacedName{Name: ep.Name, Namespace: ep.Namespace}, out); err != nil {
		t.Fatalf("object should still exist while deletion is unconfirmed: %v", err)
	}
	if len(out.Finalizers) == 0 {
		t.Fatal("finalizer must remain until Pangolin confirms the delete")
	}
}

// asIssue is errors.As specialised for the test assertions above.
func asIssue(err error, target **endpointIssue) bool {
	issue, ok := err.(*endpointIssue)
	if ok {
		*target = issue
	}
	return ok
}
