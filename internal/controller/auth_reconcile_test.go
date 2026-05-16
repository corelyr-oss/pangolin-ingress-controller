package controller

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"

	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/event"

	"github.com/vinzenz/pangolin-ingress-controller/internal/pangolin"
)

// ---------------------------------------------------------------------------
// Pure helper tests (parseStringSliceAnnotation, parseIntSliceAnnotation,
// parseSecretRef, set-equality helpers).

func TestParseStringSliceAnnotation(t *testing.T) {
	tests := []struct {
		name        string
		annotations map[string]string
		want        []string
		wantErr     bool
	}{
		{"absent", nil, nil, false},
		{"empty string", map[string]string{"k": ""}, nil, false},
		{"empty array", map[string]string{"k": "[]"}, []string{}, false},
		{"with values", map[string]string{"k": `["a","b"]`}, []string{"a", "b"}, false},
		{"malformed", map[string]string{"k": "not-json"}, nil, true},
		{"wrong shape", map[string]string{"k": `{"a":1}`}, nil, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseStringSliceAnnotation(tt.annotations, "k")
			if (err != nil) != tt.wantErr {
				t.Fatalf("err=%v wantErr=%v", err, tt.wantErr)
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("got %#v want %#v", got, tt.want)
			}
		})
	}
}

func TestParseIntSliceAnnotation(t *testing.T) {
	got, err := parseIntSliceAnnotation(map[string]string{"k": "[1,2,3]"}, "k")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, []int{1, 2, 3}) {
		t.Fatalf("got %v", got)
	}
	if _, err := parseIntSliceAnnotation(map[string]string{"k": `["a"]`}, "k"); err == nil {
		t.Fatal("expected error on non-int values")
	}
}

func TestParseSecretRef(t *testing.T) {
	tests := []struct {
		name   string
		value  string
		defNS  string
		wantNS string
		wantNm string
		wantOK bool
	}{
		{"name only", "my-secret", "default", "default", "my-secret", true},
		{"ns/name", "ops/my-secret", "default", "ops", "my-secret", true},
		{"empty", "", "default", "", "", false},
		{"only slash", "/", "default", "", "", false},
		{"trailing slash", "ops/", "default", "", "", false},
		{"leading slash", "/my-secret", "default", "", "", false},
		{"multiple slashes", "ops/team/secret", "default", "", "", false},
		{"whitespace", "  my-secret  ", "default", "default", "my-secret", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ns, nm, ok := parseSecretRef(tt.value, tt.defNS)
			if ns != tt.wantNS || nm != tt.wantNm || ok != tt.wantOK {
				t.Fatalf("got (%q, %q, %v) want (%q, %q, %v)", ns, nm, ok, tt.wantNS, tt.wantNm, tt.wantOK)
			}
		})
	}
}

func TestSetEquality(t *testing.T) {
	if !stringSetsEqual([]string{"a", "b"}, []string{"b", "a"}) {
		t.Fatal("string sets should be equal regardless of order")
	}
	if stringSetsEqual([]string{"a"}, []string{"a", "b"}) {
		t.Fatal("different length sets should not be equal")
	}
	if !intSetsEqual([]int{1, 2, 3}, []int{3, 2, 1}) {
		t.Fatal("int sets should be equal regardless of order")
	}
	if intSetsEqual([]int{1, 2}, []int{1, 3}) {
		t.Fatal("sets with different members should not be equal")
	}
}

// ---------------------------------------------------------------------------
// Predicate: controller-managed annotations must NOT trigger reconciliation.

func TestPangolinAnnotationChangedPredicate_IgnoresManaged(t *testing.T) {
	p := pangolinAnnotationChangedPredicate{}
	for managed := range controllerManagedAnnotations {
		old := &networkingv1.Ingress{ObjectMeta: metav1.ObjectMeta{Annotations: map[string]string{managed: "old"}}}
		new := &networkingv1.Ingress{ObjectMeta: metav1.ObjectMeta{Annotations: map[string]string{managed: "new"}}}
		if p.Update(event.UpdateEvent{ObjectOld: old, ObjectNew: new}) {
			t.Fatalf("predicate should ignore changes to controller-managed annotation %q", managed)
		}
	}
}

func TestPangolinAnnotationChangedPredicate_DetectsUserChanges(t *testing.T) {
	p := pangolinAnnotationChangedPredicate{}
	old := &networkingv1.Ingress{ObjectMeta: metav1.ObjectMeta{Annotations: map[string]string{annotationSSO: "true"}}}
	new := &networkingv1.Ingress{ObjectMeta: metav1.ObjectMeta{Annotations: map[string]string{annotationSSO: "false"}}}
	if !p.Update(event.UpdateEvent{ObjectOld: old, ObjectNew: new}) {
		t.Fatal("predicate should detect user-edited pangolin annotations")
	}

	old2 := &networkingv1.Ingress{ObjectMeta: metav1.ObjectMeta{Annotations: map[string]string{annotationSSO: "true"}}}
	new2 := &networkingv1.Ingress{ObjectMeta: metav1.ObjectMeta{Annotations: map[string]string{}}}
	if !p.Update(event.UpdateEvent{ObjectOld: old2, ObjectNew: new2}) {
		t.Fatal("predicate should detect removal of pangolin annotations")
	}
}

// ---------------------------------------------------------------------------
// Password/pincode sub-reconcile state machine via reconcileSecretBackedAuth.
// The set function is injected, so we don't need a fake Pangolin client.

func newTestReconciler(t *testing.T, ingress *networkingv1.Ingress, secrets ...*corev1.Secret) *IngressReconciler {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	objs := []runtime.Object{ingress}
	for _, s := range secrets {
		objs = append(objs, s)
	}
	c := fake.NewClientBuilder().WithScheme(scheme).WithRuntimeObjects(objs...).Build()
	return &IngressReconciler{Client: c, Scheme: scheme, IngressClass: "pangolin"}
}

func ingressWithAnnotations(ns string, ann map[string]string) *networkingv1.Ingress {
	return &networkingv1.Ingress{ObjectMeta: metav1.ObjectMeta{Name: "i", Namespace: ns, Annotations: ann}}
}

func TestReconcileSecretBackedAuth_NoAnnotNoHash_Noop(t *testing.T) {
	ing := ingressWithAnnotations("ns", map[string]string{})
	r := newTestReconciler(t, ing)
	calls := 0
	setFn := func(_ context.Context, _ string, _ *string) error { calls++; return nil }
	if err := r.reconcileSecretBackedAuth(context.Background(), ing, "1",
		annotationPasswordSecretRef, annotationPasswordHash, secretKeyPassword, "password", setFn); err != nil {
		t.Fatal(err)
	}
	if calls != 0 {
		t.Fatalf("expected 0 set calls, got %d", calls)
	}
	if _, has := ing.Annotations[annotationPasswordHash]; has {
		t.Fatal("no hash should be written")
	}
}

func TestReconcileSecretBackedAuth_AnnotPresent_NoHash_SetsAndWritesHash(t *testing.T) {
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "s", Namespace: "ns"},
		Data:       map[string][]byte{secretKeyPassword: []byte("hunter2!")},
	}
	ing := ingressWithAnnotations("ns", map[string]string{annotationPasswordSecretRef: "s"})
	r := newTestReconciler(t, ing, secret)
	var gotValue *string
	setFn := func(_ context.Context, _ string, v *string) error { gotValue = v; return nil }
	if err := r.reconcileSecretBackedAuth(context.Background(), ing, "1",
		annotationPasswordSecretRef, annotationPasswordHash, secretKeyPassword, "password", setFn); err != nil {
		t.Fatal(err)
	}
	if gotValue == nil || *gotValue != "hunter2!" {
		t.Fatalf("expected set called with value, got %v", gotValue)
	}
	if ing.Annotations[annotationPasswordHash] != hashSecretValue("1", "hunter2!") {
		t.Fatalf("hash not written or wrong: %q", ing.Annotations[annotationPasswordHash])
	}
}

func TestReconcileSecretBackedAuth_MatchingHash_Noop(t *testing.T) {
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "s", Namespace: "ns"},
		Data:       map[string][]byte{secretKeyPassword: []byte("hunter2!")},
	}
	ing := ingressWithAnnotations("ns", map[string]string{
		annotationPasswordSecretRef: "s",
		annotationPasswordHash:      hashSecretValue("1", "hunter2!"),
	})
	r := newTestReconciler(t, ing, secret)
	calls := 0
	setFn := func(_ context.Context, _ string, _ *string) error { calls++; return nil }
	if err := r.reconcileSecretBackedAuth(context.Background(), ing, "1",
		annotationPasswordSecretRef, annotationPasswordHash, secretKeyPassword, "password", setFn); err != nil {
		t.Fatal(err)
	}
	if calls != 0 {
		t.Fatalf("expected 0 set calls when hash matches, got %d", calls)
	}
}

func TestReconcileSecretBackedAuth_StaleHash_Resets(t *testing.T) {
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "s", Namespace: "ns"},
		Data:       map[string][]byte{secretKeyPassword: []byte("new")},
	}
	ing := ingressWithAnnotations("ns", map[string]string{
		annotationPasswordSecretRef: "s",
		annotationPasswordHash:      "stale-hash",
	})
	r := newTestReconciler(t, ing, secret)
	calls := 0
	setFn := func(_ context.Context, _ string, v *string) error {
		calls++
		if v == nil || *v != "new" {
			t.Fatalf("expected new value, got %v", v)
		}
		return nil
	}
	if err := r.reconcileSecretBackedAuth(context.Background(), ing, "1",
		annotationPasswordSecretRef, annotationPasswordHash, secretKeyPassword, "password", setFn); err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatalf("expected 1 set call, got %d", calls)
	}
	if ing.Annotations[annotationPasswordHash] != hashSecretValue("1", "new") {
		t.Fatalf("hash not updated")
	}
}

func TestReconcileSecretBackedAuth_AnnotRemoved_HashPresent_Clears(t *testing.T) {
	ing := ingressWithAnnotations("ns", map[string]string{
		annotationPasswordHash: "some-hash",
	})
	r := newTestReconciler(t, ing)
	var clearedWith *string = &[]string{"sentinel"}[0]
	setFn := func(_ context.Context, _ string, v *string) error {
		clearedWith = v
		return nil
	}
	if err := r.reconcileSecretBackedAuth(context.Background(), ing, "1",
		annotationPasswordSecretRef, annotationPasswordHash, secretKeyPassword, "password", setFn); err != nil {
		t.Fatal(err)
	}
	if clearedWith != nil {
		t.Fatalf("expected set called with nil (clear), got %v", clearedWith)
	}
	if _, has := ing.Annotations[annotationPasswordHash]; has {
		t.Fatal("hash should be removed")
	}
}

func TestReconcileSecretBackedAuth_SecretMissing(t *testing.T) {
	ing := ingressWithAnnotations("ns", map[string]string{annotationPasswordSecretRef: "missing"})
	r := newTestReconciler(t, ing)
	setFn := func(_ context.Context, _ string, _ *string) error {
		t.Fatal("set should not be called when secret missing")
		return nil
	}
	err := r.reconcileSecretBackedAuth(context.Background(), ing, "1",
		annotationPasswordSecretRef, annotationPasswordHash, secretKeyPassword, "password", setFn)
	if !errors.Is(err, errSecretNotFound) {
		t.Fatalf("expected errSecretNotFound, got %v", err)
	}
}

func TestReconcileSecretBackedAuth_SecretKeyMissing(t *testing.T) {
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "s", Namespace: "ns"},
		Data:       map[string][]byte{"other-key": []byte("x")},
	}
	ing := ingressWithAnnotations("ns", map[string]string{annotationPasswordSecretRef: "s"})
	r := newTestReconciler(t, ing, secret)
	setFn := func(_ context.Context, _ string, _ *string) error {
		t.Fatal("set should not be called when secret key missing")
		return nil
	}
	err := r.reconcileSecretBackedAuth(context.Background(), ing, "1",
		annotationPasswordSecretRef, annotationPasswordHash, secretKeyPassword, "password", setFn)
	if !errors.Is(err, errSecretKeyMissing) {
		t.Fatalf("expected errSecretKeyMissing, got %v", err)
	}
}

func TestReconcileSecretBackedAuth_NotImplemented_Tolerated(t *testing.T) {
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "s", Namespace: "ns"},
		Data:       map[string][]byte{secretKeyPassword: []byte("hunter2!")},
	}
	ing := ingressWithAnnotations("ns", map[string]string{annotationPasswordSecretRef: "s"})
	r := newTestReconciler(t, ing, secret)
	setFn := func(_ context.Context, _ string, _ *string) error {
		return &pangolin.NotImplementedError{Message: "404"}
	}
	if err := r.reconcileSecretBackedAuth(context.Background(), ing, "1",
		annotationPasswordSecretRef, annotationPasswordHash, secretKeyPassword, "password", setFn); err != nil {
		t.Fatalf("404 should be tolerated, got %v", err)
	}
	if _, has := ing.Annotations[annotationPasswordHash]; has {
		t.Fatal("hash should not be written when endpoint returned 404")
	}
}

// ---------------------------------------------------------------------------
// Whitelist/roles/users tests against an httptest server backing a real
// Pangolin client — proves diff-vs-current and 404 tolerance through the
// whole client stack.

func newPangolinHTTPClient(t *testing.T, handler http.HandlerFunc) (*pangolin.Client, *httptest.Server) {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	return pangolin.NewClient(srv.URL, "test-api-key", "test-org"), srv
}

func TestReconcileWhitelist_NoChangeWhenEqual(t *testing.T) {
	setCalls := 0
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(`{"data":{"whitelist":[{"email":"a@x.com"},{"email":"b@x.com"}]}}`))
		case http.MethodPost:
			setCalls++
			w.Write([]byte(`{"data":{}}`))
		}
	})
	client, _ := newPangolinHTTPClient(t, handler)

	ing := ingressWithAnnotations("ns", map[string]string{annotationEmailWhitelist: `["b@x.com","a@x.com"]`})
	r := newTestReconciler(t, ing)
	r.PangolinClient = client

	if err := r.reconcileWhitelist(context.Background(), ing, "1"); err != nil {
		t.Fatal(err)
	}
	if setCalls != 0 {
		t.Fatalf("expected no POST when sets are equal, got %d", setCalls)
	}
}

func TestReconcileWhitelist_PostsOnChange(t *testing.T) {
	setBodyEmails := 0
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			w.Write([]byte(`{"data":{"whitelist":[{"email":"old@x.com"}]}}`))
		case http.MethodPost:
			setBodyEmails++
			w.Write([]byte(`{"data":{}}`))
		}
	})
	client, _ := newPangolinHTTPClient(t, handler)

	ing := ingressWithAnnotations("ns", map[string]string{annotationEmailWhitelist: `["new@x.com"]`})
	r := newTestReconciler(t, ing)
	r.PangolinClient = client

	if err := r.reconcileWhitelist(context.Background(), ing, "1"); err != nil {
		t.Fatal(err)
	}
	if setBodyEmails != 1 {
		t.Fatalf("expected 1 POST, got %d", setBodyEmails)
	}
}

func TestReconcileRoles_PostsOnChange(t *testing.T) {
	posted := false
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			w.Write([]byte(`{"data":{"roles":[{"roleId":1}]}}`))
		case http.MethodPost:
			posted = true
			w.Write([]byte(`{"data":{}}`))
		}
	})
	client, _ := newPangolinHTTPClient(t, handler)

	ing := ingressWithAnnotations("ns", map[string]string{annotationRoleIDs: `[1,2]`})
	r := newTestReconciler(t, ing)
	r.PangolinClient = client

	if err := r.reconcileRoles(context.Background(), ing, "1"); err != nil {
		t.Fatal(err)
	}
	if !posted {
		t.Fatal("expected POST when role sets differ")
	}
}

func TestReconcileUsers_NoChangeWhenEqual(t *testing.T) {
	postCalls := 0
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			w.Write([]byte(`{"data":{"users":[{"userId":"u1"},{"userId":"u2"}]}}`))
		case http.MethodPost:
			postCalls++
			w.Write([]byte(`{"data":{}}`))
		}
	})
	client, _ := newPangolinHTTPClient(t, handler)

	ing := ingressWithAnnotations("ns", map[string]string{annotationUserIDs: `["u2","u1"]`})
	r := newTestReconciler(t, ing)
	r.PangolinClient = client

	if err := r.reconcileUsers(context.Background(), ing, "1"); err != nil {
		t.Fatal(err)
	}
	if postCalls != 0 {
		t.Fatalf("expected no POST when user sets equal, got %d", postCalls)
	}
}

func TestReconcileWhitelist_404Tolerated(t *testing.T) {
	handler := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "not found", http.StatusNotFound)
	})
	client, _ := newPangolinHTTPClient(t, handler)

	ing := ingressWithAnnotations("ns", map[string]string{annotationEmailWhitelist: `["a@x.com"]`})
	r := newTestReconciler(t, ing)
	r.PangolinClient = client

	if err := r.reconcileWhitelist(context.Background(), ing, "1"); err != nil {
		t.Fatalf("404 should be tolerated, got %v", err)
	}
}

func TestPangolinClient_404MapsToNotImplemented(t *testing.T) {
	handler := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "not found", http.StatusNotFound)
	})
	client, _ := newPangolinHTTPClient(t, handler)

	err := client.SetResourcePassword(context.Background(), "1", nil)
	if !pangolin.IsNotImplemented(err) {
		t.Fatalf("expected NotImplementedError, got %T: %v", err, err)
	}
}
