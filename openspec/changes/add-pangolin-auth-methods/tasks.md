## 1. Pangolin client: new endpoints and types

- [x] 1.1 Add `SkipToIdpID *int` field to `UpdateResourceRequest` in `internal/pangolin/resources.go` (JSON tag `skipToIdpId,omitempty`).
- [x] 1.2 Add `SetResourcePassword(ctx, resourceID, password *string) error` (single method, nil clears, matches Pangolin's `{password: string|null}` schema).
- [x] 1.3 Add `SetResourcePincode(ctx, resourceID, pincode *string) error` (single method, nil clears, matches Pangolin's `{pincode: string|null}` schema requiring exactly 6 digits).
- [x] 1.4 Add `GetResourceWhitelist(ctx, resourceID string) ([]string, error)` and `SetResourceWhitelist(ctx, resourceID string, emails []string) error`.
- [x] 1.5 Add `ListResourceRoles(ctx, resourceID string) ([]int, error)` and `SetResourceRoles(ctx, resourceID string, roleIDs []int) error`.
- [x] 1.6 Add `ListResourceUsers(ctx, resourceID string) ([]string, error)` and `SetResourceUsers(ctx, resourceID string, userIDs []string) error`.
- [x] 1.7 Added `NotImplementedError` + `IsNotImplemented` and a `checkResponseWithNotImplemented` helper scoped to the new auth sub-endpoints (existing GetResource etc. unchanged).

## 2. Annotation constants and parsers

- [x] 2.1 Added annotation constants `annotationSkipToIdpID`, `annotationEmailWhitelist`, `annotationPasswordSecretRef`, `annotationPincodeSecretRef`, `annotationRoleIDs`, `annotationUserIDs`, `annotationPasswordHash`, `annotationPincodeHash` plus secret-key constants.
- [x] 2.2 Added `parseStringSliceAnnotation` (and `parseIntSliceAnnotation` for role IDs) — returns nil when absent, empty slice for `"[]"`, error on malformed JSON.
- [x] 2.3 Added `parseSecretRef(value, defaultNamespace)` supporting `name` and `namespace/name` forms; rejects empty, double-slash, or trailing-slash values.
- [x] 2.4 Introduced `controllerManagedAnnotations` map; refactored `pangolinAnnotationChangedPredicate.Update` to consult it (covers `resource-id`, `password-hash`, `pincode-hash`).

## 3. Resource update wiring

- [x] 3.1 `createOrUpdatePangolinResource` now passes `SkipToIdpID: parseIntAnnotation(annotations, annotationSkipToIdpID)` into `UpdateResourceRequest`.

## 4. Auth reconciliation step

- [x] 4.1 Added `reconcileResourceAuth(ctx, ingress, resourceID)` called from the end of `createOrUpdatePangolinResource` (after target reconciliation, before return).
- [x] 4.2 Password sub-reconcile via `reconcileSecretBackedAuth` helper — implements the full (no annot, no hash) / (annot, no hash) / (annot, matching hash) / (annot, stale hash) / (no annot, hash present) state machine using `hashSecretValue(resourceID, value)`.
- [x] 4.3 Pincode sub-reconcile uses the same shared helper with the `pincode` secret key and `pincode-hash` annotation.
- [x] 4.4 Whitelist sub-reconcile: GET current, diff via `stringSetsEqual`, POST only on change. nil (annotation absent) skips.
- [x] 4.5 Roles sub-reconcile: same pattern using `ListResourceRoles` / `SetResourceRoles` with `intSetsEqual`.
- [x] 4.6 Users sub-reconcile: same pattern using `ListResourceUsers` / `SetResourceUsers`.
- [x] 4.7 Every sub-step checks `pangolin.IsNotImplemented(err)` after both list and set calls and logs+returns nil; other errors propagate.

## 5. Secret reading helper

- [x] 5.1 Added `getSecretValue(ctx, ns, name, key)` returning sentinel `errSecretNotFound`/`errSecretKeyMissing` errors via `errors.Is`-friendly wrapping.
- [x] 5.2 Verified `deploy/clusterrole.yaml:11` and `chart/templates/clusterrole.yaml` both already grant cluster-wide `secrets get;list;watch`. No RBAC change required (matches design D5).

## 6. Tests

- [x] 6.1 Added `TestParseStringSliceAnnotation`, `TestParseIntSliceAnnotation`, `TestParseSecretRef` covering absent / empty / malformed / single-name / ns/name / multiple-slash / whitespace cases.
- [x] 6.2 Added `TestPangolinAnnotationChangedPredicate_IgnoresManaged` (iterates the whole managed set) and `TestPangolinAnnotationChangedPredicate_DetectsUserChanges` (regression guard).
- [x] 6.3 Added six tests covering the full `reconcileSecretBackedAuth` state machine: NoAnnotNoHash_Noop, AnnotPresent_NoHash_SetsAndWritesHash, MatchingHash_Noop, StaleHash_Resets, AnnotRemoved_HashPresent_Clears, plus SecretMissing/SecretKeyMissing error paths.
- [x] 6.4 Added `TestReconcileWhitelist_NoChangeWhenEqual`, `TestReconcileWhitelist_PostsOnChange`, `TestReconcileRoles_PostsOnChange`, `TestReconcileUsers_NoChangeWhenEqual` via real `*pangolin.Client` against an `httptest` server.
- [x] 6.5 Added `TestReconcileSecretBackedAuth_NotImplemented_Tolerated`, `TestReconcileWhitelist_404Tolerated`, and `TestPangolinClient_404MapsToNotImplemented` (verifies the client surface maps 404 → `*NotImplementedError`).

## 7. Documentation

- [x] 7.1 README "Resource Auth Methods" + "Controller-managed" tables added with all six user annotations and two controller-written hash annotations.
- [x] 7.2 IMPLEMENTATION.md API Endpoints list extended with the six new sub-resource routes and a note on `skipToIdpId` on the resource update payload.
- [x] 7.3 README "Example: Resource Password + Whitelist + Roles" added with a complete Secret + Ingress YAML.

## 8. Validation

- [~] 8.1 `make fmt vet` clean. `go test ./...` — all 27 new tests pass plus all previously-passing tests. **Pre-existing failure**: `TestIngressReconciler_Reconcile` was already broken on `main` (it omits both `APIKeySecret`/`APIKeyNamespace` and any backing API-key Secret, so `initPangolinClient` always returns "secrets '' not found"). Verified by stashing this change and running the test on a clean tree. Out of scope for this change — flag for a follow-up "fix or delete this test" PR.
- [ ] 8.2 Manual smoke test against a real Pangolin instance: create an Ingress with each new annotation, verify in Pangolin UI, mutate the Secret, verify re-reconcile, delete the annotation, verify cleanup. **Cannot be performed in this environment** — requires a live Pangolin instance.
- [x] 8.3 `openspec validate add-pangolin-auth-methods` → "Change 'add-pangolin-auth-methods' is valid".
