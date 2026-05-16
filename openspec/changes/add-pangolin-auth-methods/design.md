## Context

The controller currently sets a fixed set of resource fields via `UpdateResourceRequest` in `internal/pangolin/resources.go` and exposes a corresponding set of `pangolin.ingress.k8s.io/*` annotations. Pangolin's resource auth model is split across two API surfaces:

1. **Fields on the resource update payload** (`POST /v1/resource/{id}`): `sso`, `ssl`, `blockAccess`, `emailWhitelistEnabled`, `applyRules`, `enabled`, plus `skipToIdpId` (missing).
2. **Separate sub-resource endpoints** under `/v1/resource/{id}/...`: `password`, `pincode`, `whitelist`, `roles`, `users`. These are stateful — each is its own `GET`/`POST` pair — and three of them (password, pincode, whitelist) hold credentials.

The reconcile loop in `IngressReconciler.Reconcile` today does: init client → fetch Ingress → handle finalizer → `processIngressRules` (which calls `createOrUpdatePangolinResource`) → `updateIngressStatus`. The resource auth methods need a new reconcile step that runs after the resource ID is known and before status update.

Stakeholders: users running GitOps/IaC who want the Ingress to be the only place auth is configured. Today they have to dual-manage (Ingress for routing, Pangolin UI for auth).

## Goals / Non-Goals

**Goals:**
- Declarative configuration of `skipToIdpId`, password, pincode, email whitelist entries, per-resource roles, and per-resource users from a single Ingress object.
- Credentials (password, pincode) are never embedded in annotations — they come from Kubernetes Secrets.
- Backward compatibility: existing Ingresses without the new annotations behave identically (no API calls made for unset auth methods).
- The reconcile is convergent: setting an annotation creates/updates the auth method; removing it clears the auth method.

**Non-Goals:**
- Shareable links / access tokens. These are dynamic and time-bound; they don't fit a declarative reconcile model and are typically created out-of-band.
- Org/identity-provider CRUD. IdPs are org-scoped, not resource-scoped, and managing them via Ingress is the wrong abstraction.
- Global MFA toggles. These are user-scoped, not resource-scoped.
- Reading credentials back from Pangolin for comparison. We trust last-write-wins from the Ingress side.

## Decisions

### D1: Secret reference syntax — `<name>` or `<namespace>/<name>`

For `password-secret-ref` and `pincode-secret-ref`, accept either a bare Secret name (resolved in the Ingress's own namespace) or a `namespace/name` form. The expected key inside the Secret is fixed: `password` and `pincode` respectively. Rationale: matches the convention used by `cert-manager`'s `secretName` and `external-dns`, doesn't require a new CRD, and avoids forcing users to colocate every Secret with the Ingress.

**Alternative considered**: a structured annotation (`'{"name":"x","namespace":"y","key":"z"}'`). Rejected — too verbose for the 99% case, and the key name is conventionally fixed per credential type.

### D2: Whitelist / role-ids / user-ids as JSON arrays

`email-whitelist`, `role-ids`, and `user-ids` annotations carry a JSON array (same approach already used by the existing `headers` and `healthcheck-headers` annotations). Empty array = "explicitly empty"; annotation absent = "do not manage". Rationale: consistency with existing annotation parsing helpers (`parseHeadersAnnotation`) and unambiguous semantics for the "I want to clear this" case.

The role/user annotations carry **Pangolin IDs** (positive integers for roles, opaque strings for users) — matching what the `setResourceRoles` and `setResourceUsers` endpoints actually accept. Resolving from friendlier identifiers (role name, user email) would require extra org-wide lookups and add a second class of "referenced thing not found" failure modes; that is left as a future enhancement.

### D3: Reconcile step ordering and convergence

A new method `reconcileResourceAuth(ctx, ingress, resourceID)` runs after `createOrUpdatePangolinResource` returns the resource ID, inside `processIngressRules`. It is **idempotent and convergent**:

- For password/pincode: hash the desired secret value (sha256 hex of `secretKey || ":" || secretValue`) into a controller-managed annotation `pangolin.ingress.k8s.io/password-hash` / `pincode-hash`. Only call the Pangolin set endpoint if the hash differs from the stored one (avoids leaking the secret on every reconcile and avoids unnecessary API calls). When the annotation is removed, the controller calls the clear endpoint and removes the hash annotation.
- For whitelist/roles/users: `GET` the current list, diff against the desired list, `POST` only on change.
- For `skipToIdpId`: just another field in `UpdateResourceRequest`, no new step needed.

**Alternative considered**: always POST every reconcile. Rejected — wasteful API churn and noisy audit logs in Pangolin, and password POSTs would re-hash on every reconcile.

### D4: Tracking annotations are excluded from the reconcile predicate

The new `password-hash` and `pincode-hash` annotations are controller-managed, just like `resource-id`. They must be added to `pangolinAnnotationChangedPredicate`'s skip list so that the controller's own writes don't re-trigger reconciliation. The cleanest implementation is to introduce a `controllerManagedAnnotations` set rather than hardcoding `annotationResourceID` inside the predicate.

### D5: RBAC widening for cross-namespace Secret reads

The controller currently has `secrets get/list/watch` cluster-wide (see RBAC in `deploy/clusterrole.yaml` and the kubebuilder marker on `IngressReconciler`). This is already sufficient for cross-namespace Secret reads, so **no RBAC change is required**. The proposal's "Impact" section will be corrected during task creation.

### D6: Error model — partial-success is acceptable but logged

If e.g. setting the password succeeds but assigning roles fails, the reconcile returns an error to trigger requeue. The successful sub-step is not rolled back (Pangolin has no transaction across these endpoints). Each sub-step logs its own success/failure. Rationale: matches how the existing target creation + stale-target deletion already works.

### D7: Pangolin client API stability

The Pangolin auth sub-endpoints are documented but not on the public Integration API docs page. We treat them as a contract that can change. The pangolin package wraps each in a single method so a future API rename only affects one file. If an endpoint returns 404 (Pangolin instance too old), the controller logs and continues without failing the whole reconcile — these are progressive features, not core to routing.

## Risks / Trade-offs

- **[Risk] Secret value drift undetected**: If a user edits the password directly in Pangolin UI, the controller won't notice (we only diff against our stored hash, not against Pangolin's current value). → **Mitigation**: document that the Ingress is the source of truth; UI edits will be reverted on next reconcile if the Secret changes.
- **[Risk] Password hash annotation leaks information**: The hash includes the key name as salt but is still a deterministic hash of a low-entropy PIN. → **Mitigation**: use sha256 with a per-resource salt (the Pangolin resource ID, which is already in another annotation). Document that the hash is intended for change-detection, not security.
- **[Risk] Order of operations during resource creation**: The password/pincode/whitelist endpoints require the resource to exist. → **Mitigation**: `reconcileResourceAuth` runs after `createOrUpdatePangolinResource` returns a non-empty ID. On the create-and-adopt path, the auth step still runs against the adopted resource.
- **[Risk] Pangolin API instability**: Sub-endpoints could change between Pangolin versions. → **Mitigation**: tolerate 404/405 with a warn log; integration tests against a real Pangolin instance gated by a build tag.
- **[Trade-off] No reconciliation of role/user *existence***: If an annotation references a role or user that doesn't exist in Pangolin, the assignment POST will fail and the reconcile will requeue forever. We accept this; the user should fix the annotation. Adding pre-validation (`GET /v1/org/{org}/roles`) would double the API calls per reconcile.

## Migration Plan

No migration needed — purely additive. Existing Ingresses are unaffected. Rollback = remove the new annotations (the controller will clear corresponding Pangolin state on next reconcile if it manages the field, or do nothing if the annotation was never set).
