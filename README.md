# Pangolin Ingress Controller

A Kubernetes Ingress Controller that automatically creates and manages resources in [Pangolin](https://pangolin.net) - an identity-aware tunneled reverse proxy server.

## Features

- 🚀 Native Kubernetes Ingress resource support
- 🔗 Automatic Pangolin resource and target creation
- 🗑️ Automatic cleanup with Kubernetes finalizers
- 🔑 Secure API key management via Kubernetes secrets
- 🎯 Path-based and host-based routing
- 📊 Prometheus metrics support
- 🏥 Health checks and readiness probes
- 🔄 Leader election for high availability
- 📝 Comprehensive logging

## Architecture

The Pangolin Ingress Controller is built using the Kubernetes controller-runtime framework and implements the standard Kubernetes Ingress specification. It watches for Ingress resources with the `pangolin` IngressClass and automatically:

1. **Creates Pangolin resources** for each Ingress host
2. **Creates targets** pointing to your Kubernetes services
3. **Manages lifecycle** with finalizers to ensure cleanup
4. **Stores metadata** in Ingress annotations for tracking

### Components

- **Controller Manager**: Main reconciliation loop that watches Ingress resources
- **Ingress Reconciler**: Processes Ingress rules and configures the load balancer
- **Metrics Server**: Exposes Prometheus metrics on port 8080
- **Health Probes**: Liveness and readiness endpoints on port 8081

## Prerequisites

- Kubernetes cluster (1.24+)
- kubectl configured to access your cluster
- **Pangolin instance** (self-hosted or cloud) - [Get started](https://pangolin.net)
- **Pangolin API key** scoped to your organization, with the permissions listed
  under [API key permissions](#api-key-permissions)
- Docker (for building images)
- Go 1.21+ (for development)

## API key permissions

The controller authenticates to Pangolin with a single organization-scoped API
key. Grant exactly the permissions below — each is listed with what needs it,
so you can drop whole rows if you do not use that feature.

Permission names are the labels shown in **Organization → API Keys → Permissions**.

### Required

| Group | Permission | Needed for |
| --- | --- | --- |
| Domain | List Organization Domains | Resolving an Ingress host to a Pangolin domain |
| Site | Get Site | Looking up the configured site, and the `proxyIp` reported in Ingress status |
| Resource | Create Resource, Get Resource, List Resources, Update Resource, Delete Resource | The `Ingress` path: creating and converging the public resource, and adopting an existing one after an annotation is lost |
| Target | Create Target, Get Target, List Targets, Update Target, Delete Target | Pointing a resource at the backing Service |

### Required for `PangolinEndpoint` (private endpoints)

| Group | Permission | Needed for |
| --- | --- | --- |
| Resource | Create Site Resource, List Site Resources, Update Site Resource, Delete Site Resource | Creating and converging a private resource, and re-finding it by its nice ID |
| Role | List Roles | Resolving `spec.private.access.roles` names to IDs |
| Client | List Clients | Resolving `spec.private.access.clients` names to IDs |
| Organization | Get Organization User | Resolving `spec.private.access.users` names to IDs |

Grant the three lookup permissions only for the principal types you actually
name. An endpoint that names no clients does not need **List Clients**, and a
missing lookup permission surfaces as `ResolvedRefs=False` on the object rather
than as a silent failure.

### Required only for the auth-method annotations

| Group | Permission | Needed for |
| --- | --- | --- |
| Resource | Set Resource Password | `pangolin.ingress.k8s.io/password-secret-ref` |
| Resource | Set Resource Pincode | `pangolin.ingress.k8s.io/pincode-secret-ref` |
| Resource | Get Resource Email Whitelist, Set Resource Email Whitelist | `pangolin.ingress.k8s.io/email-whitelist` |
| Resource | List Allowed Resource Roles, Set Allowed Resource Roles | `pangolin.ingress.k8s.io/role-ids` |
| Resource | List Resource Users, Set Resource Users | `pangolin.ingress.k8s.io/user-ids` |

### Not needed

Granting these is harmless, but the controller never calls them, and a
least-privilege key can leave them off:

- **List Users** — user lookup goes through *Get Organization User* by username
- **List Sites** — the site is fetched by its configured nice ID, never listed
- **Get Site Resource** — the private-resource point read is unusable on current
  Pangolin builds (it rejects every request), so the controller reads private
  resources out of *List Site Resources* instead
- **Resource Rule** (all) — `apply-rules` sets a field on the resource itself;
  the controller does not manage rule objects
- **Resource Policy**, **Access Token**, **Site Provisioning Key**, **Logs**,
  and everything under **Organization** other than *Get Organization User*

### Note on editing permissions

Pangolin's permission editor replaces the key's permission set rather than
adding to it. Adding a permission has been observed to silently drop others —
after any change, re-check the whole set, not just the entry you edited. A key
that loses *List Roles* keeps working until something names a role, and then
fails with `PrincipalNotFound`.

## Installation

### Quick Start

1. **Create a Pangolin API key:**

   - Log in to your Pangolin dashboard
   - Navigate to **Organization → API Keys**
   - Create a new API key scoped to your organization and grant it the
     permissions in [API key permissions](#api-key-permissions)
   - Copy the API key

2. **Create the API key secret:**

```bash
kubectl create secret generic pangolin-api-key \
  --from-literal=api-key=YOUR_PANGOLIN_API_KEY_HERE \
  --namespace=pangolin-system
```

3. **Deploy the controller to your cluster:**

```bash
kubectl apply -f deploy/
```

4. **Verify the deployment:**

```bash
kubectl get pods -n pangolin-system
kubectl get ingressclass
```

You should see the `pangolin-ingress-controller` pod running and the `pangolin` IngressClass available.

📖 **For detailed setup instructions, see [SETUP.md](SETUP.md)**

### Official Container Image

Multi-architecture images (amd64 and arm64) are published to GitHub Container Registry:

```bash
docker pull ghcr.io/corelyr-oss/pangolin-ingress-controller:latest
```

Helm installations use this registry path by default (see `chart/values.yaml`).

### Building from Source

1. **Clone the repository:**

```bash
git clone https://github.com/vinzenz/pangolin-ingress-controller.git
cd pangolin-ingress-controller
```

2. **Build the binary:**

```bash
make build
```

3. **Build the Docker image (optional, if you need a custom build):**

```bash
make docker-build IMG=repository.tf/kubernetes/pangolin-ingress-controller:dev
```

4. **Push to your registry:**

```bash
make docker-push IMG=repository.tf/kubernetes/pangolin-ingress-controller:dev
```

5. **Deploy to cluster:**

```bash
make deploy
```

## Usage

### Creating an Ingress

Create an Ingress resource with the `pangolin` IngressClass:

```yaml
apiVersion: networking.k8s.io/v1
kind: Ingress
metadata:
  name: example-ingress
  namespace: default
spec:
  ingressClassName: pangolin
  rules:
  - host: example.com
    http:
      paths:
      - path: /
        pathType: Prefix
        backend:
          service:
            name: example-service
            port:
              number: 80
```

Apply the Ingress:

```bash
kubectl apply -f your-ingress.yaml
```

### TLS Configuration

For HTTPS support, create a TLS secret and reference it in your Ingress:

```bash
kubectl create secret tls tls-secret \
  --cert=path/to/tls.crt \
  --key=path/to/tls.key
```

```yaml
apiVersion: networking.k8s.io/v1
kind: Ingress
metadata:
  name: tls-ingress
spec:
  ingressClassName: pangolin
  tls:
  - hosts:
    - secure.example.com
    secretName: tls-secret
  rules:
  - host: secure.example.com
    http:
      paths:
      - path: /
        pathType: Prefix
        backend:
          service:
            name: secure-service
            port:
              number: 443
```

### Example Application

Deploy a sample application to test the controller:

```bash
kubectl apply -f examples/sample-app.yaml
```

This creates:
- A namespace `example-app`
- An nginx deployment with 2 replicas
- A service exposing the deployment
- An Ingress resource using the Pangolin controller

Test the ingress:

```bash
# Add to /etc/hosts
echo "127.0.0.1 example.local" | sudo tee -a /etc/hosts

# Port forward to test locally
kubectl port-forward -n example-app svc/example-service 8080:80

# Access the application
curl http://example.local:8080
```

## Annotations

The Pangolin Ingress Controller supports the following annotations on Ingress resources to configure Pangolin resource settings.

### SSO / Access Control

| Annotation | Type | Default | Description |
|------------|------|---------|-------------|
| `pangolin.ingress.k8s.io/sso` | `bool` | *(unset)* | Enable or disable Pangolin SSO authentication for the resource |
| `pangolin.ingress.k8s.io/ssl` | `bool` | *(unset)* | Enable or disable SSL termination |
| `pangolin.ingress.k8s.io/block-access` | `bool` | *(unset)* | Block all access to the resource |
| `pangolin.ingress.k8s.io/email-whitelist-enabled` | `bool` | *(unset)* | Enable email whitelist–based access control (must be paired with `email-whitelist` to populate the list) |
| `pangolin.ingress.k8s.io/apply-rules` | `bool` | *(unset)* | Apply organization-level access rules to the resource |
| `pangolin.ingress.k8s.io/enabled` | `bool` | *(unset)* | Enable or disable the Pangolin resource entirely |
| `pangolin.ingress.k8s.io/skip-to-idp-id` | `int` | *(unset)* | Skip the Pangolin login page and redirect SSO directly to this configured Identity Provider ID |

### Resource Auth Methods

Per-resource auth that lives on dedicated Pangolin sub-endpoints. The controller reconciles each method independently and tolerates `404/405` from older Pangolin instances (logs a warning and skips the method).

| Annotation | Type | Description |
|------------|------|-------------|
| `pangolin.ingress.k8s.io/email-whitelist` | `JSON` | JSON array of email addresses / wildcards (e.g. `*@example.com`). Replaces the resource's whitelist on change. Empty array `[]` clears it. Annotation absent = unmanaged. Max 50 entries. |
| `pangolin.ingress.k8s.io/password-secret-ref` | `string` | Reference to a Kubernetes Secret containing the resource's shared password under key `password`. Format: `name` (Ingress namespace) or `namespace/name`. Removing the annotation clears the password. Pangolin requires 4–100 chars. |
| `pangolin.ingress.k8s.io/pincode-secret-ref` | `string` | Reference to a Kubernetes Secret containing the resource's pincode under key `pincode`. Same ref format. Pangolin requires exactly 6 digits. |
| `pangolin.ingress.k8s.io/role-ids` | `JSON` | JSON array of Pangolin role IDs (positive integers). Replaces the resource's role assignments. Find role IDs in the Pangolin UI or via the Pangolin API. |
| `pangolin.ingress.k8s.io/user-ids` | `JSON` | JSON array of Pangolin user ID strings. Replaces the resource's user assignments. |

#### Controller-managed

These annotations are written by the controller and MUST NOT be edited by users — they exist purely for change detection and do not retrigger reconciliation when modified.

| Annotation | Type | Description |
|------------|------|-------------|
| `pangolin.ingress.k8s.io/password-hash` | `string` | SHA-256 hash of the current password value, used to detect Secret changes without re-POSTing on every reconcile |
| `pangolin.ingress.k8s.io/pincode-hash` | `string` | Same as above, for the pincode |

### Proxy Settings

| Annotation | Type | Default | Description |
|------------|------|---------|-------------|
| `pangolin.ingress.k8s.io/sticky-session` | `bool` | `false` | Enable sticky sessions (session affinity) |
| `pangolin.ingress.k8s.io/tls-server-name` | `string` | *(unset)* | Override the TLS server name for backend connections |
| `pangolin.ingress.k8s.io/set-host-header` | `string` | *(unset)* | Override the Host header sent to the backend |
| `pangolin.ingress.k8s.io/post-auth-path` | `string` | *(unset)* | Path to redirect to after successful authentication |
| `pangolin.ingress.k8s.io/headers` | `JSON` | *(unset)* | Custom headers to add to proxied requests (JSON array) |

### Health Checks

| Annotation | Type | Default | Description |
|------------|------|---------|-------------|
| `pangolin.ingress.k8s.io/healthcheck-enabled` | `bool` | *(unset)* | Enable health checks for the target |
| `pangolin.ingress.k8s.io/healthcheck-path` | `string` | `/` | HTTP path to probe (e.g. `/healthz`) |
| `pangolin.ingress.k8s.io/healthcheck-scheme` | `string` | *(unset)* | Scheme for the health check (`http` or `https`) |
| `pangolin.ingress.k8s.io/healthcheck-mode` | `string` | *(unset)* | Health check mode |
| `pangolin.ingress.k8s.io/healthcheck-hostname` | `string` | *target IP* | Hostname to use in the health check request |
| `pangolin.ingress.k8s.io/healthcheck-port` | `int` | *service port* | Port to probe |
| `pangolin.ingress.k8s.io/healthcheck-interval` | `int` | `30` | Interval in seconds between checks (min 5) |
| `pangolin.ingress.k8s.io/healthcheck-unhealthy-interval` | `int` | *(unset)* | Interval in seconds between checks when unhealthy (min 5) |
| `pangolin.ingress.k8s.io/healthcheck-timeout` | `int` | *(unset)* | Timeout in seconds for each check (min 1) |
| `pangolin.ingress.k8s.io/healthcheck-headers` | `JSON` | *(unset)* | Custom headers for health check requests (JSON array) |
| `pangolin.ingress.k8s.io/healthcheck-follow-redirects` | `bool` | *(unset)* | Follow HTTP redirects during health checks |
| `pangolin.ingress.k8s.io/healthcheck-method` | `string` | `GET` | HTTP method for health checks (e.g. `GET`, `HEAD`) |
| `pangolin.ingress.k8s.io/healthcheck-status` | `int` | *(unset)* | Expected HTTP status code for a healthy response |
| `pangolin.ingress.k8s.io/healthcheck-tls-server-name` | `string` | *(unset)* | TLS server name for health check connections |

> **Note:** When `healthcheck-enabled` is `"true"`, the controller automatically fills in defaults for the five fields that Pangolin requires (`path`, `hostname`, `port`, `interval`, `method`). You only need to set `healthcheck-enabled: "true"` for a minimal working health check.

### Internal / Managed

| Annotation | Type | Description |
|------------|------|-------------|
| `pangolin.ingress.k8s.io/resource-id` | `string` | Automatically set by the controller to track the Pangolin resource ID |

### Example: Disable SSO

```yaml
apiVersion: networking.k8s.io/v1
kind: Ingress
metadata:
  name: public-app
  annotations:
    pangolin.ingress.k8s.io/sso: "false"
    pangolin.ingress.k8s.io/ssl: "true"
spec:
  ingressClassName: pangolin
  rules:
  - host: app.example.com
    http:
      paths:
      - path: /
        pathType: Prefix
        backend:
          service:
            name: app-service
            port:
              number: 80
```

### Example: Sticky Sessions and Custom Headers

```yaml
apiVersion: networking.k8s.io/v1
kind: Ingress
metadata:
  name: stateful-app
  annotations:
    pangolin.ingress.k8s.io/sticky-session: "true"
    pangolin.ingress.k8s.io/set-host-header: "internal.example.com"
    pangolin.ingress.k8s.io/headers: '[{"name":"X-Custom-Header","value":"my-value"}]'
spec:
  ingressClassName: pangolin
  rules:
  - host: stateful.example.com
    http:
      paths:
      - path: /
        pathType: Prefix
        backend:
          service:
            name: stateful-service
            port:
              number: 8080
```

### Example: Full Access Control

```yaml
apiVersion: networking.k8s.io/v1
kind: Ingress
metadata:
  name: protected-app
  annotations:
    pangolin.ingress.k8s.io/sso: "true"
    pangolin.ingress.k8s.io/ssl: "true"
    pangolin.ingress.k8s.io/apply-rules: "true"
    pangolin.ingress.k8s.io/email-whitelist-enabled: "true"
    pangolin.ingress.k8s.io/post-auth-path: "/dashboard"
spec:
  ingressClassName: pangolin
  rules:
  - host: protected.example.com
    http:
      paths:
      - path: /
        pathType: Prefix
        backend:
          service:
            name: protected-service
            port:
              number: 443
```

### Example: Resource Password + Whitelist + Roles

```yaml
apiVersion: v1
kind: Secret
metadata:
  name: app-shared-password
  namespace: default
stringData:
  password: hunter2-secret
---
apiVersion: networking.k8s.io/v1
kind: Ingress
metadata:
  name: gated-app
  namespace: default
  annotations:
    pangolin.ingress.k8s.io/sso: "true"
    pangolin.ingress.k8s.io/email-whitelist-enabled: "true"
    pangolin.ingress.k8s.io/email-whitelist: '["alice@example.com","*@partner.com"]'
    pangolin.ingress.k8s.io/password-secret-ref: "app-shared-password"
    pangolin.ingress.k8s.io/role-ids: "[1,4]"
    pangolin.ingress.k8s.io/skip-to-idp-id: "3"
spec:
  ingressClassName: pangolin
  rules:
  - host: gated.example.com
    http:
      paths:
      - path: /
        pathType: Prefix
        backend:
          service:
            name: gated-service
            port:
              number: 8080
```

### Example: Health Checks

```yaml
apiVersion: networking.k8s.io/v1
kind: Ingress
metadata:
  name: monitored-app
  annotations:
    pangolin.ingress.k8s.io/healthcheck-enabled: "true"
    pangolin.ingress.k8s.io/healthcheck-path: "/healthz"
    pangolin.ingress.k8s.io/healthcheck-interval: "30"
    pangolin.ingress.k8s.io/healthcheck-timeout: "5"
    pangolin.ingress.k8s.io/healthcheck-method: "GET"
    pangolin.ingress.k8s.io/healthcheck-status: "200"
spec:
  ingressClassName: pangolin
  rules:
  - host: monitored.example.com
    http:
      paths:
      - path: /
        pathType: Prefix
        backend:
          service:
            name: monitored-service
            port:
              number: 8080
```

## PangolinEndpoint (private endpoints)

An `Ingress` can only describe an HTTP resource reachable at a public hostname.
Pangolin also supports **private resources** — endpoints with no public
entrypoint at all, reachable only by clients connected to the Pangolin mesh at
an internal FQDN. Those are declared with the `PangolinEndpoint` custom
resource.

> **Alpha.** `pangolin.corelyr.com/v1alpha1` may change in
> backwards-incompatible ways. Only the private branch is implemented;
> `spec.public` is reserved and rejected.

```yaml
apiVersion: pangolin.corelyr.com/v1alpha1
kind: PangolinEndpoint
metadata:
  name: postgres
  namespace: data
spec:
  backendRef:
    name: postgres            # Service in the same namespace
  private:
    ports:
      - {protocol: TCP, port: 5432}
      - {protocol: TCP, from: 8000, to: 9000}
    access:
      roles: [developers]     # Pangolin names, not internal IDs
      clients: [my-laptop]
      users: [someone@example.com]
```

```console
$ kubectl get pangolinendpoints -n data
NAME       ADDRESS                       PORTS            READY   AGE
postgres   postgres.data.corp.internal   5432,8000-9000   True    30s
```

### Spec fields

| Field | Description |
|-------|-------------|
| `backendRef.name` | Service in the same namespace. Its cluster DNS name becomes the Pangolin destination. Headless and `ExternalName` Services are rejected |
| `siteRefs` | Pangolin site nice IDs. Defaults to `--pangolin-site-nice-id` |
| `enabled` | Whether Pangolin serves the endpoint. Defaults to `true` |
| `private.alias` | The internal FQDN clients dial. Derived when unset — see below |
| `private.ports[]` | `{protocol, port}`, `{protocol, from, to}` or `{protocol, all: true}`. Exactly one form per entry. **When omitted, the ports are taken from the backing Service** |
| `private.access.roles/clients/users` | Pangolin principal **names**; the controller resolves them to IDs |
| `private.disableIcmp` | Suppress ICMP to the destination |

### Alias derivation

When `spec.private.alias` is unset the controller derives
`<name>.<namespace>.<suffix>` using `--private-alias-suffix`.

**The suffix has no default and must be set.** Aliases are unique across a
Pangolin organization, so a shipped default would make two clusters sharing one
org collide on the same alias. Until it is set, endpoints without an explicit
alias report `Accepted=False` with reason `AliasSuffixNotConfigured`.

Changing the suffix rewrites the alias of every endpoint that derives one,
which changes the address clients dial.

### Ports follow the Service when unset

Leaving `private.ports` empty means "track the Service": the port set is
re-derived on every reconcile, so **adding a port to the Service widens the
endpoint** without any change to the `PangolinEndpoint`. `.status.resolvedPorts`
always shows what was actually sent. Set `private.ports` explicitly to pin the
exposure. SCTP Service ports are skipped — Pangolin private resources carry
only TCP and UDP.

### Status

| Condition | `False` means |
|-----------|---------------|
| `Accepted` | The spec cannot be acted on: no alias suffix configured, an identity that Pangolin cannot express, or an instance that does not implement private resources |
| `ResolvedRefs` | Something the spec points at is missing or ambiguous: the Service, a site, or a named role/user/client |
| `Programmed` | Pangolin rejected, or has not yet accepted, the configuration |
| `Ready` | Summary. `NoPrincipalsGranted` means the endpoint exists but grants access to nobody |

All four are operator-fixable conditions: they are reported as events and
requeued rather than returned as controller errors, so they do not ride
exponential backoff or inflate `controller_runtime_reconcile_errors_total`.

### Identity

The controller derives a deterministic Pangolin `niceId` of
`<resource-prefix>-<namespace>-<name>` and re-finds its resource by that ID if
`.status` is lost. It never claims a Pangolin resource by matching attributes,
so it cannot adopt one it does not own. A name that cannot be expressed as a
nice ID (anything outside `[a-zA-Z0-9-]`, e.g. a dot in the object name) is
refused rather than rewritten, since rewriting could collapse two endpoints
onto one identity.

## Configuration

### Controller Arguments

The controller accepts the following command-line arguments:

| Argument | Default | Description |
|----------|---------|-------------|
| `--ingress-class` | `pangolin` | The IngressClass this controller manages |
| `--pangolin-base-url` | `https://api.tunnel.tf` | Pangolin API base URL |
| `--pangolin-api-key-secret` | `pangolin-api-key` | Name of the secret containing the API key |
| `--pangolin-api-key-namespace` | `pangolin-system` | Namespace of the API key secret |
| `--pangolin-org-id` | _none_ | **Required** Pangolin organization identifier (e.g. `tunnel-tf`) |
| `--pangolin-site-nice-id` | _none_ | **Required** Pangolin site nice ID that should host created targets |
| `--resource-prefix` | `pangolin-controller` | Prefix for Pangolin resource names (resources are named `{prefix}-{host}`) |
| `--domain-cache-refresh-interval` | `60s` | How often at most the Pangolin domain list is refetched after an Ingress host fails to match. Bounds how long a domain registered after controller startup stays unresolvable. `0` disables refreshing (restart required to pick up new domains) |
| `--name-cache-refresh-interval` | `60s` | How often at most the Pangolin role and client lists are refetched after a principal named on a `PangolinEndpoint` fails to resolve. `0` disables refreshing |
| `--private-alias-suffix` | _none_ | DNS suffix used to derive a `PangolinEndpoint` alias as `<name>.<namespace>.<suffix>`. Required for endpoints that do not set `spec.private.alias`; deliberately has no default |
| `--cluster-domain` | `svc.cluster.local` | Cluster DNS suffix used to address backing Services |
| `--metrics-bind-address` | `:8080` | Address for Prometheus metrics endpoint |
| `--health-probe-bind-address` | `:8081` | Address for health/readiness probes |
| `--leader-elect` | `false` | Enable leader election for HA |

### Self-Hosted Pangolin

If you're using a self-hosted Pangolin instance, update the base URL (and optionally org/site IDs) in `deploy/deployment.yaml`:

```yaml
args:
- --pangolin-base-url=https://api.your-domain.com
- --pangolin-org-id=your-org
- --pangolin-site-nice-id=your-site
```

### Helm Values

When installing via Helm (`chart/values.yaml`), set the following:

```yaml
pangolin:
  baseUrl: https://api.tunnel.tf
  apiKeySecretName: pangolin-api-key
  apiKeyNamespace: pangolin-system
  orgId: tunnel-tf
  siteNiceId: decent-giant-pangolin
```

If `pangolin.createSecret=true`, also set `pangolin.apiKey` before installing so Helm can populate the secret. Otherwise, create your secret manually and set `createSecret=false`.

## Monitoring

### Prometheus Metrics

The controller exposes Prometheus metrics on `:8080/metrics`:

```bash
kubectl port-forward -n pangolin-system \
  svc/pangolin-ingress-controller-metrics 8080:8080

curl http://localhost:8080/metrics
```

### Health Checks

- **Liveness**: `http://localhost:8081/healthz`
- **Readiness**: `http://localhost:8081/readyz`

## Development

### Running Tests

```bash
make test
```

### Running Locally

Run the controller against your current kubeconfig context:

```bash
make run
```

### Code Formatting

```bash
make fmt
make vet
```

## Troubleshooting

### Check Controller Logs

```bash
kubectl logs -n pangolin-system \
  deployment/pangolin-ingress-controller -f
```

### Common Issues

1. **Ingress not being reconciled**: Ensure the IngressClass is set to `pangolin`
2. **Service not found errors**: Verify the backend service exists in the same namespace
3. **TLS secret errors**: Check that the secret exists and contains valid certificate data
4. **`no matching Pangolin domain` / `DomainNotFound` event**: The Ingress host does not correspond to any domain registered in your Pangolin org. Check with `kubectl describe ingress <name>`; the message reports how many domains the controller knows and when it last refreshed the list. The controller refetches the domain list automatically (see `--domain-cache-refresh-interval`), so a domain you *just* registered resolves within one interval without a restart — if it still does not resolve after that, the domain is genuinely absent or unverified in Pangolin.

> **Note on monitoring**: an unresolvable host is reported as a Warning `DomainNotFound` event and a bounded requeue, **not** as a reconcile error. It therefore does not increment `controller_runtime_reconcile_errors_total`. If you alert on that metric to catch misconfigured hosts, alert on the event instead.

### Debug Mode

Enable verbose logging:

```yaml
args:
- --zap-log-level=debug
- --zap-devel=true
```

## Contributing

Contributions are welcome! Please:

1. Fork the repository
2. Create a feature branch
3. Make your changes
4. Add tests
5. Submit a pull request

## Architecture Details

### Reconciliation Loop

The controller implements a standard Kubernetes reconciliation loop:

1. **Watch** for Ingress resource changes
2. **Filter** for Ingress resources with the `pangolin` IngressClass
3. **Initialize** Pangolin API client with credentials from secret
4. **Process** rules and validate backend services
5. **Create/Update** Pangolin resources and targets via API
6. **Add finalizers** to ensure proper cleanup
7. **Update** Ingress status and annotations

### Resource Lifecycle

**Creation:**
- Parse Ingress host into subdomain and domain
- Create Pangolin HTTP resource
- Create target pointing to Kubernetes service
- Store resource ID in Ingress annotations

**Deletion:**
- Detect Ingress deletion timestamp
- Delete Pangolin resource via API
- Remove finalizer to complete deletion

### High Availability

When leader election is enabled, multiple controller replicas can run simultaneously. Only the leader performs reconciliation, with automatic failover if the leader becomes unavailable.

## Roadmap

- [ ] Advanced load balancing algorithms
- [ ] Rate limiting and throttling
- [ ] Authentication/Authorization middleware
- [ ] WebSocket support
- [ ] gRPC backend support
- [x] Custom annotations for advanced configurations
- [ ] Integration with external load balancers
- [ ] Admission webhooks for validation

## License

MIT License - see LICENSE file for details

## Support

For issues, questions, or contributions:
- Open an issue on GitHub
- Check the examples directory for more use cases
- Review the documentation in the docs directory

## Acknowledgments

Built with:
- [controller-runtime](https://github.com/kubernetes-sigs/controller-runtime)
- [client-go](https://github.com/kubernetes/client-go)
- Kubernetes community tools and libraries
