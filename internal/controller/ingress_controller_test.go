package controller

import (
	"context"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	"github.com/vinzenz/pangolin-ingress-controller/internal/pangolin"
)

// fakePangolinIngressAPI serves the endpoints one Ingress reconcile touches:
// the domain list that resolves the host, the resource create, the target
// list and create, and the site lookup that fills in the status address.
//
// It exists because the reconciler talks to a real *pangolin.Client. Handing
// the test no Pangolin at all is what made it fail from the first commit: the
// reconcile could not get past client initialization, so nothing below that
// line was ever exercised.
var resourceByID = regexp.MustCompile(`^/v1/resource/\d+$`)

func fakePangolinIngressAPI(t *testing.T) *pangolin.Client {
	t.Helper()

	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		path := r.URL.Path

		switch {
		case strings.HasSuffix(path, "/domains"):
			// example.com is the host on the fixture Ingress.
			_, _ = w.Write([]byte(`{"data":{"domains":[{"domainId":"dom-1","baseDomain":"example.com"}]}}`))

		case strings.HasSuffix(path, "/resource") && r.Method == http.MethodPut:
			_, _ = w.Write([]byte(`{"data":{"resourceId":1,"name":"test","fullDomain":"example.com"}}`))

		case resourceByID.MatchString(path):
			// GET reads the resource back; POST applies settings to it.
			_, _ = w.Write([]byte(`{"data":{"resourceId":1,"name":"test","fullDomain":"example.com"}}`))

		case strings.HasSuffix(path, "/targets") && r.Method == http.MethodGet:
			_, _ = w.Write([]byte(`{"data":{"targets":[]}}`))

		case strings.HasSuffix(path, "/target") && r.Method == http.MethodPut:
			_, _ = w.Write([]byte(`{"data":{"targetId":1}}`))

		case strings.Contains(path, "/site/"):
			_, _ = w.Write([]byte(`{"data":{"siteId":1,"niceId":"test-site","name":"test","proxyIp":"203.0.113.10"}}`))

		default:
			// Anything unhandled is a route this test does not know about;
			// failing loudly beats a silent empty body that decodes to zero
			// values and makes the reconcile look like it worked.
			t.Errorf("unexpected Pangolin call: %s %s", r.Method, path)
			w.WriteHeader(http.StatusNotImplemented)
			_, _ = w.Write([]byte(`{"data":null}`))
		}
	})

	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	return pangolin.NewClient(srv.URL, "test-api-key", "test-org")
}

func TestIngressReconciler_Reconcile(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = clientgoscheme.AddToScheme(scheme)

	ingressClassName := "pangolin"
	pathTypePrefix := networkingv1.PathTypePrefix

	tests := []struct {
		name            string
		ingress         *networkingv1.Ingress
		service         *corev1.Service
		expectedError   bool
		shouldReconcile bool
	}{
		{
			name: "Valid ingress with pangolin class",
			ingress: &networkingv1.Ingress{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-ingress",
					Namespace: "default",
				},
				Spec: networkingv1.IngressSpec{
					IngressClassName: &ingressClassName,
					Rules: []networkingv1.IngressRule{
						{
							Host: "example.com",
							IngressRuleValue: networkingv1.IngressRuleValue{
								HTTP: &networkingv1.HTTPIngressRuleValue{
									Paths: []networkingv1.HTTPIngressPath{
										{
											Path:     "/",
											PathType: &pathTypePrefix,
											Backend: networkingv1.IngressBackend{
												Service: &networkingv1.IngressServiceBackend{
													Name: "test-service",
													Port: networkingv1.ServiceBackendPort{
														Number: 80,
													},
												},
											},
										},
									},
								},
							},
						},
					},
				},
			},
			service: &corev1.Service{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-service",
					Namespace: "default",
				},
				Spec: corev1.ServiceSpec{
					Ports: []corev1.ServicePort{
						{
							Port: 80,
						},
					},
				},
			},
			expectedError:   false,
			shouldReconcile: true,
		},
		{
			name: "Ingress with different class",
			ingress: &networkingv1.Ingress{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "other-ingress",
					Namespace: "default",
				},
				Spec: networkingv1.IngressSpec{
					IngressClassName: func() *string { s := "nginx"; return &s }(),
				},
			},
			expectedError:   false,
			shouldReconcile: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			objs := []runtime.Object{tt.ingress}
			if tt.service != nil {
				objs = append(objs, tt.service)
			}

			fakeClient := fake.NewClientBuilder().
				WithScheme(scheme).
				WithRuntimeObjects(objs...).
				WithStatusSubresource(&networkingv1.Ingress{}).
				Build()

			reconciler := &IngressReconciler{
				Client:         fakeClient,
				Scheme:         scheme,
				IngressClass:   "pangolin",
				ResourcePrefix: "pangolin-controller",
				OrgID:          "test-org",
				SiteNiceID:     "test-site",
			}

			// An Ingress belonging to another controller must reconcile
			// without Pangolin credentials at all, so that case deliberately
			// leaves PangolinClient nil.
			if tt.shouldReconcile {
				reconciler.setPangolinClient(fakePangolinIngressAPI(t))
			}

			req := ctrl.Request{
				NamespacedName: types.NamespacedName{
					Name:      tt.ingress.Name,
					Namespace: tt.ingress.Namespace,
				},
			}

			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()

			result, err := reconciler.Reconcile(ctx, req)

			if tt.expectedError && err == nil {
				t.Errorf("Expected error but got none")
			}
			if !tt.expectedError && err != nil {
				t.Errorf("Unexpected error: %v", err)
			}

			if result.Requeue {
				t.Errorf("Unexpected requeue")
			}

			// Assert what the reconcile actually did. Without this the test
			// passes on a reconcile that returned early and touched nothing,
			// which is how it read for its whole broken life.
			got := &networkingv1.Ingress{}
			if err := fakeClient.Get(ctx, req.NamespacedName, got); err != nil {
				t.Fatalf("failed to read back the Ingress: %v", err)
			}

			resourceID, hasResourceID := got.Annotations[annotationResourceID]
			hasFinalizer := controllerutil.ContainsFinalizer(got, pangolinFinalizerName)

			if !tt.shouldReconcile {
				if hasResourceID {
					t.Errorf("an Ingress of another class was programmed in Pangolin (resource-id %q)", resourceID)
				}
				if hasFinalizer {
					t.Error("an Ingress of another class was given the finalizer")
				}
				return
			}

			if !hasResourceID {
				t.Errorf("no %s annotation was recorded", annotationResourceID)
			} else if resourceID != "1" {
				t.Errorf("resource-id = %q want \"1\"", resourceID)
			}
			if !hasFinalizer {
				t.Error("no finalizer was added to a managed Ingress")
			}

			// The site reports a proxyIp, so status carries it rather than the
			// host fallback.
			lb := got.Status.LoadBalancer.Ingress
			if len(lb) != 1 {
				t.Fatalf("status.loadBalancer.ingress = %v want one entry", lb)
			}
			if lb[0].IP != "203.0.113.10" {
				t.Errorf("status ip = %q want the site proxyIp", lb[0].IP)
			}
		})
	}
}

func TestIngressReconciler_isManaged(t *testing.T) {
	tests := []struct {
		name     string
		ingress  *networkingv1.Ingress
		expected bool
	}{
		{
			name: "Managed via IngressClassName",
			ingress: &networkingv1.Ingress{
				Spec: networkingv1.IngressSpec{
					IngressClassName: func() *string { s := "pangolin"; return &s }(),
				},
			},
			expected: true,
		},
		{
			name: "Managed via annotation",
			ingress: &networkingv1.Ingress{
				ObjectMeta: metav1.ObjectMeta{
					Annotations: map[string]string{
						"kubernetes.io/ingress.class": "pangolin",
					},
				},
			},
			expected: true,
		},
		{
			name: "Not managed",
			ingress: &networkingv1.Ingress{
				Spec: networkingv1.IngressSpec{
					IngressClassName: func() *string { s := "nginx"; return &s }(),
				},
			},
			expected: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			reconciler := &IngressReconciler{
				IngressClass: "pangolin",
			}

			result := reconciler.isManaged(tt.ingress)
			if result != tt.expected {
				t.Errorf("Expected %v but got %v", tt.expected, result)
			}
		})
	}
}
