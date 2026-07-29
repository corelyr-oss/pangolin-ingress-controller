package controller

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/net/publicsuffix"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
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
	annotationSkipToIdpID           = "pangolin.ingress.k8s.io/skip-to-idp-id"

	// Auth method annotations
	annotationEmailWhitelist    = "pangolin.ingress.k8s.io/email-whitelist"
	annotationPasswordSecretRef = "pangolin.ingress.k8s.io/password-secret-ref"
	annotationPincodeSecretRef  = "pangolin.ingress.k8s.io/pincode-secret-ref"
	annotationRoleIDs           = "pangolin.ingress.k8s.io/role-ids"
	annotationUserIDs           = "pangolin.ingress.k8s.io/user-ids"
	annotationPasswordHash      = "pangolin.ingress.k8s.io/password-hash"
	annotationPincodeHash       = "pangolin.ingress.k8s.io/pincode-hash"

	// Secret keys read from a referenced Kubernetes Secret
	secretKeyPassword = "password"
	secretKeyPincode  = "pincode"

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

// controllerManagedAnnotations are annotations the controller writes itself.
// Changes to these MUST NOT retrigger reconciliation, otherwise the controller
// will spin in a write-watch loop.
var controllerManagedAnnotations = map[string]struct{}{
	annotationResourceID:   {},
	annotationPasswordHash: {},
	annotationPincodeHash:  {},
}

var (
	errSecretNotFound   = errors.New("secret not found")
	errSecretKeyMissing = errors.New("secret key missing")

	// errDomainNotFound marks a host that matches no Pangolin domain even after
	// the domain list has been refreshed. It is an expected, operator-fixable
	// condition rather than a controller fault, so Reconcile requeues on it
	// instead of returning an error. Callers must wrap it with %w.
	errDomainNotFound = errors.New("no matching Pangolin domain")
)

// reasonDomainNotFound is the Event reason recorded on an Ingress whose host
// cannot be resolved to a Pangolin domain.
const reasonDomainNotFound = "DomainNotFound"

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
	Recorder        record.EventRecorder

	// DomainCacheRefreshInterval bounds how often the Pangolin domain list is
	// refetched after a host fails to resolve. Zero disables refresh-on-miss.
	DomainCacheRefreshInterval time.Duration

	domains   *domainCache
	siteMu    sync.RWMutex
	siteCache *pangolin.Site
}

//+kubebuilder:rbac:groups=networking.k8s.io,resources=ingresses,verbs=get;list;watch;update;patch
//+kubebuilder:rbac:groups=networking.k8s.io,resources=ingresses/status,verbs=get;update;patch
//+kubebuilder:rbac:groups=networking.k8s.io,resources=ingresses/finalizers,verbs=update
//+kubebuilder:rbac:groups="",resources=services,verbs=get;list;watch
//+kubebuilder:rbac:groups="",resources=endpoints,verbs=get;list;watch
//+kubebuilder:rbac:groups="",resources=secrets,verbs=get;list;watch
//+kubebuilder:rbac:groups="",resources=events,verbs=create;patch

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
		if apierrors.IsNotFound(err) {
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
		if errors.Is(err, errDomainNotFound) {
			// Expected and operator-fixable: the host is not a registered
			// Pangolin domain. Retry on a bounded cadence rather than letting
			// controller-runtime's exponential backoff climb toward minutes,
			// which would make recovery time after registering the domain
			// unpredictable. Logged at info — this is not a controller fault.
			requeueAfter := r.domainRequeueAfter()
			log.Info("Host does not match any Pangolin domain; will retry",
				"reason", err.Error(), "requeueAfter", requeueAfter)
			if r.Recorder != nil {
				r.Recorder.Event(ingress, corev1.EventTypeWarning, reasonDomainNotFound, err.Error())
			}
			return ctrl.Result{RequeueAfter: requeueAfter}, nil
		}
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

	// Build the desired LoadBalancer status entry.
	// Prefer the site's proxy IP; fall back to the first ingress rule hostname
	// so that ArgoCD (and similar tools) see the Ingress as healthy.
	var desired networkingv1.IngressLoadBalancerIngress
	proxyIP := site.ProxyIP
	if proxyIP != "" {
		desired.IP = proxyIP
	} else {
		// Use the first rule host as the hostname fallback
		for _, rule := range ingress.Spec.Rules {
			if rule.Host != "" {
				desired.Hostname = rule.Host
				break
			}
		}
		if desired.Hostname == "" {
			log.Info("Configured site has no proxy IP and ingress has no host rules, skipping status update", "site", site.NiceID)
			return nil
		}
	}

	needsUpdate := false
	if len(ingress.Status.LoadBalancer.Ingress) == 0 {
		needsUpdate = true
	} else {
		cur := ingress.Status.LoadBalancer.Ingress[0]
		if cur.IP != desired.IP || cur.Hostname != desired.Hostname {
			needsUpdate = true
		}
	}

	if needsUpdate {
		ingress.Status.LoadBalancer.Ingress = []networkingv1.IngressLoadBalancerIngress{desired}
		if err := r.Status().Update(ctx, ingress); err != nil {
			log.Error(err, "Failed to update Ingress status")
			return err
		}
		log.Info("Updated Ingress status with Pangolin address", "name", ingress.Name, "ip", desired.IP, "hostname", desired.Hostname)
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
	r.domains = newDomainCache(r.PangolinClient, r.DomainCacheRefreshInterval)
	log.Info("Initialized Pangolin client", "baseURL", r.PangolinBaseURL,
		"domainCacheRefreshInterval", r.DomainCacheRefreshInterval)

	return nil
}

// domainRequeueAfter is the retry delay for a host that matches no Pangolin
// domain. A small margin is added over the refresh interval so a requeue never
// lands fractionally before the cooldown expires, which would waste a cycle
// skipping the refetch it came back to perform.
func (r *IngressReconciler) domainRequeueAfter() time.Duration {
	interval := r.DomainCacheRefreshInterval
	if interval <= 0 {
		interval = defaultDomainCacheRefreshInterval
	}
	return interval + interval/10
}

// createOrUpdatePangolinResource creates or updates a Pangolin resource for an ingress rule
func (r *IngressReconciler) createOrUpdatePangolinResource(ctx context.Context, ingress *networkingv1.Ingress, host string, path networkingv1.HTTPIngressPath, serviceName string, servicePort int32) error {
	log := log.FromContext(ctx)

	// Resolve host against known Pangolin domains
	subdomain, domainID, err := r.resolveHostDomain(ctx, host)
	if err != nil {
		log.Error(err, "Failed to resolve host domain", "host", host)
		return err
	}

	// Create resource name with configurable prefix
	prefix := r.ResourcePrefix
	if prefix == "" {
		prefix = "pangolin-controller"
	}
	resourceName := fmt.Sprintf("%s-%s", prefix, host)

	// Check if resource already exists (stored in annotation)
	resourceID := ingress.Annotations[annotationResourceID]

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
		SkipToIdpID:           parseIntAnnotation(annotations, annotationSkipToIdpID),
	}

	var resource *pangolin.Resource

	if resourceID != "" {
		resource, err = r.PangolinClient.UpdateResource(ctx, resourceID, updateReq)
		if err != nil {
			log.Error(err, "Failed to update Pangolin resource", "resourceID", resourceID, "subdomain", subdomain, "domainID", domainID, "host", host)
			return fmt.Errorf("failed to update Pangolin resource %s: %w", resourceID, err)
		}
		log.Info("Updated Pangolin resource", "resourceID", resourceID, "name", resourceName)
	} else {
		// Create new resource
		resource, err = r.PangolinClient.CreateResource(ctx, resourceReq)
		if err != nil {
			if pangolin.IsConflict(err) {
				// Resource already exists in Pangolin — adopt it
				log.Info("Resource already exists, attempting to adopt", "host", host, "subdomain", subdomain)
				resource, err = r.findExistingResource(ctx, subdomain, domainID)
				if err != nil {
					return fmt.Errorf("failed to adopt existing Pangolin resource for host %s: %w", host, err)
				}
				log.Info("Adopted existing Pangolin resource", "resourceID", resource.ID, "name", resource.Name)
			} else {
				log.Error(err, "Failed to create Pangolin resource", "subdomain", subdomain, "domainID", domainID, "host", host)
				return fmt.Errorf("failed to create Pangolin resource for host %s: %w", host, err)
			}
		} else {
			log.Info("Created Pangolin resource", "resourceID", resource.ID, "name", resourceName)
		}

		// Store resource ID in annotation
		if ingress.Annotations == nil {
			ingress.Annotations = make(map[string]string)
		}
		resourceID = strconv.Itoa(resource.ID)
		ingress.Annotations[annotationResourceID] = resourceID
		if err := r.Update(ctx, ingress); err != nil {
			return err
		}

		// Apply update settings (SSO, SSL, etc.) to the resource
		resource, err = r.PangolinClient.UpdateResource(ctx, resourceID, updateReq)
		if err != nil {
			log.Error(err, "Failed to apply settings to Pangolin resource", "resourceID", resourceID)
			return fmt.Errorf("failed to apply settings to Pangolin resource %s: %w", resourceID, err)
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

	// Pangolin requires hcPath, hcHostname, hcPort, hcInterval, and hcMethod
	// to all be non-null for health checks to be pushed to Newt. When health
	// checks are enabled, fill in sensible defaults for any missing fields.
	if targetReq.HCEnabled != nil && *targetReq.HCEnabled {
		if targetReq.HCPath == nil {
			s := "/"
			targetReq.HCPath = &s
		}
		if targetReq.HCHostname == nil {
			targetReq.HCHostname = &targetIP
		}
		if targetReq.HCPort == nil {
			p := int(servicePort)
			targetReq.HCPort = &p
		}
		if targetReq.HCInterval == nil {
			i := 30
			targetReq.HCInterval = &i
		}
		if targetReq.HCMethod == nil {
			m := "GET"
			targetReq.HCMethod = &m
		}
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

	// Reconcile per-resource auth methods (password, pincode, whitelist, roles, users).
	if err := r.reconcileResourceAuth(ctx, ingress, resourceID); err != nil {
		return err
	}

	return nil
}

// getSecretValue fetches a single key from a Kubernetes Secret, returning
// errSecretNotFound or errSecretKeyMissing for the two distinguishable
// failure modes.
func (r *IngressReconciler) getSecretValue(ctx context.Context, namespace, name, key string) (string, error) {
	secret := &corev1.Secret{}
	if err := r.Get(ctx, types.NamespacedName{Namespace: namespace, Name: name}, secret); err != nil {
		if apierrors.IsNotFound(err) {
			return "", fmt.Errorf("%w: %s/%s", errSecretNotFound, namespace, name)
		}
		return "", fmt.Errorf("failed to get secret %s/%s: %w", namespace, name, err)
	}
	v, ok := secret.Data[key]
	if !ok {
		return "", fmt.Errorf("%w: secret %s/%s has no key %q", errSecretKeyMissing, namespace, name, key)
	}
	return string(v), nil
}

// hashSecretValue computes a stable change-detection hash over the resource ID
// (acts as a per-resource salt) and the secret value. Not intended as a
// password hash for security — just to detect when the user changed the value.
func hashSecretValue(resourceID, value string) string {
	h := sha256.Sum256([]byte(resourceID + ":" + value))
	return hex.EncodeToString(h[:])
}

// setManagedAnnotation writes a controller-managed annotation onto the Ingress
// and persists it. The annotation key must be in controllerManagedAnnotations
// so it doesn't re-trigger reconciliation.
func (r *IngressReconciler) setManagedAnnotation(ctx context.Context, ingress *networkingv1.Ingress, key, value string) error {
	if ingress.Annotations == nil {
		ingress.Annotations = map[string]string{}
	}
	if ingress.Annotations[key] == value {
		return nil
	}
	ingress.Annotations[key] = value
	return r.Update(ctx, ingress)
}

// clearManagedAnnotation removes a controller-managed annotation if present.
func (r *IngressReconciler) clearManagedAnnotation(ctx context.Context, ingress *networkingv1.Ingress, key string) error {
	if _, ok := ingress.Annotations[key]; !ok {
		return nil
	}
	delete(ingress.Annotations, key)
	return r.Update(ctx, ingress)
}

// reconcileResourceAuth reconciles the per-resource auth methods that live on
// separate Pangolin endpoints (password, pincode, email whitelist, role
// assignments, user assignments). 404/405 from any sub-endpoint is logged and
// the sub-step is skipped — older Pangolin instances may not have the route.
func (r *IngressReconciler) reconcileResourceAuth(ctx context.Context, ingress *networkingv1.Ingress, resourceID string) error {
	if resourceID == "" {
		return nil
	}
	if err := r.reconcilePassword(ctx, ingress, resourceID); err != nil {
		return err
	}
	if err := r.reconcilePincode(ctx, ingress, resourceID); err != nil {
		return err
	}
	if err := r.reconcileWhitelist(ctx, ingress, resourceID); err != nil {
		return err
	}
	if err := r.reconcileRoles(ctx, ingress, resourceID); err != nil {
		return err
	}
	if err := r.reconcileUsers(ctx, ingress, resourceID); err != nil {
		return err
	}
	return nil
}

func (r *IngressReconciler) reconcilePassword(ctx context.Context, ingress *networkingv1.Ingress, resourceID string) error {
	return r.reconcileSecretBackedAuth(
		ctx, ingress, resourceID,
		annotationPasswordSecretRef, annotationPasswordHash, secretKeyPassword,
		"password",
		r.PangolinClient.SetResourcePassword,
	)
}

func (r *IngressReconciler) reconcilePincode(ctx context.Context, ingress *networkingv1.Ingress, resourceID string) error {
	return r.reconcileSecretBackedAuth(
		ctx, ingress, resourceID,
		annotationPincodeSecretRef, annotationPincodeHash, secretKeyPincode,
		"pincode",
		r.PangolinClient.SetResourcePincode,
	)
}

// reconcileSecretBackedAuth implements the convergent state machine shared by
// password and pincode: annotation absent + hash absent → no-op; annotation
// present + hash matches → no-op; annotation present + hash stale or absent →
// set + write hash; annotation absent + hash present → clear + remove hash.
func (r *IngressReconciler) reconcileSecretBackedAuth(
	ctx context.Context,
	ingress *networkingv1.Ingress,
	resourceID string,
	refAnnotation, hashAnnotation, secretKey, label string,
	setFn func(context.Context, string, *string) error,
) error {
	log := log.FromContext(ctx).WithValues("authMethod", label, "resourceID", resourceID)
	annotations := ingress.Annotations
	refValue := annotations[refAnnotation]
	storedHash := annotations[hashAnnotation]

	if refValue == "" {
		if storedHash == "" {
			return nil
		}
		if err := setFn(ctx, resourceID, nil); err != nil {
			if pangolin.IsNotImplemented(err) {
				log.Info("Pangolin endpoint not available; skipping clear", "error", err)
				return nil
			}
			return fmt.Errorf("failed to clear %s: %w", label, err)
		}
		log.Info("Cleared resource " + label)
		return r.clearManagedAnnotation(ctx, ingress, hashAnnotation)
	}

	ns, name, ok := parseSecretRef(refValue, ingress.Namespace)
	if !ok {
		return fmt.Errorf("annotation %q has invalid Secret reference %q", refAnnotation, refValue)
	}
	value, err := r.getSecretValue(ctx, ns, name, secretKey)
	if err != nil {
		return err
	}
	desiredHash := hashSecretValue(resourceID, value)
	if desiredHash == storedHash {
		return nil
	}
	if err := setFn(ctx, resourceID, &value); err != nil {
		if pangolin.IsNotImplemented(err) {
			log.Info("Pangolin endpoint not available; skipping set", "error", err)
			return nil
		}
		return fmt.Errorf("failed to set %s: %w", label, err)
	}
	log.Info("Set resource " + label)
	return r.setManagedAnnotation(ctx, ingress, hashAnnotation, desiredHash)
}

func (r *IngressReconciler) reconcileWhitelist(ctx context.Context, ingress *networkingv1.Ingress, resourceID string) error {
	log := log.FromContext(ctx).WithValues("authMethod", "whitelist", "resourceID", resourceID)
	desired, err := parseStringSliceAnnotation(ingress.Annotations, annotationEmailWhitelist)
	if err != nil {
		return err
	}
	if desired == nil {
		return nil
	}
	current, err := r.PangolinClient.GetResourceWhitelist(ctx, resourceID)
	if err != nil {
		if pangolin.IsNotImplemented(err) {
			log.Info("Pangolin endpoint not available; skipping whitelist", "error", err)
			return nil
		}
		return fmt.Errorf("failed to get current whitelist: %w", err)
	}
	if stringSetsEqual(current, desired) {
		return nil
	}
	if err := r.PangolinClient.SetResourceWhitelist(ctx, resourceID, desired); err != nil {
		if pangolin.IsNotImplemented(err) {
			log.Info("Pangolin endpoint not available; skipping whitelist set", "error", err)
			return nil
		}
		return fmt.Errorf("failed to set whitelist: %w", err)
	}
	log.Info("Updated resource whitelist", "count", len(desired))
	return nil
}

func (r *IngressReconciler) reconcileRoles(ctx context.Context, ingress *networkingv1.Ingress, resourceID string) error {
	log := log.FromContext(ctx).WithValues("authMethod", "roles", "resourceID", resourceID)
	desired, err := parseIntSliceAnnotation(ingress.Annotations, annotationRoleIDs)
	if err != nil {
		return err
	}
	if desired == nil {
		return nil
	}
	current, err := r.PangolinClient.ListResourceRoles(ctx, resourceID)
	if err != nil {
		if pangolin.IsNotImplemented(err) {
			log.Info("Pangolin endpoint not available; skipping roles", "error", err)
			return nil
		}
		return fmt.Errorf("failed to list current roles: %w", err)
	}
	if intSetsEqual(current, desired) {
		return nil
	}
	if err := r.PangolinClient.SetResourceRoles(ctx, resourceID, desired); err != nil {
		if pangolin.IsNotImplemented(err) {
			log.Info("Pangolin endpoint not available; skipping roles set", "error", err)
			return nil
		}
		return fmt.Errorf("failed to set roles: %w", err)
	}
	log.Info("Updated resource roles", "count", len(desired))
	return nil
}

func (r *IngressReconciler) reconcileUsers(ctx context.Context, ingress *networkingv1.Ingress, resourceID string) error {
	log := log.FromContext(ctx).WithValues("authMethod", "users", "resourceID", resourceID)
	desired, err := parseStringSliceAnnotation(ingress.Annotations, annotationUserIDs)
	if err != nil {
		return err
	}
	if desired == nil {
		return nil
	}
	current, err := r.PangolinClient.ListResourceUsers(ctx, resourceID)
	if err != nil {
		if pangolin.IsNotImplemented(err) {
			log.Info("Pangolin endpoint not available; skipping users", "error", err)
			return nil
		}
		return fmt.Errorf("failed to list current users: %w", err)
	}
	if stringSetsEqual(current, desired) {
		return nil
	}
	if err := r.PangolinClient.SetResourceUsers(ctx, resourceID, desired); err != nil {
		if pangolin.IsNotImplemented(err) {
			log.Info("Pangolin endpoint not available; skipping users set", "error", err)
			return nil
		}
		return fmt.Errorf("failed to set users: %w", err)
	}
	log.Info("Updated resource users", "count", len(desired))
	return nil
}

func stringSetsEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	set := make(map[string]struct{}, len(a))
	for _, s := range a {
		set[s] = struct{}{}
	}
	for _, s := range b {
		if _, ok := set[s]; !ok {
			return false
		}
	}
	return true
}

func intSetsEqual(a, b []int) bool {
	if len(a) != len(b) {
		return false
	}
	set := make(map[int]struct{}, len(a))
	for _, n := range a {
		set[n] = struct{}{}
	}
	for _, n := range b {
		if _, ok := set[n]; !ok {
			return false
		}
	}
	return true
}

// findExistingResource searches for an existing Pangolin resource matching the
// given subdomain and domainID. This is used to adopt resources that already
// exist when a create returns 409 Conflict.
func (r *IngressReconciler) findExistingResource(ctx context.Context, subdomain, domainID string) (*pangolin.Resource, error) {
	resources, err := r.PangolinClient.ListResources(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to list resources: %w", err)
	}
	for i := range resources {
		res := &resources[i]
		if res.Subdomain == subdomain && res.DomainID == domainID {
			return res, nil
		}
	}
	return nil, fmt.Errorf("could not find existing resource with subdomain %q and domainID %q", subdomain, domainID)
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

// matchHostToDomains matches a host against a list of known Pangolin domains
// using suffix matching. The domains slice must be sorted by BaseDomain length
// descending (longest first) so that the most specific domain wins.
// Returns the subdomain prefix, the matching domain ID, and whether a match was found.
func matchHostToDomains(host string, domains []pangolin.Domain) (subdomain, domainID string, matched bool) {
	for _, d := range domains {
		if strings.HasSuffix(host, "."+d.BaseDomain) {
			return strings.TrimSuffix(host, "."+d.BaseDomain), d.ID, true
		}
		if host == d.BaseDomain {
			return "", d.ID, true
		}
	}
	return "", "", false
}

// resolveHostDomain resolves a hostname against known Pangolin domains,
// returning the subdomain, domain ID, and any error.
// It uses API-first matching: the host is matched against known Pangolin
// domains by suffix, with the longest match winning. If no Pangolin domain
// matches, it falls back to the Public Suffix List to parse the domain.
func (r *IngressReconciler) resolveHostDomain(ctx context.Context, host string) (subdomain, domainID string, err error) {
	log := log.FromContext(ctx)

	host = strings.TrimSpace(host)
	if host == "" {
		return "", "", fmt.Errorf("empty host")
	}
	if r.domains == nil {
		return "", "", fmt.Errorf("domain cache is not initialized")
	}

	domains, err := r.domains.get(ctx)
	if err != nil {
		return "", "", err
	}

	if sub, id, ok := matchHost(host, domains); ok {
		return sub, id, nil
	}

	// A miss may simply mean the cache predates a domain registered in Pangolin
	// after this process started. Refetch (rate-limited) and retry before
	// declaring the host unresolvable, so the controller self-heals instead of
	// failing until someone restarts the pod.
	refreshed, didRefresh, refreshErr := r.domains.refreshIfStale(ctx)
	switch {
	case refreshErr != nil:
		// Keep serving the existing cache. The resolution failure below is the
		// error the operator can act on; this one is context.
		log.Error(refreshErr, "Failed to refresh Pangolin domain list", "host", host)
	case didRefresh:
		log.Info("Refreshed Pangolin domain list after resolution miss", "host", host, "domains", len(refreshed))
		log.V(1).Info("Known Pangolin domains", "domains", baseDomains(refreshed))
		if sub, id, ok := matchHost(host, refreshed); ok {
			return sub, id, nil
		}
	}

	count, lastRefresh := r.domains.describe()
	detail := ""
	if pslDomain, pslErr := publicsuffix.EffectiveTLDPlusOne(host); pslErr != nil {
		detail = fmt.Sprintf(", PSL fallback failed: %v", pslErr)
	} else {
		detail = fmt.Sprintf(" (parsed domain: %q)", pslDomain)
	}

	return "", "", fmt.Errorf("%w for host %q%s: %d Pangolin domains known, list last refreshed %s",
		errDomainNotFound, host, detail, count, lastRefresh)
}

// matchHost resolves a host against a domain list: first by longest suffix
// match, then by exact match against the registrable domain derived from the
// Public Suffix List.
func matchHost(host string, domains []pangolin.Domain) (subdomain, domainID string, ok bool) {
	// API-first matching (domains are sorted longest-first)
	if sub, id, matched := matchHostToDomains(host, domains); matched {
		return sub, id, true
	}

	// Fallback: use the Public Suffix List to extract the registrable domain,
	// then try an exact lookup against the known Pangolin domains.
	pslDomain, pslErr := publicsuffix.EffectiveTLDPlusOne(host)
	if pslErr != nil {
		return "", "", false
	}

	for _, d := range domains {
		if d.BaseDomain == pslDomain {
			sub := ""
			if pslDomain != host {
				sub = strings.TrimSuffix(host, "."+pslDomain)
			}
			return sub, d.ID, true
		}
	}

	return "", "", false
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

// parseStringSliceAnnotation returns nil when the annotation is absent, an
// empty (non-nil) slice when the value is "[]", and the parsed slice otherwise.
// Returns (nil, error) when the value is present but cannot be parsed.
func parseStringSliceAnnotation(annotations map[string]string, key string) ([]string, error) {
	v, ok := annotations[key]
	if !ok {
		return nil, nil
	}
	if strings.TrimSpace(v) == "" {
		return nil, nil
	}
	out := []string{}
	if err := json.Unmarshal([]byte(v), &out); err != nil {
		return nil, fmt.Errorf("annotation %q is not a JSON array of strings: %w", key, err)
	}
	return out, nil
}

// parseIntSliceAnnotation mirrors parseStringSliceAnnotation for integers.
func parseIntSliceAnnotation(annotations map[string]string, key string) ([]int, error) {
	v, ok := annotations[key]
	if !ok {
		return nil, nil
	}
	if strings.TrimSpace(v) == "" {
		return nil, nil
	}
	out := []int{}
	if err := json.Unmarshal([]byte(v), &out); err != nil {
		return nil, fmt.Errorf("annotation %q is not a JSON array of integers: %w", key, err)
	}
	return out, nil
}

// parseSecretRef parses a "name" or "namespace/name" Secret reference,
// defaulting the namespace to defaultNamespace when not specified.
// Returns ok=false when value is empty.
func parseSecretRef(value, defaultNamespace string) (namespace, name string, ok bool) {
	v := strings.TrimSpace(value)
	if v == "" {
		return "", "", false
	}
	if i := strings.Index(v, "/"); i >= 0 {
		ns := strings.TrimSpace(v[:i])
		nm := strings.TrimSpace(v[i+1:])
		if ns == "" || nm == "" || strings.Contains(nm, "/") {
			return "", "", false
		}
		return ns, nm, true
	}
	return defaultNamespace, v, true
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
		if _, managed := controllerManagedAnnotations[key]; managed {
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
		if _, managed := controllerManagedAnnotations[key]; managed {
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
	if r.Recorder == nil {
		r.Recorder = mgr.GetEventRecorderFor("pangolin-ingress-controller")
	}
	return ctrl.NewControllerManagedBy(mgr).
		For(&networkingv1.Ingress{}).
		WithEventFilter(predicate.Or(
			predicate.GenerationChangedPredicate{},
			pangolinAnnotationChangedPredicate{},
		)).
		Complete(r)
}
