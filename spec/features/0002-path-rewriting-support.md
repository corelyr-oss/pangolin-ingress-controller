# Feature: Path Rewriting and Target Priority Support

**Spec ID:** 0002
**Status:** Draft
**Author:**
**Created:** 2026-03-13
**Priority:** High

## Summary

The Pangolin API supports `rewritePath`, `rewritePathType`, and `priority` fields on targets, and the Go structs in `internal/pangolin/resources.go` already define these fields. However, the controller never populates them and no annotations exist to configure them. This feature exposes these capabilities to users via new annotations.

## Motivation

Path rewriting is a fundamental ingress controller capability. Users commonly need to strip path prefixes (e.g., route `/api/v1/*` to `/v1/*` on the backend) or rewrite paths entirely. Without this, the controller is limited to pass-through routing only, which is insufficient for microservice architectures where services don't share the same URL structure as the public API.

Target priority is needed when multiple targets exist for the same resource (e.g., canary deployments or A/B testing) to control traffic ordering.

## Detailed Design

### Overview

Add three new annotations that map directly to the existing `CreateTargetRequest` struct fields.

### API / Configuration Changes

New annotations:

| Annotation | Type | Default | Description |
|---|---|---|---|
| `pangolin.ingress.k8s.io/rewrite-path` | string | (none) | Target path to rewrite to |
| `pangolin.ingress.k8s.io/rewrite-path-type` | string | (none) | Rewrite match type: `exact`, `prefix`, `regex` |
| `pangolin.ingress.k8s.io/target-priority` | int | 0 | Target routing priority (higher = preferred) |

### Implementation Details

1. **New annotation constants** in `ingress_controller.go`:
   ```go
   annotationRewritePath     = "pangolin.ingress.k8s.io/rewrite-path"
   annotationRewritePathType = "pangolin.ingress.k8s.io/rewrite-path-type"
   annotationTargetPriority  = "pangolin.ingress.k8s.io/target-priority"
   ```

2. **In `createOrUpdatePangolinResource()`**, when building the `CreateTargetRequest`, populate:
   - `RewritePath` from `parseStringAnnotation(annotationRewritePath)`
   - `RewritePathType` from `parseStringAnnotation(annotationRewritePathType)`
   - `Priority` from `parseIntAnnotation(annotationTargetPriority)` (defaulting to 0)

3. **Validation:** `rewritePathType` should be validated against known values (`exact`, `prefix`, `regex`). If an invalid value is provided, log a warning and skip setting the field.

4. **Update the `pangolinAnnotationChangedPredicate`** to include these new annotation keys in the change detection.

### Error Handling

- Invalid `rewrite-path-type` values should be logged as warnings and ignored (not cause reconciliation failure).
- If `rewrite-path` is set without `rewrite-path-type`, default to `prefix`.

## Alternatives Considered

1. **Use the Ingress `pathType` field for rewrite type:** Rejected because `pathType` controls matching, not rewriting -- they are independent concerns.
2. **Use nginx-style rewrite annotations:** Rejected in favor of staying consistent with Pangolin API terminology.

## Testing Strategy

- Unit test: Verify `CreateTargetRequest` is populated with rewrite fields when annotations are present.
- Unit test: Verify defaults when `rewrite-path-type` is omitted but `rewrite-path` is set.
- Unit test: Verify invalid `rewrite-path-type` is handled gracefully.
- Add examples to `examples/ingress-examples.md` showing path rewriting.

## Rollout Plan

- Additive change, no backwards compatibility concerns.
- Add to README annotation reference table.
- Add an example to the examples directory.

## Open Questions

- What are the valid values for `rewritePathType` in the Pangolin API? Need to verify against API documentation.
- Should `rewrite-path` support templating (e.g., capture groups from regex matches)?
