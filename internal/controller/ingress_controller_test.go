package controller

import (
	"context"
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
)

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
				Client:       fakeClient,
				Scheme:       scheme,
				IngressClass: "pangolin",
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
