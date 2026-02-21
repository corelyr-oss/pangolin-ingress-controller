# Pangolin Ingress Controller Helm Chart

This Helm chart deploys the Pangolin Ingress Controller to your Kubernetes cluster.

## Prerequisites

- Kubernetes 1.19+
- Helm 3.0+
- A valid Pangolin API key

## Installation

### Install from local chart

```bash
helm install pangolin-ingress-controller ./chart \
  --create-namespace \
  --namespace pangolin-system \
  --set pangolin.apiKey=YOUR_PANGOLIN_API_KEY
```

### Install with custom values

```bash
helm install pangolin-ingress-controller ./chart \
  --create-namespace \
  --namespace pangolin-system \
  --values custom-values.yaml
```

## Configuration

The following table lists the configurable parameters of the Pangolin Ingress Controller chart and their default values.

| Parameter | Description | Default |
|-----------|-------------|---------|
| `replicaCount` | Number of controller replicas | `1` |
| `image.repository` | Controller image repository | `repository.tf/kubernetes/pangolin-ingress-controller` |
| `image.tag` | Controller image tag | *(empty; falls back to chart appVersion)* |
| `image.pullPolicy` | Image pull policy | `IfNotPresent` |
| `pangolin.baseUrl` | Pangolin API base URL | `https://api.pangolin.net` |
| `pangolin.apiKey` | Pangolin API key (required if createSecret is true) | `YOUR_PANGOLIN_API_KEY_HERE` |
| `pangolin.createSecret` | Create a new secret for the API key | `true` |
| `pangolin.apiKeySecretName` | Name of secret containing API key | `pangolin-api-key` |
| `pangolin.apiKeyNamespace` | Namespace where the API key secret is stored | *(empty; defaults to release namespace)* |
| `controller.ingressClass` | Ingress class name | `pangolin` |
| `controller.resourcePrefix` | Prefix for Pangolin resource names | `pangolin-controller` |
| `controller.logLevel` | Log level: `info`, `debug`, `error` (or integer: 0=info, 1=debug, 2=trace) | `info` |
| `controller.leaderElect` | Enable leader election | `true` |
| `ingressClass.enabled` | Create IngressClass resource | `true` |
| `ingressClass.isDefault` | Set as default ingress class | `false` |
| `serviceAccount.create` | Create service account | `true` |
| `rbac.create` | Create RBAC resources | `true` |
| `service.enabled` | Create metrics service | `true` |
| `service.type` | Service type | `ClusterIP` |
| `service.port` | Service port | `8080` |
| `resources.limits.cpu` | CPU limit | `500m` |
| `resources.limits.memory` | Memory limit | `128Mi` |
| `resources.requests.cpu` | CPU request | `10m` |
| `resources.requests.memory` | Memory request | `64Mi` |

## Ingress Annotations

The controller reads the following annotations from Ingress resources to configure Pangolin resource settings. All annotations are optional — omit an annotation to leave the corresponding setting at its Pangolin default.

### SSO / Access Control

| Annotation | Type | Description |
|------------|------|-------------|
| `pangolin.ingress.k8s.io/sso` | `bool` | Enable or disable Pangolin SSO authentication |
| `pangolin.ingress.k8s.io/ssl` | `bool` | Enable or disable SSL termination |
| `pangolin.ingress.k8s.io/block-access` | `bool` | Block all access to the resource |
| `pangolin.ingress.k8s.io/email-whitelist-enabled` | `bool` | Enable email whitelist–based access control |
| `pangolin.ingress.k8s.io/apply-rules` | `bool` | Apply organization-level access rules |
| `pangolin.ingress.k8s.io/enabled` | `bool` | Enable or disable the Pangolin resource |

### Proxy Settings

| Annotation | Type | Description |
|------------|------|-------------|
| `pangolin.ingress.k8s.io/sticky-session` | `bool` | Enable sticky sessions (session affinity) |
| `pangolin.ingress.k8s.io/tls-server-name` | `string` | Override TLS server name for backend connections |
| `pangolin.ingress.k8s.io/set-host-header` | `string` | Override the Host header sent to the backend |
| `pangolin.ingress.k8s.io/post-auth-path` | `string` | Path to redirect to after authentication |
| `pangolin.ingress.k8s.io/headers` | `JSON` | Custom proxy headers as a JSON array: `'[{"name":"X-Foo","value":"bar"}]'` |

### Health Checks

| Annotation | Type | Description |
|------------|------|-------------|
| `pangolin.ingress.k8s.io/healthcheck-enabled` | `bool` | Enable health checks for the target |
| `pangolin.ingress.k8s.io/healthcheck-path` | `string` | HTTP path to probe (e.g. `/healthz`) |
| `pangolin.ingress.k8s.io/healthcheck-scheme` | `string` | Scheme for the health check (`http` or `https`) |
| `pangolin.ingress.k8s.io/healthcheck-mode` | `string` | Health check mode |
| `pangolin.ingress.k8s.io/healthcheck-hostname` | `string` | Hostname for the health check request |
| `pangolin.ingress.k8s.io/healthcheck-port` | `int` | Port to probe (defaults to target port) |
| `pangolin.ingress.k8s.io/healthcheck-interval` | `int` | Interval in seconds between checks (min 6) |
| `pangolin.ingress.k8s.io/healthcheck-unhealthy-interval` | `int` | Interval when unhealthy (min 6) |
| `pangolin.ingress.k8s.io/healthcheck-timeout` | `int` | Timeout in seconds per check (min 2) |
| `pangolin.ingress.k8s.io/healthcheck-headers` | `JSON` | Custom headers as JSON array: `'[{"name":"X-Foo","value":"bar"}]'` |
| `pangolin.ingress.k8s.io/healthcheck-follow-redirects` | `bool` | Follow HTTP redirects during checks |
| `pangolin.ingress.k8s.io/healthcheck-method` | `string` | HTTP method (e.g. `GET`, `HEAD`) |
| `pangolin.ingress.k8s.io/healthcheck-status` | `int` | Expected HTTP status code |
| `pangolin.ingress.k8s.io/healthcheck-tls-server-name` | `string` | TLS server name for health check connections |

### Examples

```yaml
apiVersion: networking.k8s.io/v1
kind: Ingress
metadata:
  name: my-app
  annotations:
    pangolin.ingress.k8s.io/sso: "false"
    pangolin.ingress.k8s.io/ssl: "true"
    pangolin.ingress.k8s.io/sticky-session: "true"
    pangolin.ingress.k8s.io/healthcheck-enabled: "true"
    pangolin.ingress.k8s.io/healthcheck-path: "/healthz"
    pangolin.ingress.k8s.io/healthcheck-interval: "30"
    pangolin.ingress.k8s.io/healthcheck-timeout: "5"
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

## Upgrading

To upgrade an existing release:

```bash
helm upgrade pangolin-ingress-controller ./chart \
  --namespace pangolin-system \
  --set pangolin.apiKey=YOUR_PANGOLIN_API_KEY
```

## Uninstalling

To uninstall/delete the deployment:

```bash
helm uninstall pangolin-ingress-controller --namespace pangolin-system
```

## Examples

### Install with custom resource limits

```bash
helm install pangolin-ingress-controller ./chart \
  --namespace pangolin-system \
  --set pangolin.apiKey=YOUR_API_KEY \
  --set resources.limits.cpu=1000m \
  --set resources.limits.memory=256Mi
```

### Install with multiple replicas

```bash
helm install pangolin-ingress-controller ./chart \
  --namespace pangolin-system \
  --set pangolin.apiKey=YOUR_API_KEY \
  --set replicaCount=3
```

### Set as default ingress class

```bash
helm install pangolin-ingress-controller ./chart \
  --namespace pangolin-system \
  --set pangolin.apiKey=YOUR_API_KEY \
  --set ingressClass.isDefault=true
```

### Use an existing secret for the API key

If you already have a secret containing your Pangolin API key, you can reference it instead of creating a new one:

```bash
# First, create your secret manually
kubectl create secret generic my-pangolin-secret \
  --from-literal=api-key=YOUR_API_KEY \
  --namespace pangolin-system

# Then install the chart referencing the existing secret
helm install pangolin-ingress-controller ./chart \
  --namespace pangolin-system \
  --set pangolin.createSecret=false \
  --set pangolin.apiKeySecretName=my-pangolin-secret
```

## Troubleshooting

### Check controller logs

```bash
kubectl logs -n pangolin-system -l app=pangolin-ingress-controller
```

### Check controller status

```bash
kubectl get pods -n pangolin-system
kubectl get ingressclass
```

### Verify API key secret

```bash
kubectl get secret pangolin-api-key -n pangolin-system
```
