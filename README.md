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
- **Pangolin API key** with resource management permissions
- Docker (for building images)
- Go 1.21+ (for development)

## Installation

### Quick Start

1. **Create a Pangolin API key:**

   - Log in to your Pangolin dashboard
   - Navigate to **Organization → API Keys**
   - Create a new API key with resource management permissions
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
| `pangolin.ingress.k8s.io/email-whitelist-enabled` | `bool` | *(unset)* | Enable email whitelist–based access control |
| `pangolin.ingress.k8s.io/apply-rules` | `bool` | *(unset)* | Apply organization-level access rules to the resource |
| `pangolin.ingress.k8s.io/enabled` | `bool` | *(unset)* | Enable or disable the Pangolin resource entirely |

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
| `pangolin.ingress.k8s.io/healthcheck-path` | `string` | *(unset)* | HTTP path to probe (e.g. `/healthz`) |
| `pangolin.ingress.k8s.io/healthcheck-scheme` | `string` | *(unset)* | Scheme for the health check (`http` or `https`) |
| `pangolin.ingress.k8s.io/healthcheck-mode` | `string` | *(unset)* | Health check mode |
| `pangolin.ingress.k8s.io/healthcheck-hostname` | `string` | *(unset)* | Hostname to use in the health check request |
| `pangolin.ingress.k8s.io/healthcheck-port` | `int` | *(unset)* | Port to probe (defaults to the target port) |
| `pangolin.ingress.k8s.io/healthcheck-interval` | `int` | *(unset)* | Interval in seconds between checks (min 6) |
| `pangolin.ingress.k8s.io/healthcheck-unhealthy-interval` | `int` | *(unset)* | Interval in seconds between checks when unhealthy (min 6) |
| `pangolin.ingress.k8s.io/healthcheck-timeout` | `int` | *(unset)* | Timeout in seconds for each check (min 2) |
| `pangolin.ingress.k8s.io/healthcheck-headers` | `JSON` | *(unset)* | Custom headers for health check requests (JSON array) |
| `pangolin.ingress.k8s.io/healthcheck-follow-redirects` | `bool` | *(unset)* | Follow HTTP redirects during health checks |
| `pangolin.ingress.k8s.io/healthcheck-method` | `string` | *(unset)* | HTTP method for health checks (e.g. `GET`, `HEAD`) |
| `pangolin.ingress.k8s.io/healthcheck-status` | `int` | *(unset)* | Expected HTTP status code for a healthy response |
| `pangolin.ingress.k8s.io/healthcheck-tls-server-name` | `string` | *(unset)* | TLS server name for health check connections |

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
