# Feature: Helm Chart Production Readiness

**Spec ID:** 0008
**Status:** Draft
**Author:**
**Created:** 2026-03-13
**Priority:** Medium

## Summary

The Helm chart is functional but missing several production-grade Kubernetes features: PodDisruptionBudget, HorizontalPodAutoscaler, ServiceMonitor for Prometheus Operator, NetworkPolicy, topology spread constraints, extra environment variables/volumes passthrough, and priority class configuration. This feature brings the chart to production readiness.

## Motivation

The controller is deployed as a critical infrastructure component -- if it goes down, new Ingress resources stop being provisioned and updates stop being applied. Production deployments need:

- **PodDisruptionBudget (PDB):** Prevents voluntary disruptions (node drains, upgrades) from taking down all controller replicas simultaneously.
- **HorizontalPodAutoscaler (HPA):** Scales the controller based on CPU/memory when managing many Ingress resources.
- **ServiceMonitor:** Integrates with Prometheus Operator (the de facto standard for Kubernetes monitoring) so that metrics at `:8080/metrics` are automatically scraped.
- **NetworkPolicy:** Restricts network access to only what the controller needs (Kubernetes API, Pangolin API, metrics scraping).
- **Extra env/volumes:** Allows users to inject custom configuration (e.g., proxy settings, CA certificates) without forking the chart.

These are standard features in mature Helm charts (e.g., ingress-nginx, cert-manager, external-dns).

## Detailed Design

### Overview

Add optional Kubernetes resources to the Helm chart, all disabled by default, with corresponding `values.yaml` entries.

### API / Configuration Changes

New `values.yaml` sections:

```yaml
# Pod Disruption Budget
podDisruptionBudget:
  enabled: false
  minAvailable: 1
  # maxUnavailable: 1

# Horizontal Pod Autoscaler
autoscaling:
  enabled: false
  minReplicas: 2
  maxReplicas: 5
  targetCPUUtilizationPercentage: 80
  targetMemoryUtilizationPercentage: 80

# Prometheus ServiceMonitor
serviceMonitor:
  enabled: false
  namespace: ""
  interval: 30s
  scrapeTimeout: 10s
  labels: {}
  metricRelabelings: []
  relabelings: []

# Network Policy
networkPolicy:
  enabled: false
  # Allow egress to Kubernetes API and Pangolin API
  egressRules: []
  # Allow ingress for metrics scraping and webhook (if enabled)
  ingressRules: []

# Topology spread constraints
topologySpreadConstraints: []

# Priority class
priorityClassName: ""

# Extra environment variables
extraEnv: []

# Extra volumes and volume mounts
extraVolumes: []
extraVolumeMounts: []

# Extra labels and annotations for all resources
commonLabels: {}
commonAnnotations: {}
```

### Implementation Details

#### 1. PodDisruptionBudget (`templates/pdb.yaml`)

```yaml
{{- if .Values.podDisruptionBudget.enabled }}
apiVersion: policy/v1
kind: PodDisruptionBudget
metadata:
  name: {{ include "chart.fullname" . }}
  labels:
    {{- include "chart.labels" . | nindent 4 }}
spec:
  selector:
    matchLabels:
      {{- include "chart.selectorLabels" . | nindent 6 }}
  {{- with .Values.podDisruptionBudget.minAvailable }}
  minAvailable: {{ . }}
  {{- end }}
  {{- with .Values.podDisruptionBudget.maxUnavailable }}
  maxUnavailable: {{ . }}
  {{- end }}
{{- end }}
```

#### 2. HorizontalPodAutoscaler (`templates/hpa.yaml`)

Standard HPA v2 resource targeting the controller Deployment. When `autoscaling.enabled=true`, the `replicaCount` value is used as `minReplicas` fallback.

#### 3. ServiceMonitor (`templates/servicemonitor.yaml`)

Standard Prometheus Operator ServiceMonitor targeting the metrics Service. Requires the `monitoring.coreos.com/v1` API group.

#### 4. NetworkPolicy (`templates/networkpolicy.yaml`)

Default policy when enabled:
- **Ingress:** Allow port 8080 (metrics) from Prometheus, allow port 9443 (webhook) from API server.
- **Egress:** Allow port 443 to Kubernetes API server, allow port 443 to Pangolin API, allow DNS (port 53).

#### 5. Deployment Enhancements

Update `templates/deployment.yaml` to include:
- `topologySpreadConstraints` from values
- `priorityClassName` from values
- `extraEnv` appended to container env
- `extraVolumes` and `extraVolumeMounts` appended to pod spec

#### 6. Common Labels/Annotations

Add `commonLabels` and `commonAnnotations` to all resource metadata via the `_helpers.tpl` template.

### Error Handling

- All new resources are gated by `.enabled` flags, so existing deployments are unaffected.
- Helm template tests should validate that resources are not rendered when disabled.

## Alternatives Considered

1. **Kustomize overlays instead of Helm values:** Some users prefer Kustomize, but the project already uses Helm as the primary deployment method. Kustomize support could be added separately.
2. **Operator pattern with CRD:** A PangolinIngressController CRD could manage these operational concerns. Overkill for the current scope.

## Testing Strategy

- `helm template` tests: Verify each resource is rendered only when enabled.
- `helm template` tests: Verify resources are not rendered when disabled.
- `helm lint`: Verify chart passes linting with various value combinations.
- `kubeconform`: Validate rendered manifests against Kubernetes schemas (already in CI).
- Test matrix: default values, all features enabled, each feature individually.

## Rollout Plan

- All features disabled by default -- zero impact on existing deployments.
- Bump chart minor version.
- Document each feature in `chart/README.md` with examples.
- Add a "Production Configuration" section to the main README.

## Open Questions

- Should the chart include a Grafana dashboard ConfigMap for the controller's metrics?
- Should NetworkPolicy default egress rules be pre-configured for the Pangolin API domain, or left to the user?
- Should the HPA support custom metrics (e.g., reconciliation queue depth) in addition to CPU/memory?
