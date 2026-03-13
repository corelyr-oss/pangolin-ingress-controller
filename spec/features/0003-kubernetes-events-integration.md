# Feature: Kubernetes Events Integration

**Spec ID:** 0003
**Status:** Draft
**Author:**
**Created:** 2026-03-13
**Priority:** High

## Summary

The controller has RBAC permissions to create Kubernetes Events (`events: create, patch`) but never emits any. All operational information is only available in controller logs. This feature adds Kubernetes Event emission for key reconciliation lifecycle events, making operational status visible via `kubectl describe ingress`.

## Motivation

Operators currently have no way to observe reconciliation activity without accessing controller logs. In production Kubernetes environments, `kubectl describe` is the primary debugging tool. Other ingress controllers (nginx, traefik, etc.) all emit events for resource creation, updates, errors, and status changes. Without events:

- Operators cannot see why an Ingress isn't working without log access.
- There is no audit trail of reconciliation activity on the Ingress resource.
- Monitoring systems that watch Kubernetes events cannot track Pangolin controller activity.
- RBAC for events is granted but wasted.

## Detailed Design

### Overview

Add an `EventRecorder` to the reconciler and emit events at key lifecycle points. Events should be concise, actionable, and follow Kubernetes conventions.

### API / Configuration Changes

No new annotations or CLI flags. The `record.EventRecorder` is provided by controller-runtime and is already available via the manager.

### Implementation Details

1. **Add `EventRecorder` to the reconciler struct:**
   ```go
   type IngressReconciler struct {
       client.Client
       Scheme   *runtime.Scheme
       Recorder record.EventRecorder
       // ... existing fields
   }
   ```

2. **Initialize in `cmd/main.go`:**
   ```go
   Recorder: mgr.GetEventRecorderFor("pangolin-ingress-controller"),
   ```

3. **Emit events at these points:**

   | Lifecycle Point | Type | Reason | Message |
   |---|---|---|---|
   | Pangolin resource created | Normal | `ResourceCreated` | `Created Pangolin resource "{name}" for host {host}` |
   | Pangolin resource updated | Normal | `ResourceUpdated` | `Updated Pangolin resource "{name}" for host {host}` |
   | Pangolin target created | Normal | `TargetCreated` | `Created target {svc}:{port} for resource "{name}"` |
   | Pangolin target updated | Normal | `TargetUpdated` | `Updated target {svc}:{port} for resource "{name}"` |
   | Stale target cleaned up | Normal | `TargetCleaned` | `Removed stale target {targetID} from resource "{name}"` |
   | Resource adopted (409 conflict) | Normal | `ResourceAdopted` | `Adopted existing Pangolin resource "{name}"` |
   | Resource deleted | Normal | `ResourceDeleted` | `Deleted Pangolin resource "{name}"` |
   | Ingress status updated | Normal | `StatusUpdated` | `Updated LoadBalancer status with IP {ip}` |
   | Finalizer added | Normal | `FinalizerAdded` | `Added cleanup finalizer` |
   | API error (non-transient) | Warning | `SyncFailed` | `Failed to sync with Pangolin API: {error}` |
   | Invalid annotation | Warning | `InvalidAnnotation` | `Invalid value for annotation {key}: {error}` |
   | Client initialization failed | Warning | `ClientInitFailed` | `Failed to initialize Pangolin client: {error}` |
   | Domain resolution failed | Warning | `DomainNotFound` | `Could not resolve domain "{domain}" in Pangolin` |

4. **Event deduplication:** controller-runtime's EventRecorder already handles deduplication (same reason + message within a time window), so no additional logic is needed.

### Error Handling

- Event emission should never cause reconciliation failure. If `Recorder.Event()` fails, it is silently ignored (this is the standard controller-runtime behavior).
- Warning events should include enough context (resource name, host, error message) to be actionable without logs.

## Alternatives Considered

1. **Kubernetes Conditions on Ingress status:** More structured but non-standard for Ingress resources (Conditions are part of Gateway API, not Ingress). Could be added later.
2. **Only emit Warning events:** Simpler but loses the audit trail of successful operations.

## Testing Strategy

- Unit test: Mock the EventRecorder and verify events are emitted at each lifecycle point.
- Unit test: Verify event messages contain expected fields (host, resource name, error).
- Verify events are visible via `kubectl describe ingress` in a running cluster.

## Rollout Plan

- Additive change, no backwards compatibility concerns.
- No new RBAC required (events permission already granted).
- Document event reasons in README.

## Open Questions

- Should event verbosity be configurable (e.g., only warnings, or all events)?
- Should events include the Pangolin resource ID for correlation?
