package controller

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
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
	// annotationResourceIDs maps each host on the Ingress to its Pangolin
	// resource id, as a JSON object. An Ingress with several hosts owns one
	// resource per host, which the single resource-id cannot record. resource-id
	// is still written, holding the first host's id, so status and a controller
	// rolled back to an older image keep working.
	annotationResourceIDs = "pangolin.ingress.k8s.io/resource-ids"

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
	annotationResourceIDs:  {},
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

	// Initialize the Pangolin client if needed.
	//
	// This happens after the class check, not before: the controller watches
	// every Ingress in the cluster, and an Ingress belonging to another
	// controller is none of its business. Initializing first meant a cluster
	// with no Pangolin credentials failed -- and retried, and counted a
	// reconcile error -- for every foreign Ingress it would then have ignored.
	if r.PangolinClient == nil {
		if err := r.initPangolinClient(ctx); err != nil {
			log.Error(err, "Failed to initialize Pangolin client")
			return ctrl.Result{}, err
		}
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

// hostPaths is one host on an Ingress with every path declared for it. Rules
// naming the same host are merged: Pangolin keys a resource by its domain, so
// they are one resource either way.
type hostPaths struct {
	host  string
	paths []networkingv1.HTTPIngressPath
}

// desiredTarget is one Ingress path resolved to the Service port it routes to.
type desiredTarget struct {
	service string
	port    int32
	path    networkingv1.HTTPIngressPath
}

// groupPathsByHost returns the Ingress's hosts in the order they first appear,
// each with its paths. Rules without a host or without paths are skipped.
func groupPathsByHost(ingress *networkingv1.Ingress) []hostPaths {
	var out []hostPaths
	index := map[string]int{}
	for _, rule := range ingress.Spec.Rules {
		if rule.Host == "" || rule.HTTP == nil || len(rule.HTTP.Paths) == 0 {
			continue
		}
		i, seen := index[rule.Host]
		if !seen {
			i = len(out)
			index[rule.Host] = i
			out = append(out, hostPaths{host: rule.Host})
		}
		out[i].paths = append(out[i].paths, rule.HTTP.Paths...)
	}
	return out
}

// processIngressRules programs one Pangolin resource per host on the Ingress,
// with one target per path, and deletes the resources of hosts the Ingress no
// longer declares.
//
// It works host by host, not path by path. Reconciling each path on its own is
// what broke multi-host and multi-path Ingresses: every path wrote the single
// resource-id annotation and pruned every target but its own, so the last path
// reconciled was the only one left routing.
func (r *IngressReconciler) processIngressRules(ctx context.Context, ingress *networkingv1.Ingress) error {
	log := log.FromContext(ctx)

	hosts := groupPathsByHost(ingress)
	ids := readResourceIDs(ingress)

	for _, hp := range hosts {
		targets := make([]desiredTarget, 0, len(hp.paths))
		for _, path := range hp.paths {
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
				"host", hp.host,
				"path", path.Path,
				"pathType", *path.PathType,
				"service", serviceName,
				"servicePort", servicePort,
			)

			targets = append(targets, desiredTarget{service: serviceName, port: servicePort, path: path})
		}

		if err := r.reconcileHost(ctx, ingress, hp.host, targets, ids, len(hosts)); err != nil {
			log.Error(err, "Failed to create/update Pangolin resource", "host", hp.host)
			return err
		}
	}

	if err := r.pruneRemovedHosts(ctx, ingress, ids, hosts); err != nil {
		return err
	}

	// Auth runs once over every resource the Ingress owns rather than per host:
	// the password and pincode hashes are one annotation each, and per-host
	// passes would each overwrite the previous host's hash.
	return r.reconcileResourceAuth(ctx, ingress, sortedResourceIDs(ids))
}

// readResourceIDs returns the host -> Pangolin resource id map recorded on the
// Ingress. A missing or malformed annotation yields an empty map, which
// reconciles as if the resources were new: creation conflicts on the existing
// domain and adopts the resource that holds it.
func readResourceIDs(ingress *networkingv1.Ingress) map[string]string {
	raw := ingress.Annotations[annotationResourceIDs]
	if raw == "" {
		return map[string]string{}
	}
	ids := map[string]string{}
	if err := json.Unmarshal([]byte(raw), &ids); err != nil {
		return map[string]string{}
	}
	return ids
}

// storeResourceIDs records ids on the Ingress, together with the first host's
// id in the legacy resource-id annotation, and persists the Ingress only when
// either annotation changed.
func (r *IngressReconciler) storeResourceIDs(ctx context.Context, ingress *networkingv1.Ingress, ids map[string]string) error {
	if ingress.Annotations == nil {
		ingress.Annotations = map[string]string{}
	}

	encoded := ""
	if len(ids) > 0 {
		// Map keys marshal in sorted order, so equal maps encode identically.
		raw, err := json.Marshal(ids)
		if err != nil {
			return fmt.Errorf("failed to encode resource ids: %w", err)
		}
		encoded = string(raw)
	}

	legacy := ""
	for _, hp := range groupPathsByHost(ingress) {
		if id := ids[hp.host]; id != "" {
			legacy = id
			break
		}
	}

	changed := setOrDeleteAnnotation(ingress, annotationResourceIDs, encoded)
	if setOrDeleteAnnotation(ingress, annotationResourceID, legacy) {
		changed = true
	}
	if !changed {
		return nil
	}
	return r.Update(ctx, ingress)
}

// setOrDeleteAnnotation sets key to value, or removes it when value is empty,
// and reports whether the annotations changed.
func setOrDeleteAnnotation(ingress *networkingv1.Ingress, key, value string) bool {
	current, present := ingress.Annotations[key]
	if value == "" {
		if !present {
			return false
		}
		delete(ingress.Annotations, key)
		return true
	}
	if present && current == value {
		return false
	}
	ingress.Annotations[key] = value
	return true
}

// sortedResourceIDs returns the distinct resource ids in ids, sorted.
func sortedResourceIDs(ids map[string]string) []string {
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		if !slices.Contains(out, id) {
			out = append(out, id)
		}
	}
	slices.Sort(out)
	return out
}

// hasResourceID reports whether any host in ids maps to id.
func hasResourceID(ids map[string]string, id string) bool {
	for _, v := range ids {
		if v == id {
			return true
		}
	}
	return false
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

	r.setPangolinClient(pangolin.NewClient(r.PangolinBaseURL, string(apiKey), r.OrgID))
	log.Info("Initialized Pangolin client", "baseURL", r.PangolinBaseURL,
		"domainCacheRefreshInterval", r.DomainCacheRefreshInterval)

	return nil
}

// setPangolinClient installs the client and the domain cache that reads
// through it.
//
// The two are set together because the cache is useless without the client and
// the reconcile fails with "domain cache is not initialized" without the cache.
// Assigning PangolinClient directly is what leaves that invariant broken.
func (r *IngressReconciler) setPangolinClient(c *pangolin.Client) {
	r.PangolinClient = c
	r.domains = newDomainCache(c, r.DomainCacheRefreshInterval)
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

// resourceIDForHost returns the id of host's Pangolin resource, or "" when it
// has none yet.
//
// Ingresses programmed before resource-ids existed carry only resource-id. On a
// single-host Ingress that id is the host's resource -- the only shape
// resource-id ever worked for -- and it is adopted as is. With several hosts it
// belongs to whichever host reconciled last, so only the host Pangolin says it
// serves adopts it; the others fall through to creation, which conflicts on
// their domain and adopts the resource that already holds it.
func (r *IngressReconciler) resourceIDForHost(ctx context.Context, ingress *networkingv1.Ingress, host string, ids map[string]string, hostCount int) string {
	if id := ids[host]; id != "" {
		return id
	}
	legacy := ingress.Annotations[annotationResourceID]
	if legacy == "" || hasResourceID(ids, legacy) {
		return ""
	}
	if hostCount == 1 {
		return legacy
	}
	resource, err := r.PangolinClient.GetResource(ctx, legacy)
	if err != nil {
		log.FromContext(ctx).Error(err, "Failed to look up legacy Pangolin resource; treating host as new",
			"resourceID", legacy, "host", host)
		return ""
	}
	if resource.FullDomain != host {
		return ""
	}
	return legacy
}

// reconcileHost brings host's Pangolin resource in line with the Ingress: the
// resource and its settings, then one target per declared path.
func (r *IngressReconciler) reconcileHost(ctx context.Context, ingress *networkingv1.Ingress, host string, targets []desiredTarget, ids map[string]string, hostCount int) error {
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

	resourceID := r.resourceIDForHost(ctx, ingress, host, ids, hostCount)

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

	if resourceID != "" {
		if _, err := r.PangolinClient.UpdateResource(ctx, resourceID, updateReq); err != nil {
			log.Error(err, "Failed to update Pangolin resource", "resourceID", resourceID, "subdomain", subdomain, "domainID", domainID, "host", host)
			return fmt.Errorf("failed to update Pangolin resource %s: %w", resourceID, err)
		}
		log.Info("Updated Pangolin resource", "resourceID", resourceID, "name", resourceName)

		if ids[host] != resourceID {
			ids[host] = resourceID
			if err := r.storeResourceIDs(ctx, ingress, ids); err != nil {
				return err
			}
		}
	} else {
		// Create new resource
		resource, err := r.PangolinClient.CreateResource(ctx, resourceReq)
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

		// Record the resource ID before applying settings, so a failure below
		// does not leave a resource the next reconcile cannot find.
		resourceID = strconv.Itoa(resource.ID)
		ids[host] = resourceID
		if err := r.storeResourceIDs(ctx, ingress, ids); err != nil {
			return err
		}

		// Apply update settings (SSO, SSL, etc.) to the resource
		if _, err := r.PangolinClient.UpdateResource(ctx, resourceID, updateReq); err != nil {
			log.Error(err, "Failed to apply settings to Pangolin resource", "resourceID", resourceID)
			return fmt.Errorf("failed to apply settings to Pangolin resource %s: %w", resourceID, err)
		}
	}

	return r.reconcileTargets(ctx, ingress, resourceID, targets)
}

// reconcileTargets gives the resource exactly one target per desired path and
// deletes every other target on it. A target is matched on site, backend, path
// and match type, so two paths to the same Service are two targets rather than
// one target that each path's reconcile rewrites.
func (r *IngressReconciler) reconcileTargets(ctx context.Context, ingress *networkingv1.Ingress, resourceID string, targets []desiredTarget) error {
	log := log.FromContext(ctx)

	site, err := r.getSiteInfo(ctx)
	if err != nil {
		log.Error(err, "Failed to resolve site for target creation", "siteNiceID", r.SiteNiceID)
		return err
	}

	// Check for existing targets to avoid duplicates on restarts
	existingTargets, err := r.PangolinClient.ListTargets(ctx, resourceID)
	if err != nil {
		log.Error(err, "Failed to list existing targets", "resourceID", resourceID)
		return fmt.Errorf("failed to list targets for resource %s: %w", resourceID, err)
	}

	active := make(map[int]struct{}, len(targets))
	for _, desired := range targets {
		targetReq := buildTargetRequest(ingress, site.ID, desired)

		var existingTarget *pangolin.Target
		for i := range existingTargets {
			t := &existingTargets[i]
			if _, claimed := active[t.ID]; claimed {
				continue
			}
			if targetMatches(t, targetReq) {
				existingTarget = t
				break
			}
		}

		if existingTarget != nil {
			// Target already exists — update it instead of creating a duplicate
			targetIDStr := strconv.Itoa(existingTarget.ID)
			if _, err := r.PangolinClient.UpdateTarget(ctx, targetIDStr, targetReq); err != nil {
				log.Error(err, "Failed to update Pangolin target", "targetID", targetIDStr, "resourceID", resourceID)
				return fmt.Errorf("failed to update Pangolin target %s: %w", targetIDStr, err)
			}
			active[existingTarget.ID] = struct{}{}
			log.Info("Updated existing Pangolin target", "targetID", targetIDStr, "service", desired.service, "port", desired.port, "path", targetReq.Path)
			continue
		}

		// No matching target — create a new one
		newTarget, err := r.PangolinClient.CreateTarget(ctx, resourceID, targetReq)
		if err != nil {
			log.Error(err, "Failed to create Pangolin target", "resourceID", resourceID, "service", desired.service, "port", desired.port, "path", targetReq.Path)
			return fmt.Errorf("failed to create Pangolin target for service %s:%d path %s: %w", desired.service, desired.port, targetReq.Path, err)
		}
		active[newTarget.ID] = struct{}{}
		log.Info("Created Pangolin target", "targetID", newTarget.ID, "service", desired.service, "port", desired.port, "path", targetReq.Path)
	}

	// Clean up targets that no path on the Ingress declares any more
	for _, t := range existingTargets {
		if _, ok := active[t.ID]; ok {
			continue
		}
		staleID := strconv.Itoa(t.ID)
		if delErr := r.PangolinClient.DeleteTarget(ctx, staleID); delErr != nil {
			log.Error(delErr, "Failed to delete stale Pangolin target", "targetID", staleID)
		} else {
			log.Info("Deleted stale Pangolin target", "targetID", staleID, "ip", t.IP, "port", t.Port, "path", t.Path)
		}
	}

	return nil
}

// buildTargetRequest describes the Pangolin target for one Ingress path,
// including the health check settings from the Ingress annotations.
func buildTargetRequest(ingress *networkingv1.Ingress, siteID int, desired desiredTarget) *pangolin.CreateTargetRequest {
	annotations := ingress.Annotations
	targetIP := fmt.Sprintf("%s.%s.svc.cluster.local", desired.service, ingress.Namespace)
	targetPort := int(desired.port)
	targetPath := desired.path.Path
	if targetPath == "" {
		targetPath = "/"
	}

	targetReq := &pangolin.CreateTargetRequest{
		SiteID:              siteID,
		IP:                  targetIP,
		Method:              "http",
		Port:                targetPort,
		Enabled:             true,
		Path:                targetPath,
		PathMatchType:       pathTypeToMatch(desired.path.PathType),
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
			p := targetPort
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

	return targetReq
}

// targetMatches reports whether an existing target is the one req describes. A
// target recorded without a path routes the whole host, which is what "/" as a
// prefix means, so it matches that path instead of being replaced by an
// equivalent target.
func targetMatches(t *pangolin.Target, req *pangolin.CreateTargetRequest) bool {
	path, matchType := t.Path, t.PathMatchType
	if path == "" {
		path = "/"
	}
	if matchType == "" {
		matchType = "prefix"
	}
	return t.SiteID == req.SiteID && t.IP == req.IP && t.Port == req.Port &&
		path == req.Path && matchType == req.PathMatchType
}

// pruneRemovedHosts deletes the Pangolin resource of every recorded host the
// Ingress no longer declares, so a renamed or removed host stops routing
// instead of lingering in Pangolin.
func (r *IngressReconciler) pruneRemovedHosts(ctx context.Context, ingress *networkingv1.Ingress, ids map[string]string, hosts []hostPaths) error {
	log := log.FromContext(ctx)

	declared := make(map[string]string, len(hosts))
	for _, hp := range hosts {
		declared[hp.host] = ids[hp.host]
	}

	pruned := false
	for host, id := range ids {
		if _, ok := declared[host]; ok {
			continue
		}
		if hasResourceID(declared, id) {
			// Still serving a declared host; only the stale entry goes.
			log.Info("Dropping stale resource-ids entry that shares a resource with a declared host", "host", host, "resourceID", id)
		} else {
			if err := r.deleteResource(ctx, id); err != nil {
				return fmt.Errorf("failed to delete Pangolin resource %s of removed host %s: %w", id, host, err)
			}
			log.Info("Deleted Pangolin resource of a host the Ingress no longer declares", "host", host, "resourceID", id)
		}
		delete(ids, host)
		pruned = true
	}
	if !pruned {
		return nil
	}
	return r.storeResourceIDs(ctx, ingress, ids)
}

// deleteResource deletes a Pangolin resource, counting one that is already gone
// as deleted so a retry after a partial failure can finish.
func (r *IngressReconciler) deleteResource(ctx context.Context, resourceID string) error {
	if err := r.PangolinClient.DeleteResource(ctx, resourceID); err != nil && !pangolin.IsNotFound(err) {
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

// hashSecretValue computes a stable change-detection hash over the resource IDs
// (comma-joined, acting as a per-Ingress salt; a single resource's ID alone) and
// the secret value. Not intended as a password hash for security — just to
// detect when the user changed the value or the resources it applies to.
func hashSecretValue(resourceIDs, value string) string {
	h := sha256.Sum256([]byte(resourceIDs + ":" + value))
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
// assignments, user assignments) on every resource the Ingress owns. 404/405
// from any sub-endpoint is logged and the sub-step is skipped — older Pangolin
// instances may not have the route.
func (r *IngressReconciler) reconcileResourceAuth(ctx context.Context, ingress *networkingv1.Ingress, resourceIDs []string) error {
	if len(resourceIDs) == 0 {
		return nil
	}
	if err := r.reconcilePassword(ctx, ingress, resourceIDs); err != nil {
		return err
	}
	if err := r.reconcilePincode(ctx, ingress, resourceIDs); err != nil {
		return err
	}
	for _, resourceID := range resourceIDs {
		if err := r.reconcileWhitelist(ctx, ingress, resourceID); err != nil {
			return err
		}
		if err := r.reconcileRoles(ctx, ingress, resourceID); err != nil {
			return err
		}
		if err := r.reconcileUsers(ctx, ingress, resourceID); err != nil {
			return err
		}
	}
	return nil
}

func (r *IngressReconciler) reconcilePassword(ctx context.Context, ingress *networkingv1.Ingress, resourceIDs []string) error {
	return r.reconcileSecretBackedAuth(
		ctx, ingress, resourceIDs,
		annotationPasswordSecretRef, annotationPasswordHash, secretKeyPassword,
		"password",
		r.PangolinClient.SetResourcePassword,
	)
}

func (r *IngressReconciler) reconcilePincode(ctx context.Context, ingress *networkingv1.Ingress, resourceIDs []string) error {
	return r.reconcileSecretBackedAuth(
		ctx, ingress, resourceIDs,
		annotationPincodeSecretRef, annotationPincodeHash, secretKeyPincode,
		"pincode",
		r.PangolinClient.SetResourcePincode,
	)
}

// reconcileSecretBackedAuth implements the convergent state machine shared by
// password and pincode: annotation absent + hash absent → no-op; annotation
// present + hash matches → no-op; annotation present + hash stale or absent →
// set on every resource + write hash; annotation absent + hash present → clear
// on every resource + remove hash.
//
// The hash is one annotation for the whole Ingress, so it covers the set of
// resources as well as the value: adding a host changes it, and the new host's
// resource gets the password too.
func (r *IngressReconciler) reconcileSecretBackedAuth(
	ctx context.Context,
	ingress *networkingv1.Ingress,
	resourceIDs []string,
	refAnnotation, hashAnnotation, secretKey, label string,
	setFn func(context.Context, string, *string) error,
) error {
	log := log.FromContext(ctx).WithValues("authMethod", label, "resourceIDs", resourceIDs)
	annotations := ingress.Annotations
	refValue := annotations[refAnnotation]
	storedHash := annotations[hashAnnotation]

	if refValue == "" {
		if storedHash == "" {
			return nil
		}
		for _, resourceID := range resourceIDs {
			if err := setFn(ctx, resourceID, nil); err != nil {
				if pangolin.IsNotImplemented(err) {
					log.Info("Pangolin endpoint not available; skipping clear", "error", err)
					return nil
				}
				return fmt.Errorf("failed to clear %s on resource %s: %w", label, resourceID, err)
			}
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
	desiredHash := hashSecretValue(strings.Join(resourceIDs, ","), value)
	if desiredHash == storedHash {
		return nil
	}
	for _, resourceID := range resourceIDs {
		if err := setFn(ctx, resourceID, &value); err != nil {
			if pangolin.IsNotImplemented(err) {
				log.Info("Pangolin endpoint not available; skipping set", "error", err)
				return nil
			}
			return fmt.Errorf("failed to set %s on resource %s: %w", label, resourceID, err)
		}
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

// deletePangolinResources deletes every Pangolin resource the Ingress owns: one
// per recorded host, plus a legacy resource-id that no host has claimed.
func (r *IngressReconciler) deletePangolinResources(ctx context.Context, ingress *networkingv1.Ingress) error {
	log := log.FromContext(ctx)

	resourceIDs := sortedResourceIDs(readResourceIDs(ingress))
	if legacy := ingress.Annotations[annotationResourceID]; legacy != "" && !slices.Contains(resourceIDs, legacy) {
		resourceIDs = append(resourceIDs, legacy)
	}
	if len(resourceIDs) == 0 {
		log.Info("No Pangolin resource ID found, skipping deletion")
		return nil
	}

	for _, resourceID := range resourceIDs {
		// Delete the resource (targets will be deleted automatically)
		if err := r.deleteResource(ctx, resourceID); err != nil {
			log.Error(err, "Failed to delete Pangolin resource", "resourceID", resourceID)
			return err
		}
		log.Info("Deleted Pangolin resource", "resourceID", resourceID)
	}

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
