# Feature: Multi-Host Ingress Support

**Spec ID:** 0001
**Status:** Draft
**Author:**
**Created:** 2026-03-13
**Priority:** Critical

## Summary

The controller currently stores a single `pangolin.ingress.k8s.io/resource-id` annotation per Ingress, which means only the last-created Pangolin resource ID is persisted. For Ingresses with multiple rules targeting different hosts, earlier resource IDs are silently overwritten, causing incorrect updates and orphaned resources on deletion. This feature adds proper multi-host tracking.

## Motivation

A standard Kubernetes Ingress can define multiple rules with different hosts (e.g., `api.example.com` and `web.example.com`). The current controller processes each rule and creates a separate Pangolin resource per host, but only persists the last resource ID in the annotation. On subsequent reconciliations, the controller calls `UpdateResource` using that single ID for all hosts -- corrupting earlier resources. On deletion, `deletePangolinResources` only deletes the single stored resource, leaking the others.

This is a correctness issue that affects any user with multi-rule Ingresses.

## Detailed Design

### Overview

Replace the single `resource-id` annotation with a JSON-encoded map that tracks resource IDs per host, enabling correct CRUD operations for all hosts in a multi-rule Ingress.

### API / Configuration Changes

**Annotation change:**
- Current: `pangolin.ingress.k8s.io/resource-id: "abc123"` (single string)
- Proposed: `pangolin.ingress.k8s.io/resource-ids: '{"api.example.com":"abc123","web.example.com":"def456"}'` (JSON map)

The old `resource-id` annotation should be supported for backwards compatibility during migration but deprecated.

### Implementation Details

1. **New annotation constant:** `annotationResourceIDs = "pangolin.ingress.k8s.io/resource-ids"` storing a `map[string]string` (host -> resourceNiceID) serialized as JSON.

2. **Migration path in `Reconcile()`:** If the old `resource-id` annotation is present and `resource-ids` is absent, migrate by mapping the single ID to the first rule's host, then remove the old annotation.

3. **`createOrUpdatePangolinResource()`:** After creating/updating a resource, update only the entry for that specific host in the map annotation, rather than overwriting the entire value.

4. **`deletePangolinResources()`:** Iterate over all entries in the `resource-ids` map and delete each resource from the Pangolin API. Only remove the finalizer after all deletions succeed.

5. **`processIngressRules()`:** Track which hosts were processed in the current reconciliation. After processing all rules, identify hosts in the annotation map that no longer have a corresponding rule and delete their Pangolin resources (handles rule removal).

6. **`updateIngressStatus()`:** Set multiple entries in `LoadBalancer.Ingress` status -- one per host/resource, rather than just the last one.

### Error Handling

- If deletion of one resource fails, continue attempting to delete others but requeue for retry.
- If annotation JSON is malformed, log a warning and re-initialize the map from scratch (rebuild by listing resources from API).
- Partial failures during multi-host creation should not block other hosts from being processed (remove the short-circuit behavior in `processIngressRules`).

## Alternatives Considered

1. **One Ingress per host:** Document that users should create separate Ingress resources per host. Rejected because it violates the Kubernetes Ingress spec and creates unnecessary operational burden.
2. **Separate annotations per host** (e.g., `resource-id-api-example-com`): More readable but annotation key length limits and special character escaping make this fragile.

## Testing Strategy

- Unit test: `processIngressRules` with 2+ rules, verify all resource IDs are stored in the map.
- Unit test: Deletion path with multiple resource IDs, verify all are deleted.
- Unit test: Migration from old `resource-id` to new `resource-ids` annotation.
- Unit test: Rule removal triggers orphan cleanup.
- Integration test: End-to-end with a multi-host Ingress against a mock API.

## Rollout Plan

- The migration from `resource-id` to `resource-ids` must be automatic and non-breaking.
- The old annotation should be supported for at least one minor version.
- Document the annotation change in the CHANGELOG and README.

## Open Questions

- Should there be a configurable limit on the number of hosts per Ingress?
- Should the controller emit a warning event when migrating from the old annotation format?
