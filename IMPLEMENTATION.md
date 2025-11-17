# Pangolin Ingress Controller Implementation

This document describes the implementation of the Pangolin Ingress Controller integration with the Pangolin proxy API.

## Overview

The Pangolin Ingress Controller automatically creates and manages resources in Pangolin proxy when Kubernetes Ingress resources are created, updated, or deleted. It uses the Pangolin REST API to synchronize state between Kubernetes and Pangolin.

## Architecture

### Components

1. **Pangolin API Client** (`internal/pangolin/`)
   - `client.go`: HTTP client with authentication
   - `resources.go`: Resource and target management

2. **Ingress Controller** (`internal/controller/ingress_controller.go`)
   - Watches Kubernetes Ingress resources
   - Creates/updates/deletes Pangolin resources
   - Manages finalizers for cleanup
   - Stores state in annotations

3. **Main Application** (`cmd/main.go`)
   - Initializes controller manager
   - Configures Pangolin client
   - Handles command-line arguments

## API Integration

### Pangolin API Endpoints

The controller uses the following Pangolin API endpoints:

- `POST /v1/resources` - Create a new resource
- `GET /v1/resources/{id}` - Get resource details
- `PUT /v1/resources/{id}` - Update a resource
- `DELETE /v1/resources/{id}` - Delete a resource
- `POST /v1/targets` - Create a new target
- `GET /v1/resources/{id}/targets` - List targets for a resource
- `DELETE /v1/targets/{id}` - Delete a target

### Authentication

The controller authenticates with the Pangolin API using a Bearer token:

```
Authorization: Bearer <api-key>
```

The API key is stored in a Kubernetes Secret and loaded at runtime.

## Resource Mapping

### Ingress → Pangolin Resource

When an Ingress is created:

```yaml
apiVersion: networking.k8s.io/v1
kind: Ingress
metadata:
  name: example-ingress
  namespace: default
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

The controller creates:

1. **Pangolin Resource**:
   - Name: `default-example-ingress-app`
   - Subdomain: `app`
   - Domain: `example.com`
   - Type: `http`
   - Enabled: `true`

2. **Pangolin Target**:
   - Host: `app-service.default.svc.cluster.local`
   - Port: `80`
   - Method: `http`
   - Weight: `100`
   - Enabled: `true`

### Metadata Storage

The controller stores metadata in Ingress annotations:

```yaml
metadata:
  annotations:
    pangolin.ingress.k8s.io/resource-id: "res_abc123xyz"
  finalizers:
  - pangolin.ingress.k8s.io/finalizer
```

## Lifecycle Management

### Creation Flow

1. User creates Ingress with `ingressClassName: pangolin`
2. Controller receives reconciliation event
3. Controller initializes Pangolin client (if needed)
4. Controller adds finalizer to Ingress
5. Controller parses host into subdomain and domain
6. Controller creates Pangolin resource via API
7. Controller stores resource ID in annotation
8. Controller creates Pangolin target via API
9. Controller updates Ingress status

### Update Flow

1. User updates Ingress
2. Controller receives reconciliation event
3. Controller retrieves resource ID from annotation
4. Controller updates Pangolin resource via API
5. Controller updates/creates targets as needed

### Deletion Flow

1. User deletes Ingress
2. Kubernetes sets deletion timestamp
3. Controller receives reconciliation event
4. Controller retrieves resource ID from annotation
5. Controller deletes Pangolin resource via API
6. Pangolin automatically deletes associated targets
7. Controller removes finalizer
8. Kubernetes completes Ingress deletion

## Configuration

### Controller Arguments

```bash
--ingress-class=pangolin                          # IngressClass to watch
--pangolin-base-url=https://api.pangolin.net      # Pangolin API URL
--pangolin-api-key-secret=pangolin-api-key        # Secret name
--pangolin-api-key-namespace=pangolin-system      # Secret namespace
--metrics-bind-address=:8080                      # Metrics endpoint
--health-probe-bind-address=:8081                 # Health endpoint
--leader-elect=false                              # Leader election
```

### API Key Secret

The API key must be stored in a Kubernetes Secret:

```yaml
apiVersion: v1
kind: Secret
metadata:
  name: pangolin-api-key
  namespace: pangolin-system
type: Opaque
data:
  api-key: <base64-encoded-api-key>
```

### Required API Permissions

The API key must have the following permissions:

- **Resource**: Create, Delete, Get, List, Update
- **Target**: Create, Delete, Get, List
- **Organization**: List Organizations, List Organization Domains
- **Site**: Get Site, List Sites

## Error Handling

### API Errors

The controller handles various API error scenarios:

- **401 Unauthorized**: Invalid API key
- **403 Forbidden**: Insufficient permissions
- **404 Not Found**: Resource doesn't exist
- **429 Too Many Requests**: Rate limiting
- **500 Server Error**: Pangolin API issues

On error, the controller:
1. Logs the error with context
2. Returns error to trigger requeue
3. Kubernetes automatically retries with exponential backoff

### Network Errors

Network connectivity issues are handled gracefully:
- Connection timeouts (30s default)
- DNS resolution failures
- TLS certificate errors

### Resource Conflicts

If a resource already exists with the same name:
- Controller attempts to update instead of create
- Metadata helps track ownership

## Security Considerations

### API Key Storage

- API key stored in Kubernetes Secret
- Secret mounted as environment variable or file
- Never logged or exposed in status

### RBAC Permissions

The controller requires:

```yaml
# Ingress resources
- apiGroups: ["networking.k8s.io"]
  resources: ["ingresses"]
  verbs: ["get", "list", "watch", "update", "patch"]

# Ingress status
- apiGroups: ["networking.k8s.io"]
  resources: ["ingresses/status"]
  verbs: ["get", "update", "patch"]

# Ingress finalizers
- apiGroups: ["networking.k8s.io"]
  resources: ["ingresses/finalizers"]
  verbs: ["update"]

# Services (for backend validation)
- apiGroups: [""]
  resources: ["services"]
  verbs: ["get", "list", "watch"]

# Secrets (for API key)
- apiGroups: [""]
  resources: ["secrets"]
  verbs: ["get", "list", "watch"]
```

### Network Security

- All API communication uses HTTPS
- TLS certificate validation enabled
- No sensitive data in logs

## Testing

### Unit Tests

Test coverage includes:
- Host parsing logic
- Resource creation/update logic
- Finalizer handling
- Error scenarios

### Integration Tests

To test the integration:

1. Deploy a test Pangolin instance
2. Create API key with required permissions
3. Deploy the controller
4. Create test Ingress resources
5. Verify resources in Pangolin dashboard
6. Delete Ingress and verify cleanup

### Manual Testing

```bash
# 1. Create test namespace
kubectl create namespace test-app

# 2. Deploy test application
kubectl apply -f examples/pangolin-ingress-example.yaml

# 3. Check controller logs
kubectl logs -n pangolin-system deployment/pangolin-ingress-controller -f

# 4. Verify Ingress
kubectl describe ingress example-ingress -n example-app

# 5. Check Pangolin dashboard for resources

# 6. Delete and verify cleanup
kubectl delete -f examples/pangolin-ingress-example.yaml
```

## Troubleshooting

### Common Issues

**Controller not starting:**
- Check API key secret exists
- Verify secret has correct key name (`api-key`)
- Check RBAC permissions

**Resources not created:**
- Verify IngressClass is `pangolin`
- Check controller logs for errors
- Verify API key permissions
- Test API connectivity

**Resources not deleted:**
- Check finalizer is present
- Verify controller is running
- Check controller logs for deletion errors
- Manually remove finalizer if needed

### Debug Commands

```bash
# Check controller status
kubectl get pods -n pangolin-system

# View controller logs
kubectl logs -n pangolin-system deployment/pangolin-ingress-controller -f

# Check Ingress details
kubectl describe ingress <name> -n <namespace>

# View Ingress annotations
kubectl get ingress <name> -n <namespace> -o yaml

# Test API connectivity
kubectl exec -n pangolin-system deployment/pangolin-ingress-controller -- \
  curl -H "Authorization: Bearer $API_KEY" https://api.pangolin.net/v1/resources
```

## Future Enhancements

### Planned Features

1. **TLS/SSL Support**: Automatic certificate management
2. **Path-based routing**: Support for multiple paths per host
3. **Load balancing**: Configure load balancing algorithms
4. **Health checks**: Configure target health checks
5. **Rate limiting**: Configure rate limits per resource
6. **Custom domains**: Support for custom domain configuration
7. **Site selection**: Allow specifying target Pangolin site
8. **Annotations**: Support for custom Pangolin configuration via annotations

### API Enhancements

1. **Batch operations**: Create multiple resources in one call
2. **Webhooks**: Receive notifications from Pangolin
3. **Status sync**: Sync resource status back to Ingress
4. **Metrics**: Expose Pangolin-specific metrics

## References

- [Pangolin Documentation](https://docs.pangolin.net)
- [Pangolin API Reference](https://api.pangolin.net/v1/docs)
- [Kubernetes Ingress](https://kubernetes.io/docs/concepts/services-networking/ingress/)
- [Controller Runtime](https://github.com/kubernetes-sigs/controller-runtime)
