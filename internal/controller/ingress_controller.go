package controller

import (
	"context"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/predicate"

	"github.com/vinzenz/pangolin-ingress-controller/internal/pangolin"
)

const (
	pangolinFinalizerName = "pangolin.ingress.k8s.io/finalizer"
	annotationResourceID  = "pangolin.ingress.k8s.io/resource-id"
)

// IngressReconciler reconciles an Ingress object
type IngressReconciler struct {
	client.Client
	Scheme          *runtime.Scheme
	IngressClass    string
	PangolinClient  *pangolin.Client
	PangolinBaseURL string
	APIKeySecret    string
	APIKeyNamespace string
}

//+kubebuilder:rbac:groups=networking.k8s.io,resources=ingresses,verbs=get;list;watch;update;patch
//+kubebuilder:rbac:groups=networking.k8s.io,resources=ingresses/status,verbs=get;update;patch
//+kubebuilder:rbac:groups=networking.k8s.io,resources=ingresses/finalizers,verbs=update
//+kubebuilder:rbac:groups="",resources=services,verbs=get;list;watch
//+kubebuilder:rbac:groups="",resources=endpoints,verbs=get;list;watch
//+kubebuilder:rbac:groups="",resources=secrets,verbs=get;list;watch

// Reconcile is part of the main kubernetes reconciliation loop which aims to
// move the current state of the cluster closer to the desired state.
func (r *IngressReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := log.FromContext(ctx)

	// Initialize Pangolin client if needed
	if r.PangolinClient == nil {
		if err := r.initPangolinClient(ctx); err != nil {
			log.Error(err, "Failed to initialize Pangolin client")
			return ctrl.Result{}, err
		}
	}

	// Fetch the Ingress instance
	ingress := &networkingv1.Ingress{}
	err := r.Get(ctx, req.NamespacedName, ingress)
	if err != nil {
		if errors.IsNotFound(err) {
			// Ingress not found, could have been deleted
			log.Info("Ingress resource not found. Ignoring since object must be deleted")
			return ctrl.Result{}, nil
		}
		// Error reading the object - requeue the request
		log.Error(err, "Failed to get Ingress")
		return ctrl.Result{}, err
	}

	// Check if this ingress is for our ingress class
	if !r.isManaged(ingress) {
		log.V(1).Info("Ingress not managed by this controller", "ingressClass", r.IngressClass)
		return ctrl.Result{}, nil
	}

	log.Info("Reconciling Ingress", "name", ingress.Name, "namespace", ingress.Namespace)

	// Handle deletion
	if !ingress.DeletionTimestamp.IsZero() {
		if controllerutil.ContainsFinalizer(ingress, pangolinFinalizerName) {
			// Delete resources from Pangolin
			if err := r.deletePangolinResources(ctx, ingress); err != nil {
				log.Error(err, "Failed to delete Pangolin resources")
				return ctrl.Result{}, err
			}

			// Remove finalizer
			controllerutil.RemoveFinalizer(ingress, pangolinFinalizerName)
			if err := r.Update(ctx, ingress); err != nil {
				return ctrl.Result{}, err
			}
		}
		return ctrl.Result{}, nil
	}

	// Add finalizer if not present
	if !controllerutil.ContainsFinalizer(ingress, pangolinFinalizerName) {
		controllerutil.AddFinalizer(ingress, pangolinFinalizerName)
		if err := r.Update(ctx, ingress); err != nil {
			return ctrl.Result{}, err
		}
	}

	// Process ingress rules and create/update Pangolin resources
	if err := r.processIngressRules(ctx, ingress); err != nil {
		log.Error(err, "Failed to process ingress rules")
		return ctrl.Result{}, err
	}

	// Update ingress status
	if err := r.updateIngressStatus(ctx, ingress); err != nil {
		log.Error(err, "Failed to update ingress status")
		return ctrl.Result{}, err
	}

	log.Info("Successfully reconciled Ingress", "name", ingress.Name)
	return ctrl.Result{}, nil
}

// isManaged checks if the ingress should be managed by this controller
func (r *IngressReconciler) isManaged(ingress *networkingv1.Ingress) bool {
	// Check IngressClassName field (newer API)
	if ingress.Spec.IngressClassName != nil && *ingress.Spec.IngressClassName == r.IngressClass {
		return true
	}

	// Check annotation (legacy support)
	if class, ok := ingress.Annotations["kubernetes.io/ingress.class"]; ok && class == r.IngressClass {
		return true
	}

	return false
}

// processIngressRules processes the rules in the ingress specification and creates Pangolin resources
func (r *IngressReconciler) processIngressRules(ctx context.Context, ingress *networkingv1.Ingress) error {
	log := log.FromContext(ctx)

	// Process each rule and create Pangolin resources
	for _, rule := range ingress.Spec.Rules {
		host := rule.Host
		if host == "" {
			log.Info("Skipping rule without host")
			continue
		}

		if rule.HTTP != nil {
			for _, path := range rule.HTTP.Paths {
				// Get the backend service
				serviceName := path.Backend.Service.Name
				service := &corev1.Service{}
				err := r.Get(ctx, types.NamespacedName{
					Name:      serviceName,
					Namespace: ingress.Namespace,
				}, service)
				if err != nil {
					log.Error(err, "Failed to get backend service", "service", serviceName)
					return err
				}

				// Determine service port
				var servicePort int32
				if path.Backend.Service.Port.Number != 0 {
					servicePort = path.Backend.Service.Port.Number
				} else {
					// Find port by name
					for _, port := range service.Spec.Ports {
						if port.Name == path.Backend.Service.Port.Name {
							servicePort = port.Port
							break
						}
					}
				}

				if servicePort == 0 {
					return fmt.Errorf("could not determine service port for service %s", serviceName)
				}

				log.Info("Processing ingress rule",
					"host", host,
					"path", path.Path,
					"pathType", *path.PathType,
					"service", serviceName,
					"servicePort", servicePort,
				)

				// Create or update Pangolin resource
				if err := r.createOrUpdatePangolinResource(ctx, ingress, host, path, serviceName, servicePort); err != nil {
					log.Error(err, "Failed to create/update Pangolin resource")
					return err
				}
			}
		}
	}

	return nil
}

// updateIngressStatus updates the status of the ingress with load balancer information
func (r *IngressReconciler) updateIngressStatus(ctx context.Context, ingress *networkingv1.Ingress) error {
	log := log.FromContext(ctx)

	// Get the resource ID from annotations
	resourceID := ingress.Annotations[annotationResourceID]
	if resourceID == "" {
		log.V(1).Info("No resource ID found, skipping status update")
		return nil
	}

	// Get the resource from Pangolin to retrieve site information
	resource, err := r.PangolinClient.GetResource(ctx, resourceID)
	if err != nil {
		log.Error(err, "Failed to get Pangolin resource", "resourceID", resourceID)
		return err
	}

	// Get proxy IP from site information
	var proxyIP string
	if resource.SiteID != "" {
		site, err := r.PangolinClient.GetSite(ctx, resource.SiteID)
		if err != nil {
			log.Error(err, "Failed to get site information", "siteID", resource.SiteID)
			// Continue with empty IP rather than failing completely
		} else {
			proxyIP = site.ProxyIP
		}
	}

	// If no site ID or failed to get site, try to get default site
	if proxyIP == "" {
		sites, err := r.PangolinClient.ListSites(ctx)
		if err != nil {
			log.Error(err, "Failed to list sites")
			return err
		}
		// Use the first enabled site
		for _, site := range sites {
			if site.Enabled {
				proxyIP = site.ProxyIP
				break
			}
		}
	}

	if proxyIP == "" {
		log.Info("No proxy IP available, skipping status update")
		return nil
	}

	// Check if status needs updating
	needsUpdate := false
	if len(ingress.Status.LoadBalancer.Ingress) == 0 {
		needsUpdate = true
	} else if len(ingress.Status.LoadBalancer.Ingress) > 0 && ingress.Status.LoadBalancer.Ingress[0].IP != proxyIP {
		needsUpdate = true
	}

	if needsUpdate {
		// Update the status with actual Pangolin proxy IP
		ingress.Status.LoadBalancer.Ingress = []networkingv1.IngressLoadBalancerIngress{
			{
				IP: proxyIP,
			},
		}

		if err := r.Status().Update(ctx, ingress); err != nil {
			log.Error(err, "Failed to update Ingress status")
			return err
		}
		log.Info("Updated Ingress status with Pangolin proxy IP", "name", ingress.Name, "proxyIP", proxyIP)
	}

	return nil
}

// initPangolinClient initializes the Pangolin API client with API key from secret
func (r *IngressReconciler) initPangolinClient(ctx context.Context) error {
	log := log.FromContext(ctx)

	// Get API key from secret
	secret := &corev1.Secret{}
	err := r.Get(ctx, types.NamespacedName{
		Name:      r.APIKeySecret,
		Namespace: r.APIKeyNamespace,
	}, secret)
	if err != nil {
		return fmt.Errorf("failed to get API key secret: %w", err)
	}

	apiKey, ok := secret.Data["api-key"]
	if !ok {
		return fmt.Errorf("api-key not found in secret %s/%s", r.APIKeyNamespace, r.APIKeySecret)
	}

	r.PangolinClient = pangolin.NewClient(r.PangolinBaseURL, string(apiKey))
	log.Info("Initialized Pangolin client", "baseURL", r.PangolinBaseURL)

	return nil
}

// createOrUpdatePangolinResource creates or updates a Pangolin resource for an ingress rule
func (r *IngressReconciler) createOrUpdatePangolinResource(ctx context.Context, ingress *networkingv1.Ingress, host string, path networkingv1.HTTPIngressPath, serviceName string, servicePort int32) error {
	log := log.FromContext(ctx)

	// Parse host into subdomain and domain
	// For simplicity, assume host format is subdomain.domain.tld
	// In production, you'd want more sophisticated parsing
	subdomain, domain := parseHost(host)

	// Create resource name
	resourceName := fmt.Sprintf("%s-%s-%s", ingress.Namespace, ingress.Name, subdomain)

	// Check if resource already exists (stored in annotation)
	resourceID := ingress.Annotations[annotationResourceID]

	// Prepare resource request
	resourceReq := &pangolin.CreateResourceRequest{
		Name:      resourceName,
		Subdomain: subdomain,
		Domain:    domain,
		Type:      "http",
		Enabled:   true,
		Metadata: map[string]string{
			"kubernetes.namespace": ingress.Namespace,
			"kubernetes.ingress":   ingress.Name,
			"kubernetes.host":      host,
		},
	}

	var resource *pangolin.Resource
	var err error

	if resourceID != "" {
		// Update existing resource
		resource, err = r.PangolinClient.UpdateResource(ctx, resourceID, resourceReq)
		if err != nil {
			log.Error(err, "Failed to update Pangolin resource", "resourceID", resourceID)
			return err
		}
		log.Info("Updated Pangolin resource", "resourceID", resourceID, "name", resourceName)
	} else {
		// Create new resource
		resource, err = r.PangolinClient.CreateResource(ctx, resourceReq)
		if err != nil {
			log.Error(err, "Failed to create Pangolin resource")
			return err
		}
		log.Info("Created Pangolin resource", "resourceID", resource.ID, "name", resourceName)

		// Store resource ID in annotation
		if ingress.Annotations == nil {
			ingress.Annotations = make(map[string]string)
		}
		ingress.Annotations[annotationResourceID] = resource.ID
		if err := r.Update(ctx, ingress); err != nil {
			return err
		}
	}

	// Create target for the service
	targetReq := &pangolin.CreateTargetRequest{
		ResourceID: resource.ID,
		Host:       fmt.Sprintf("%s.%s.svc.cluster.local", serviceName, ingress.Namespace),
		Port:       int(servicePort),
		Method:     "http",
		Weight:     100,
		Enabled:    true,
		Metadata: map[string]string{
			"kubernetes.service": serviceName,
			"kubernetes.port":    fmt.Sprintf("%d", servicePort),
		},
	}

	_, err = r.PangolinClient.CreateTarget(ctx, targetReq)
	if err != nil {
		log.Error(err, "Failed to create Pangolin target")
		return err
	}

	log.Info("Created Pangolin target", "service", serviceName, "port", servicePort)

	return nil
}

// deletePangolinResources deletes all Pangolin resources associated with an ingress
func (r *IngressReconciler) deletePangolinResources(ctx context.Context, ingress *networkingv1.Ingress) error {
	log := log.FromContext(ctx)

	resourceID := ingress.Annotations[annotationResourceID]
	if resourceID == "" {
		log.Info("No Pangolin resource ID found, skipping deletion")
		return nil
	}

	// Delete the resource (targets will be deleted automatically)
	if err := r.PangolinClient.DeleteResource(ctx, resourceID); err != nil {
		log.Error(err, "Failed to delete Pangolin resource", "resourceID", resourceID)
		return err
	}

	log.Info("Deleted Pangolin resource", "resourceID", resourceID)
	return nil
}

// parseHost parses a hostname into subdomain and domain
func parseHost(host string) (subdomain, domain string) {
	// Simple parsing: assume format is subdomain.domain.tld
	// For production, use a proper domain parsing library
	parts := []rune(host)
	firstDot := -1
	for i, r := range parts {
		if r == '.' {
			firstDot = i
			break
		}
	}

	if firstDot == -1 {
		return host, ""
	}

	return string(parts[:firstDot]), string(parts[firstDot+1:])
}

// SetupWithManager sets up the controller with the Manager
func (r *IngressReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&networkingv1.Ingress{}).
		WithEventFilter(predicate.GenerationChangedPredicate{}).
		Complete(r)
}
