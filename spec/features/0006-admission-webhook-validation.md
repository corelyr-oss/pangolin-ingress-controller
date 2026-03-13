# Feature: Admission Webhook for Annotation Validation

**Spec ID:** 0006
**Status:** Draft
**Author:**
**Created:** 2026-03-13
**Priority:** Medium

## Summary

Invalid Pangolin annotations (malformed booleans, non-numeric integers, invalid JSON for headers) are silently ignored by the controller -- the parse functions return `nil` on error with no warning. This feature adds a validating admission webhook that rejects Ingress resources with invalid Pangolin annotations at admission time, giving users immediate feedback.

## Motivation

Currently, if a user sets `pangolin.ingress.k8s.io/healthcheck-interval: "not-a-number"`, the annotation is silently ignored and the default value (or no value) is used. The user has no way to know their configuration was invalid without inspecting the resulting Pangolin resource via the API. This is a poor user experience that leads to:

1. **Silent misconfiguration:** Users believe their settings are applied when they are not.
2. **Debugging difficulty:** No events, no logs (parse errors aren't logged), no indication of the problem.
3. **Inconsistent state:** The Ingress annotation says one thing, but the Pangolin resource has different settings.

The README roadmap lists "Admission webhooks for validation" as a planned feature.

## Detailed Design

### Overview

Implement a Kubernetes `ValidatingWebhookConfiguration` that intercepts Ingress CREATE and UPDATE operations and validates all `pangolin.ingress.k8s.io/*` annotations against their expected types and allowed values.

### API / Configuration Changes

New CLI flags:

| Flag | Default | Description |
|---|---|---|
| `--enable-webhook` | `false` | Enable the admission webhook server |
| `--webhook-port` | `9443` | Port for the webhook HTTPS server |
| `--webhook-cert-dir` | `/tmp/k8s-webhook-server/serving-certs` | Directory containing TLS cert and key |

Helm values:
```yaml
webhook:
  enabled: false
  port: 9443
  certManager:
    enabled: true
    issuerRef:
      name: selfsigned-issuer
      kind: ClusterIssuer
```

### Implementation Details

1. **Validation rules per annotation:**

   | Annotation | Validation |
   |---|---|
   | `sso`, `ssl`, `block-access`, `email-whitelist-enabled`, `apply-rules`, `sticky-session`, `enabled`, `healthcheck-enabled`, `healthcheck-follow-redirects` | Must be `"true"` or `"false"` |
   | `healthcheck-port`, `healthcheck-interval`, `healthcheck-unhealthy-interval`, `healthcheck-timeout`, `healthcheck-status` | Must be a valid positive integer |
   | `headers`, `healthcheck-headers` | Must be valid JSON array of `[{"name":"...","value":"..."}]` |
   | `healthcheck-method` | Must be a valid HTTP method (`GET`, `HEAD`, `POST`, `OPTIONS`) |
   | `healthcheck-scheme` | Must be `http` or `https` |
   | `rewrite-path-type` (if spec 0002 is implemented) | Must be `exact`, `prefix`, or `regex` |

2. **Webhook handler** in a new package `internal/webhook/`:
   ```go
   type IngressValidator struct {
       IngressClass string
   }
   
   func (v *IngressValidator) Handle(ctx context.Context, req admission.Request) admission.Response {
       // Decode Ingress
       // Check if managed by this controller (same isManaged logic)
       // If not managed, allow (don't block other controllers)
       // Validate all pangolin.ingress.k8s.io/* annotations
       // Return Allowed or Denied with specific field errors
   }
   ```

3. **Share validation logic** between the webhook and the controller. Extract annotation parsing into a `validate` package that both can import, so validation rules are defined once.

4. **TLS certificate management:** Support cert-manager for automatic TLS certificate provisioning. Also support manual cert injection for environments without cert-manager.

5. **Helm chart additions:**
   - `ValidatingWebhookConfiguration` resource
   - `Service` for the webhook endpoint
   - cert-manager `Certificate` and `Issuer` resources (conditional)
   - Updated `Deployment` with webhook port and volume mounts

### Error Handling

- Webhook failures should be configured with `failurePolicy: Ignore` by default to prevent the webhook from blocking Ingress operations if the controller is down. Users can set `failurePolicy: Fail` for stricter enforcement.
- Validation errors should return all invalid fields at once (not fail on the first one), so users can fix all issues in a single edit.

## Alternatives Considered

1. **Log warnings instead of rejecting:** Less disruptive but doesn't solve the core problem -- users still won't notice warnings in controller logs.
2. **Controller-side validation with events:** Validate in the reconcile loop and emit Warning events (see spec 0003). This is complementary and should be done regardless, but admission-time rejection is a better UX.
3. **CRD with CEL validation:** Define a Pangolin-specific CRD instead of using Ingress annotations. Provides native schema validation but abandons the standard Ingress resource model.

## Testing Strategy

- Unit test: Each validation rule with valid and invalid inputs.
- Unit test: Webhook handler allows non-managed Ingresses.
- Unit test: Multiple validation errors returned together.
- Integration test: Deploy webhook to a test cluster, verify invalid annotations are rejected.
- Helm template test: Verify webhook resources are generated when enabled.

## Rollout Plan

- Webhook is disabled by default (`--enable-webhook=false`).
- Users opt-in via Helm value or CLI flag.
- `failurePolicy: Ignore` by default for safety.
- Document cert-manager integration in SETUP.md.

## Open Questions

- Should the webhook also validate that referenced backend Services exist?
- Should there be a "dry-run" mode that logs validation failures but allows the resource?
- Which cert-manager issuers should be supported in the Helm chart?
