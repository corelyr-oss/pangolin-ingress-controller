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

3. **Build the Docker image:**

```bash
make docker-build IMG=your-registry/pangolin-ingress-controller:latest
```

4. **Push to your registry:**

```bash
make docker-push IMG=your-registry/pangolin-ingress-controller:latest
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

## Configuration

### Controller Arguments

The controller accepts the following command-line arguments:

| Argument | Default | Description |
|----------|---------|-------------|
| `--ingress-class` | `pangolin` | The IngressClass this controller manages |
| `--pangolin-base-url` | `https://api.pangolin.net` | Pangolin API base URL |
| `--pangolin-api-key-secret` | `pangolin-api-key` | Name of the secret containing the API key |
| `--pangolin-api-key-namespace` | `pangolin-system` | Namespace of the API key secret |
| `--metrics-bind-address` | `:8080` | Address for Prometheus metrics endpoint |
| `--health-probe-bind-address` | `:8081` | Address for health/readiness probes |
| `--leader-elect` | `false` | Enable leader election for HA |

### Self-Hosted Pangolin

If you're using a self-hosted Pangolin instance, update the base URL in `deploy/deployment.yaml`:

```yaml
args:
- --pangolin-base-url=https://api.your-domain.com
```

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
- [ ] Custom annotations for advanced configurations
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
