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
