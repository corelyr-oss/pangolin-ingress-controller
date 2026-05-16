## Why

The ingress controller currently exposes only the toggle-style auth fields on the Pangolin resource update payload (`sso`, `ssl`, `blockAccess`, `emailWhitelistEnabled`, `applyRules`, `enabled`). Pangolin supports several additional resource-scoped auth methods — resource password, pincode, email whitelist *entries*, role/user assignments, and skip-to-IdP for SSO — that today cannot be configured from a Kubernetes Ingress and must be set manually in the Pangolin UI. Users running everything as IaC need parity so the Ingress is the single source of truth for resource auth.

## What Changes

- Add Ingress annotation `pangolin.ingress.k8s.io/skip-to-idp-id` mapping to the resource update field `skipToIdpId` (skip Pangolin login and jump straight to a specific IdP).
- Add annotation `pangolin.ingress.k8s.io/email-whitelist` (JSON array of email addresses / wildcards) that reconciles the resource's whitelist via `POST /resource/{id}/whitelist`. Pairs with the existing `email-whitelist-enabled` toggle.
- Add annotation `pangolin.ingress.k8s.io/password-secret-ref` (`<secret-name>` or `<namespace>/<secret-name>`, key `password`) that reconciles a resource shared password via `POST /resource/{id}/password`. Empty/missing secret clears the password.
- Add annotation `pangolin.ingress.k8s.io/pincode-secret-ref` (same Secret-ref shape, key `pincode`) that reconciles the resource pincode via `POST /resource/{id}/pincode`.
- Add annotations `pangolin.ingress.k8s.io/role-ids` (JSON array of integer role IDs) and `pangolin.ingress.k8s.io/user-ids` (JSON array of Pangolin user ID strings) reconciling per-resource role and user assignments via `POST /resource/{id}/roles` and `POST /resource/{id}/users`.
- Extend the pangolin client (`internal/pangolin/`) with typed methods + request structs for the new endpoints, and the controller's RBAC to read referenced Secrets in any namespace.
- Document all new annotations in `README.md`.

Non-goals: shareable links / access tokens (dynamic, time-bound, ill-suited to Ingress reconciliation), global MFA toggles (user-scoped, not resource-scoped), and any IdP CRUD (org-scoped, not per-resource).

## Capabilities

### New Capabilities
- `resource-auth-methods`: configuring Pangolin's resource-scoped auth methods (skip-to-IdP, email whitelist, password, pincode, roles, users) declaratively via Ingress annotations, including Secret-backed credential injection.

### Modified Capabilities
<!-- none — no pre-existing specs in openspec/specs/ -->

## Impact

- **Code**:
  - `internal/pangolin/resources.go`: new `SetPassword`, `ClearPassword`, `SetPincode`, `ClearPincode`, `SetWhitelist`, `ListRoles`, `AssignRoles`, `ListUsers`, `AssignUsers` methods + request structs; extend `UpdateResourceRequest` with `SkipToIdpID *int`.
  - `internal/controller/ingress_controller.go`: new annotation constants + parsing helpers; new reconcile step `reconcileResourceAuth` invoked after the resource exists. Secret-ref resolution helper.
  - RBAC (`deploy/clusterrole.yaml`, `chart/templates/clusterrole.yaml`, kubebuilder markers): widen `secrets` get/watch to all namespaces if the user references a Secret outside `--pangolin-api-key-namespace`.
- **API surface**: six new annotations under `pangolin.ingress.k8s.io/`. No breaking changes — existing Ingresses continue to work unchanged.
- **Dependencies**: none added.
- **Docs**: `README.md` annotation tables (Access Control + new Auth Methods sections), `IMPLEMENTATION.md` API endpoint list.
- **Security**: passwords/pincodes are sourced from Kubernetes Secrets, never from annotations directly, to keep them out of `kubectl describe` output and `etcd` plaintext exposure beyond what Secrets already provide. The controller never writes credentials back into annotations or status.
