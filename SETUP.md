# Pangolin Ingress Controller Setup Guide

This guide will help you set up the Pangolin Ingress Controller to automatically create and manage resources in your Pangolin proxy instance.

## Prerequisites

- Kubernetes cluster (1.24+)
- kubectl configured to access your cluster
- A Pangolin instance (self-hosted or cloud)
- Pangolin API key with the following permissions:
  - Resource: Create, Delete, Get, List, Update
  - Target: Create, Delete, Get, List
  - Organization: List Organizations, List Organization Domains
  - Site: Get Site, List Sites

## Step 1: Create Pangolin API Key

1. Log in to your Pangolin dashboard
2. Navigate to **Organization → API Keys** (for organization keys) or **Server Admin → API Keys** (for root keys on self-hosted)
3. Click **Generate a new key**
4. Configure the required permissions (listed above)
5. Copy the API key (you won't be able to see it again)

## Step 2: Create Kubernetes Secret

Create a secret containing your Pangolin API key:

```bash
kubectl create secret generic pangolin-api-key \
  --from-literal=api-key=YOUR_PANGOLIN_API_KEY_HERE \
  --namespace=pangolin-system
```

Or edit the `deploy/pangolin-api-secret.yaml` file and apply it:

```bash
# Edit the file and replace YOUR_PANGOLIN_API_KEY_HERE with your actual API key
kubectl apply -f deploy/pangolin-api-secret.yaml
```

## Step 3: Configure Pangolin Base URL (Optional)

If you're using a self-hosted Pangolin instance, update the `--pangolin-base-url` argument in `deploy/deployment.yaml`:

```yaml
args:
- --pangolin-base-url=https://api.your-pangolin-domain.com
```

For Pangolin Cloud, the default `https://api.pangolin.net` is correct.

## Step 4: Deploy the Controller

Deploy all components to your cluster:

```bash
kubectl apply -f deploy/
```

This will create:
- Namespace: `pangolin-system`
- ServiceAccount, Role, RoleBinding, ClusterRole, ClusterRoleBinding
- Deployment: `pangolin-ingress-controller`
- Service: `pangolin-ingress-controller-metrics`
- IngressClass: `pangolin`
- Secret: `pangolin-api-key` (if using the manifest)

## Step 5: Verify Installation

Check that the controller is running:

```bash
kubectl get pods -n pangolin-system
```

You should see:
```
NAME                                          READY   STATUS    RESTARTS   AGE
pangolin-ingress-controller-xxxxxxxxx-xxxxx   1/1     Running   0          30s
```

Check the logs:

```bash
kubectl logs -n pangolin-system deployment/pangolin-ingress-controller -f
```

Verify the IngressClass:

```bash
kubectl get ingressclass
```

You should see:
```
NAME       CONTROLLER                    PARAMETERS   AGE
pangolin   pangolin.ingress.k8s.io       <none>       1m
```

## Step 6: Create Your First Ingress

Create an Ingress resource using the `pangolin` IngressClass:

```yaml
apiVersion: networking.k8s.io/v1
kind: Ingress
metadata:
  name: my-app-ingress
  namespace: default
spec:
  ingressClassName: pangolin
  rules:
  - host: myapp.example.com
    http:
      paths:
      - path: /
        pathType: Prefix
        backend:
          service:
            name: my-app-service
            port:
              number: 80
```

Apply the Ingress:

```bash
kubectl apply -f my-ingress.yaml
```

## Step 7: Verify Pangolin Resources

The controller will automatically:

1. Create a resource in Pangolin with the subdomain and domain from your Ingress host
2. Create a target pointing to your Kubernetes service
3. Add a finalizer to the Ingress for cleanup
4. Store the Pangolin resource ID in the Ingress annotations

Check the Ingress status:

```bash
kubectl describe ingress my-app-ingress -n default
```

You should see:
- The `pangolin.ingress.k8s.io/finalizer` in the finalizers
- The `pangolin.ingress.k8s.io/resource-id` annotation with the Pangolin resource ID

Log in to your Pangolin dashboard and verify that the resource and target were created.

## How It Works

### Resource Creation

When you create an Ingress with `ingressClassName: pangolin`:

1. The controller parses the host (e.g., `myapp.example.com`) into subdomain (`myapp`) and domain (`example.com`)
2. Creates a Pangolin HTTP resource with the subdomain and domain
3. Creates a target pointing to `<service-name>.<namespace>.svc.cluster.local:<port>`
4. Stores the Pangolin resource ID in the Ingress annotations

### Resource Deletion

When you delete an Ingress:

1. The controller detects the deletion timestamp
2. Retrieves the Pangolin resource ID from annotations
3. Deletes the resource from Pangolin (targets are deleted automatically)
4. Removes the finalizer to allow Kubernetes to delete the Ingress

### Target Configuration

Targets are automatically configured to point to your Kubernetes services using the cluster-internal DNS name:

```
<service-name>.<namespace>.svc.cluster.local
```

This assumes your Pangolin instance can reach your Kubernetes cluster's internal network. For external access, you may need to:

- Use a Pangolin site/tunnel connected to your cluster
- Configure external endpoints
- Use NodePort or LoadBalancer services

## Configuration Options

### Controller Arguments

| Argument | Default | Description |
|----------|---------|-------------|
| `--ingress-class` | `pangolin` | The IngressClass this controller manages |
| `--pangolin-base-url` | `https://api.pangolin.net` | Pangolin API base URL |
| `--pangolin-api-key-secret` | `pangolin-api-key` | Name of the secret containing the API key |
| `--pangolin-api-key-namespace` | `pangolin-system` | Namespace of the API key secret |
| `--metrics-bind-address` | `:8080` | Address for Prometheus metrics |
| `--health-probe-bind-address` | `:8081` | Address for health probes |
| `--leader-elect` | `false` | Enable leader election for HA |

### Annotations

| Annotation | Description |
|------------|-------------|
| `pangolin.ingress.k8s.io/resource-id` | Stores the Pangolin resource ID (managed by controller) |

### Finalizers

| Finalizer | Description |
|-----------|-------------|
| `pangolin.ingress.k8s.io/finalizer` | Ensures Pangolin resources are deleted before Ingress removal |

## Troubleshooting

### Controller Not Starting

Check the logs:
```bash
kubectl logs -n pangolin-system deployment/pangolin-ingress-controller
```

Common issues:
- **API key secret not found**: Ensure the secret exists in the correct namespace
- **Invalid API key**: Verify the API key is correct and has the required permissions
- **Network connectivity**: Ensure the controller can reach the Pangolin API

### Resources Not Created

1. Verify the Ingress has the correct IngressClass:
   ```bash
   kubectl get ingress -A
   ```

2. Check controller logs for errors:
   ```bash
   kubectl logs -n pangolin-system deployment/pangolin-ingress-controller -f
   ```

3. Verify the backend service exists:
   ```bash
   kubectl get svc -n <namespace>
   ```

### Resources Not Deleted

If Pangolin resources aren't deleted when you delete an Ingress:

1. Check if the finalizer is present:
   ```bash
   kubectl get ingress <name> -n <namespace> -o yaml | grep finalizers
   ```

2. Check controller logs for deletion errors

3. Manually delete the resource from Pangolin dashboard if needed

4. Remove the finalizer to allow Kubernetes to delete the Ingress:
   ```bash
   kubectl patch ingress <name> -n <namespace> -p '{"metadata":{"finalizers":[]}}' --type=merge
   ```

## Examples

See the `examples/` directory for complete examples:

- `examples/pangolin-ingress-example.yaml` - Basic HTTP ingress
- `examples/sample-app.yaml` - Complete application with ingress

## Security Considerations

1. **API Key Storage**: The API key is stored in a Kubernetes Secret. Ensure your cluster has appropriate RBAC policies.

2. **API Key Permissions**: Use the principle of least privilege. Only grant the permissions required for the controller to function.

3. **Network Access**: Ensure only authorized components can access the Pangolin API.

4. **TLS/HTTPS**: Always use HTTPS for the Pangolin API (default).

## Next Steps

- Configure multiple Ingress resources for different applications
- Set up monitoring with Prometheus metrics
- Enable leader election for high availability
- Integrate with your CI/CD pipeline

## Support

For issues or questions:
- Check the [Pangolin documentation](https://docs.pangolin.net)
- Open an issue on GitHub
- Join the Pangolin community on [Slack](https://pangolin.net/slack) or [Discord](https://pangolin.net/discord)
