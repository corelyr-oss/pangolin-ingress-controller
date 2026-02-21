package controller

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"sync"

	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/predicate"

	"github.com/vinzenz/pangolin-ingress-controller/internal/pangolin"
)

const (
	pangolinFinalizerName = "pangolin.ingress.k8s.io/finalizer"
	annotationResourceID  = "pangolin.ingress.k8s.io/resource-id"

	// SSO / access control annotations
	annotationSSO                   = "pangolin.ingress.k8s.io/sso"
	annotationSSL                   = "pangolin.ingress.k8s.io/ssl"
	annotationBlockAccess           = "pangolin.ingress.k8s.io/block-access"
	annotationEmailWhitelistEnabled = "pangolin.ingress.k8s.io/email-whitelist-enabled"
	annotationApplyRules            = "pangolin.ingress.k8s.io/apply-rules"

	// Proxy settings annotations
	annotationStickySession = "pangolin.ingress.k8s.io/sticky-session"
	annotationTLSServerName = "pangolin.ingress.k8s.io/tls-server-name"
	annotationSetHostHeader = "pangolin.ingress.k8s.io/set-host-header"
	annotationHeaders       = "pangolin.ingress.k8s.io/headers"
	annotationPostAuthPath  = "pangolin.ingress.k8s.io/post-auth-path"

	// Resource enabled annotation
	annotationEnabled = "pangolin.ingress.k8s.io/enabled"

	// Health check annotations
	annotationHCEnabled           = "pangolin.ingress.k8s.io/healthcheck-enabled"
	annotationHCPath              = "pangolin.ingress.k8s.io/healthcheck-path"
	annotationHCScheme            = "pangolin.ingress.k8s.io/healthcheck-scheme"
	annotationHCMode              = "pangolin.ingress.k8s.io/healthcheck-mode"
	annotationHCHostname          = "pangolin.ingress.k8s.io/healthcheck-hostname"
	annotationHCPort              = "pangolin.ingress.k8s.io/healthcheck-port"
	annotationHCInterval          = "pangolin.ingress.k8s.io/healthcheck-interval"
	annotationHCUnhealthyInterval = "pangolin.ingress.k8s.io/healthcheck-unhealthy-interval"
	annotationHCTimeout           = "pangolin.ingress.k8s.io/healthcheck-timeout"
	annotationHCHeaders           = "pangolin.ingress.k8s.io/healthcheck-headers"
	annotationHCFollowRedirects   = "pangolin.ingress.k8s.io/healthcheck-follow-redirects"
	annotationHCMethod            = "pangolin.ingress.k8s.io/healthcheck-method"
	annotationHCStatus            = "pangolin.ingress.k8s.io/healthcheck-status"
	annotationHCTLSServerName     = "pangolin.ingress.k8s.io/healthcheck-tls-server-name"
)

// IngressReconciler reconciles an Ingress object
type IngressReconciler struct {
	client.Client
	Scheme          *runtime.Scheme
	IngressClass    string
	ResourcePrefix  string
	PangolinClient  *pangolin.Client
	PangolinBaseURL string
	APIKeySecret    string
	APIKeyNamespace string
	OrgID           string
	SiteNiceID      string
	domainMu        sync.RWMutex
	domainMap       map[string]string
	siteMu          sync.RWMutex
	siteCache       *pangolin.Site
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

	resourceID := ingress.Annotations[annotationResourceID]
	if resourceID == "" {
		log.V(1).Info("No resource ID found, skipping status update")
		return nil
	}

	if _, err := r.PangolinClient.GetResource(ctx, resourceID); err != nil {
		log.Error(err, "Failed to get Pangolin resource", "resourceID", resourceID)
		return err
	}

	site, err := r.getSiteInfo(ctx)
	if err != nil {
		log.Error(err, "Failed to fetch site info for status update", "siteNiceID", r.SiteNiceID)
		return err
	}

	proxyIP := site.ProxyIP
	if proxyIP == "" {
		log.Info("Configured site has no proxy IP, skipping status update", "site", site.NiceID)
		return nil
	}

	needsUpdate := false
	if len(ingress.Status.LoadBalancer.Ingress) == 0 {
		needsUpdate = true
	} else if ingress.Status.LoadBalancer.Ingress[0].IP != proxyIP {
		needsUpdate = true
	}

	if needsUpdate {
		ingress.Status.LoadBalancer.Ingress = []networkingv1.IngressLoadBalancerIngress{{IP: proxyIP}}
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

	r.PangolinClient = pangolin.NewClient(r.PangolinBaseURL, string(apiKey), r.OrgID)
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

	// Create resource name with configurable prefix
	prefix := r.ResourcePrefix
	if prefix == "" {
		prefix = "pangolin-controller"
	}
	resourceName := fmt.Sprintf("%s-%s-%s-%s", prefix, ingress.Namespace, ingress.Name, subdomain)

	// Check if resource already exists (stored in annotation)
	resourceID := ingress.Annotations[annotationResourceID]

	var err error

	if domain == "" {
		return fmt.Errorf("host %s is missing a registrable domain", host)
	}

	domainID, err := r.resolveDomainID(ctx, domain)
	if err != nil {
		log.Error(err, "Failed to resolve domain ID", "domain", domain)
		return err
	}

	// Parse annotations for proxy and access control settings
	annotations := ingress.Annotations
	stickySession := parseBoolAnnotation(annotations, annotationStickySession)
	postAuthPath := parseStringAnnotation(annotations, annotationPostAuthPath)

	resourceReq := &pangolin.CreateResourceRequest{
		Name:      resourceName,
		Subdomain: subdomain,
		HTTP:      true,
		Protocol:  "tcp",
		DomainID:  domainID,
	}
	if stickySession != nil && *stickySession {
		resourceReq.StickySession = true
	}
	if postAuthPath != nil {
		resourceReq.PostAuthPath = *postAuthPath
	}

	updateReq := &pangolin.UpdateResourceRequest{
		Name:                  resourceName,
		Subdomain:             subdomain,
		DomainID:              domainID,
		Enabled:               parseBoolAnnotation(annotations, annotationEnabled),
		SSO:                   parseBoolAnnotation(annotations, annotationSSO),
		SSL:                   parseBoolAnnotation(annotations, annotationSSL),
		BlockAccess:           parseBoolAnnotation(annotations, annotationBlockAccess),
		EmailWhitelistEnabled: parseBoolAnnotation(annotations, annotationEmailWhitelistEnabled),
		ApplyRules:            parseBoolAnnotation(annotations, annotationApplyRules),
		StickySession:         stickySession,
		TLSServerName:         parseStringAnnotation(annotations, annotationTLSServerName),
		SetHostHeader:         parseStringAnnotation(annotations, annotationSetHostHeader),
		PostAuthPath:          postAuthPath,
		Headers:               parseHeadersAnnotation(annotations, annotationHeaders),
	}

	var resource *pangolin.Resource

	if resourceID != "" {
		resource, err = r.PangolinClient.UpdateResource(ctx, resourceID, updateReq)
		if err != nil {
			log.Error(err, "Failed to update Pangolin resource", "resourceID", resourceID, "subdomain", subdomain, "domain", domain, "host", host)
			return fmt.Errorf("failed to update Pangolin resource %s: %w", resourceID, err)
		}
		log.Info("Updated Pangolin resource", "resourceID", resourceID, "name", resourceName)
	} else {
		// Create new resource
		resource, err = r.PangolinClient.CreateResource(ctx, resourceReq)
		if err != nil {
			log.Error(err, "Failed to create Pangolin resource", "subdomain", subdomain, "domain", domain, "host", host)
			return fmt.Errorf("failed to create Pangolin resource for host %s: %w", host, err)
		}
		log.Info("Created Pangolin resource", "resourceID", resource.ID, "name", resourceName)

		// Store resource ID in annotation
		if ingress.Annotations == nil {
			ingress.Annotations = make(map[string]string)
		}
		resourceID = strconv.Itoa(resource.ID)
		ingress.Annotations[annotationResourceID] = resourceID
		if err := r.Update(ctx, ingress); err != nil {
			return err
		}

		// Apply update settings (SSO, SSL, etc.) to the newly created resource
		resource, err = r.PangolinClient.UpdateResource(ctx, resourceID, updateReq)
		if err != nil {
			log.Error(err, "Failed to apply settings to new Pangolin resource", "resourceID", resourceID)
			return fmt.Errorf("failed to apply settings to new Pangolin resource %s: %w", resourceID, err)
		}
	}

	site, err := r.getSiteInfo(ctx)
	if err != nil {
		log.Error(err, "Failed to resolve site for target creation", "siteNiceID", r.SiteNiceID)
		return err
	}

	targetIP := fmt.Sprintf("%s.%s.svc.cluster.local", serviceName, ingress.Namespace)
	targetPort := int(servicePort)
	targetPath := path.Path
	if targetPath == "" {
		targetPath = "/"
	}

	// Check for existing targets to avoid duplicates on restarts
	existingTargets, err := r.PangolinClient.ListTargets(ctx, resourceID)
	if err != nil {
		log.Error(err, "Failed to list existing targets", "resourceID", resourceID)
		return fmt.Errorf("failed to list targets for resource %s: %w", resourceID, err)
	}

	// Look for a target that matches our site, IP, and port
	var existingTarget *pangolin.Target
	for i := range existingTargets {
		t := &existingTargets[i]
		if t.SiteID == site.ID && t.IP == targetIP && t.Port == targetPort {
			existingTarget = t
			break
		}
	}

	targetReq := &pangolin.CreateTargetRequest{
		SiteID:              site.ID,
		IP:                  targetIP,
		Method:              "http",
		Port:                targetPort,
		Enabled:             true,
		Path:                targetPath,
		PathMatchType:       pathTypeToMatch(path.PathType),
		HCEnabled:           parseBoolAnnotation(annotations, annotationHCEnabled),
		HCPath:              parseStringAnnotation(annotations, annotationHCPath),
		HCScheme:            parseStringAnnotation(annotations, annotationHCScheme),
		HCMode:              parseStringAnnotation(annotations, annotationHCMode),
		HCHostname:          parseStringAnnotation(annotations, annotationHCHostname),
		HCPort:              parseIntAnnotation(annotations, annotationHCPort),
		HCInterval:          parseIntAnnotation(annotations, annotationHCInterval),
		HCUnhealthyInterval: parseIntAnnotation(annotations, annotationHCUnhealthyInterval),
		HCTimeout:           parseIntAnnotation(annotations, annotationHCTimeout),
		HCHeaders:           parseHeadersAnnotation(annotations, annotationHCHeaders),
		HCFollowRedirects:   parseBoolAnnotation(annotations, annotationHCFollowRedirects),
		HCMethod:            parseStringAnnotation(annotations, annotationHCMethod),
		HCStatus:            parseIntAnnotation(annotations, annotationHCStatus),
		HCTLSServerName:     parseStringAnnotation(annotations, annotationHCTLSServerName),
	}

	var activeTargetID int
	if existingTarget != nil {
		// Target already exists — update it instead of creating a duplicate
		targetIDStr := strconv.Itoa(existingTarget.ID)
		_, err = r.PangolinClient.UpdateTarget(ctx, targetIDStr, targetReq)
		if err != nil {
			log.Error(err, "Failed to update Pangolin target", "targetID", targetIDStr, "resourceID", resourceID)
			return fmt.Errorf("failed to update Pangolin target %s: %w", targetIDStr, err)
		}
		activeTargetID = existingTarget.ID
		log.Info("Updated existing Pangolin target", "targetID", targetIDStr, "service", serviceName, "port", servicePort)
	} else {
		// No matching target — create a new one
		newTarget, createErr := r.PangolinClient.CreateTarget(ctx, resourceID, targetReq)
		if createErr != nil {
			log.Error(createErr, "Failed to create Pangolin target", "resourceID", resourceID, "service", serviceName, "port", servicePort)
			return fmt.Errorf("failed to create Pangolin target for service %s:%d: %w", serviceName, servicePort, createErr)
		}
		activeTargetID = newTarget.ID
		log.Info("Created Pangolin target", "targetID", newTarget.ID, "service", serviceName, "port", servicePort)
	}

	// Clean up stale targets that don't match the active one
	for _, t := range existingTargets {
		if t.ID == activeTargetID {
			continue
		}
		staleID := strconv.Itoa(t.ID)
		if delErr := r.PangolinClient.DeleteTarget(ctx, staleID); delErr != nil {
			log.Error(delErr, "Failed to delete stale Pangolin target", "targetID", staleID)
		} else {
			log.Info("Deleted stale Pangolin target", "targetID", staleID, "ip", t.IP, "port", t.Port)
		}
	}

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
	host = strings.TrimSpace(host)
	if host == "" {
		return "", ""
	}
	parts := strings.Split(host, ".")
	if len(parts) < 2 {
		return host, ""
	}
	domain = strings.Join(parts[len(parts)-2:], ".")
	if len(parts) == 2 {
		return "", domain
	}
	subdomain = strings.Join(parts[:len(parts)-2], ".")
	return subdomain, domain
}

func (r *IngressReconciler) getSiteInfo(ctx context.Context) (*pangolin.Site, error) {
	if r.SiteNiceID == "" {
		return nil, fmt.Errorf("pangolin site nice ID is not configured")
	}
	r.siteMu.RLock()
	if r.siteCache != nil {
		site := r.siteCache
		r.siteMu.RUnlock()
		return site, nil
	}
	r.siteMu.RUnlock()

	site, err := r.PangolinClient.GetSiteByNiceID(ctx, r.SiteNiceID)
	if err != nil {
		return nil, err
	}

	r.siteMu.Lock()
	r.siteCache = site
	r.siteMu.Unlock()

	return site, nil
}

func (r *IngressReconciler) resolveDomainID(ctx context.Context, baseDomain string) (string, error) {
	r.domainMu.RLock()
	if r.domainMap != nil {
		if id, ok := r.domainMap[baseDomain]; ok {
			r.domainMu.RUnlock()
			return id, nil
		}
	}
	r.domainMu.RUnlock()

	domains, err := r.PangolinClient.ListDomains(ctx)
	if err != nil {
		return "", fmt.Errorf("failed to list Pangolin domains: %w", err)
	}

	localMap := make(map[string]string, len(domains))
	for _, d := range domains {
		localMap[d.BaseDomain] = d.ID
	}

	r.domainMu.Lock()
	if r.domainMap == nil {
		r.domainMap = make(map[string]string, len(localMap))
	}
	for k, v := range localMap {
		r.domainMap[k] = v
	}
	resolved, ok := r.domainMap[baseDomain]
	r.domainMu.Unlock()
	if !ok {
		return "", fmt.Errorf("no Pangolin domain configured for %s", baseDomain)
	}
	return resolved, nil
}

func pathTypeToMatch(pt *networkingv1.PathType) string {
	if pt == nil {
		return "prefix"
	}
	switch *pt {
	case networkingv1.PathTypeExact:
		return "exact"
	case networkingv1.PathTypeImplementationSpecific:
		return "regex"
	default:
		return "prefix"
	}
}

// parseBoolAnnotation returns a *bool from an annotation value, or nil if not set.
func parseBoolAnnotation(annotations map[string]string, key string) *bool {
	v, ok := annotations[key]
	if !ok || v == "" {
		return nil
	}
	b, err := strconv.ParseBool(v)
	if err != nil {
		return nil
	}
	return &b
}

// parseStringAnnotation returns a *string from an annotation value, or nil if not set.
func parseStringAnnotation(annotations map[string]string, key string) *string {
	v, ok := annotations[key]
	if !ok {
		return nil
	}
	return &v
}

// parseIntAnnotation returns a *int from an annotation value, or nil if not set.
func parseIntAnnotation(annotations map[string]string, key string) *int {
	v, ok := annotations[key]
	if !ok || v == "" {
		return nil
	}
	i, err := strconv.Atoi(v)
	if err != nil {
		return nil
	}
	return &i
}

// parseHeadersAnnotation parses a JSON array of {"name":"...","value":"..."} objects from an annotation.
func parseHeadersAnnotation(annotations map[string]string, key string) []pangolin.Header {
	v, ok := annotations[key]
	if !ok || v == "" {
		return nil
	}
	var headers []pangolin.Header
	if err := json.Unmarshal([]byte(v), &headers); err != nil {
		return nil
	}
	return headers
}

// pangolinAnnotationChangedPredicate triggers reconciliation when any
// pangolin.ingress.k8s.io/* annotation changes EXCEPT the controller-managed
// resource-id annotation (which the controller itself writes).
type pangolinAnnotationChangedPredicate struct {
	predicate.Funcs
}

func (p pangolinAnnotationChangedPredicate) Update(e event.UpdateEvent) bool {
	if e.ObjectOld == nil || e.ObjectNew == nil {
		return false
	}
	oldAnn := e.ObjectOld.GetAnnotations()
	newAnn := e.ObjectNew.GetAnnotations()
	for key, newVal := range newAnn {
		if key == annotationResourceID {
			continue
		}
		if !strings.HasPrefix(key, "pangolin.ingress.k8s.io/") {
			continue
		}
		if oldAnn[key] != newVal {
			return true
		}
	}
	// Check for removed pangolin annotations
	for key := range oldAnn {
		if key == annotationResourceID {
			continue
		}
		if !strings.HasPrefix(key, "pangolin.ingress.k8s.io/") {
			continue
		}
		if _, exists := newAnn[key]; !exists {
			return true
		}
	}
	return false
}

// SetupWithManager sets up the controller with the Manager
func (r *IngressReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&networkingv1.Ingress{}).
		WithEventFilter(predicate.Or(
			predicate.GenerationChangedPredicate{},
			pangolinAnnotationChangedPredicate{},
		)).
		Complete(r)
}
