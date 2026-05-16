## ADDED Requirements

### Requirement: Skip-to-IdP for SSO

The controller SHALL set the Pangolin resource's `skipToIdpId` field from the Ingress annotation `pangolin.ingress.k8s.io/skip-to-idp-id` so users are redirected directly to a configured identity provider instead of the Pangolin login page.

#### Scenario: Skip-to-IdP annotation is set
- **WHEN** an Ingress with `pangolin.ingress.k8s.io/skip-to-idp-id: "3"` is reconciled
- **THEN** the controller sends `skipToIdpId: 3` in the resource update request
- **AND** the Pangolin resource has `skipToIdpId = 3` after reconcile

#### Scenario: Skip-to-IdP annotation is removed
- **WHEN** an Ingress that previously had `skip-to-idp-id` is updated to remove the annotation
- **THEN** the controller sends a resource update that clears `skipToIdpId` (zero value, omitted/null per API contract)
- **AND** subsequent SSO logins no longer skip the Pangolin login page

#### Scenario: Skip-to-IdP annotation has a non-integer value
- **WHEN** the annotation value cannot be parsed as a positive integer
- **THEN** the controller logs an error referencing the annotation key
- **AND** does not send `skipToIdpId` in the resource update (treats it as unset)

### Requirement: Email whitelist entries

The controller SHALL reconcile the Pangolin resource's email whitelist from the Ingress annotation `pangolin.ingress.k8s.io/email-whitelist`, a JSON array of email addresses or wildcards (e.g. `*@example.com`).

#### Scenario: Whitelist annotation is set
- **WHEN** an Ingress is reconciled with `email-whitelist: '["alice@example.com","*@example.com"]'` and `email-whitelist-enabled: "true"`
- **THEN** the controller posts the two entries to `POST /v1/resource/{id}/whitelist`
- **AND** the Pangolin resource's whitelist contains exactly those entries

#### Scenario: Whitelist annotation is updated
- **WHEN** the annotation changes from `["a@example.com"]` to `["b@example.com"]`
- **THEN** the controller GETs the current whitelist, computes a diff, and POSTs the new list
- **AND** the previous entry is no longer present in Pangolin

#### Scenario: Whitelist annotation is removed
- **WHEN** the annotation is deleted from the Ingress
- **THEN** the controller does not modify the whitelist (annotation absence means "unmanaged")

#### Scenario: Whitelist annotation is an empty array
- **WHEN** the annotation is set to `[]`
- **THEN** the controller posts an empty list, clearing all entries

#### Scenario: Whitelist annotation contains invalid JSON
- **WHEN** the annotation value cannot be parsed as a JSON array of strings
- **THEN** the controller logs an error referencing the annotation key
- **AND** does not call the whitelist endpoint

### Requirement: Resource password via Secret reference

The controller SHALL reconcile the Pangolin resource's shared password from a referenced Kubernetes Secret named by `pangolin.ingress.k8s.io/password-secret-ref`. The Secret value at key `password` is the desired plaintext password.

#### Scenario: Password secret reference is set in the same namespace
- **WHEN** an Ingress in namespace `ns-a` has annotation `password-secret-ref: "my-secret"` and Secret `ns-a/my-secret` has data key `password = "hunter2"`
- **THEN** the controller calls `POST /v1/resource/{id}/password` with body containing the password "hunter2"
- **AND** records a hash of the password in annotation `pangolin.ingress.k8s.io/password-hash`

#### Scenario: Password secret reference is cross-namespace
- **WHEN** the annotation value is `kube-system/shared-creds`
- **THEN** the controller reads Secret `kube-system/shared-creds` for the `password` key

#### Scenario: Password value changes
- **WHEN** the referenced Secret's `password` data changes from `"old"` to `"new"`
- **THEN** the next reconcile detects a hash mismatch and POSTs the new password
- **AND** updates the `password-hash` annotation

#### Scenario: Password reference is removed
- **WHEN** the `password-secret-ref` annotation is removed from a previously-managed Ingress
- **THEN** the controller calls the Pangolin endpoint that clears the password
- **AND** removes the `password-hash` annotation

#### Scenario: Referenced Secret does not exist
- **WHEN** the annotation references a Secret that cannot be fetched
- **THEN** the reconcile returns an error and is requeued
- **AND** the existing Pangolin password is not modified

#### Scenario: Referenced Secret is missing the `password` key
- **WHEN** the Secret exists but has no `password` data key
- **THEN** the controller logs an error and the reconcile is requeued
- **AND** the existing Pangolin password is not modified

### Requirement: Resource pincode via Secret reference

The controller SHALL reconcile the Pangolin resource's pincode from a referenced Kubernetes Secret named by `pangolin.ingress.k8s.io/pincode-secret-ref`. The Secret value at key `pincode` is the desired numeric pincode.

#### Scenario: Pincode secret reference is set
- **WHEN** an Ingress has `pincode-secret-ref: "pin-secret"` and the Secret has `pincode = "1234"`
- **THEN** the controller calls `POST /v1/resource/{id}/pincode` with the pincode value
- **AND** records a hash in annotation `pangolin.ingress.k8s.io/pincode-hash`

#### Scenario: Pincode reference is removed
- **WHEN** the `pincode-secret-ref` annotation is removed from a previously-managed Ingress
- **THEN** the controller clears the pincode in Pangolin and removes the `pincode-hash` annotation

#### Scenario: Pincode value is not numeric
- **WHEN** the Secret's `pincode` key contains non-numeric characters
- **THEN** the controller still passes the value to Pangolin (validation is Pangolin's responsibility)
- **AND** if Pangolin returns an error, the reconcile is requeued and the error logged

### Requirement: Per-resource role assignment

The controller SHALL reconcile the set of roles assigned to a Pangolin resource from the Ingress annotation `pangolin.ingress.k8s.io/role-ids`, a JSON array of positive integer role IDs (matching Pangolin's `roleIds` body field on `POST /v1/resource/{id}/roles`).

#### Scenario: Role-IDs annotation is set
- **WHEN** an Ingress has `role-ids: '[1,4]'`
- **THEN** the controller fetches the current role assignments, computes the diff against `{1,4}`, and posts `{"roleIds":[1,4]}` only if the set differs
- **AND** the Pangolin resource has exactly those role IDs assigned

#### Scenario: Role-IDs annotation is removed
- **WHEN** the annotation is deleted
- **THEN** the controller does not modify role assignments (unmanaged)

#### Scenario: Role-IDs annotation is an empty array
- **WHEN** the annotation is set to `[]`
- **THEN** the controller posts `{"roleIds":[]}`, removing all role assignments

#### Scenario: A referenced role ID does not exist in Pangolin
- **WHEN** the assignment POST returns an error indicating the role is unknown
- **THEN** the reconcile returns an error, is requeued, and the error is logged with the offending role ID

### Requirement: Per-resource user assignment

The controller SHALL reconcile the set of users assigned to a Pangolin resource from the Ingress annotation `pangolin.ingress.k8s.io/user-ids`, a JSON array of Pangolin user ID strings (matching Pangolin's `userIds` body field on `POST /v1/resource/{id}/users`).

#### Scenario: User-IDs annotation is set
- **WHEN** an Ingress has `user-ids: '["abc123","def456"]'`
- **THEN** the controller diffs against the current assignments and posts `{"userIds":["abc123","def456"]}` only if the set differs
- **AND** the Pangolin resource has exactly those user IDs assigned

#### Scenario: User-IDs annotation is removed
- **WHEN** the annotation is deleted
- **THEN** the controller does not modify user assignments (unmanaged)

### Requirement: Auth reconciliation runs after resource creation

The controller SHALL invoke the auth-method reconciliation only after the Pangolin resource exists and a resource ID has been recorded on the Ingress.

#### Scenario: Resource is being created for the first time
- **WHEN** an Ingress is reconciled with no `resource-id` annotation
- **THEN** the controller first creates (or adopts) the Pangolin resource
- **AND** then invokes auth reconciliation against the new resource ID

#### Scenario: Resource creation fails
- **WHEN** resource creation returns an error before an ID is recorded
- **THEN** auth reconciliation is skipped for this reconcile attempt
- **AND** no password/pincode/whitelist/role/user API calls are made

### Requirement: Controller-managed annotations do not retrigger reconciliation

The annotation predicate filter SHALL treat `pangolin.ingress.k8s.io/password-hash` and `pangolin.ingress.k8s.io/pincode-hash` as controller-managed (alongside the existing `resource-id`), so that the controller's own writes do not cause reconciliation loops.

#### Scenario: Controller writes a password hash
- **WHEN** the controller updates the `password-hash` annotation after a successful password POST
- **THEN** the resulting watch event does not pass the annotation-changed predicate
- **AND** does not trigger another reconciliation

### Requirement: Backward compatibility

Existing Ingresses without any of the new annotations SHALL behave identically to the pre-change controller — no additional Pangolin API calls are made for unset auth methods.

#### Scenario: Existing Ingress without new annotations
- **WHEN** an Ingress that uses only the pre-existing annotations is reconciled
- **THEN** the controller does not call `/password`, `/pincode`, `/whitelist`, `/roles`, or `/users` endpoints
- **AND** `skipToIdpId` is not included in the resource update body

### Requirement: Pangolin endpoint availability is tolerated

If a Pangolin instance responds with 404 or 405 to one of the auth sub-endpoints (indicating an older Pangolin version that lacks the feature), the controller SHALL log a warning, skip that specific auth method, and continue reconciling the rest of the Ingress.

#### Scenario: Pangolin returns 404 for /pincode
- **WHEN** an Ingress with `pincode-secret-ref` is reconciled against a Pangolin instance that returns 404 on `/v1/resource/{id}/pincode`
- **THEN** the controller logs a warning naming the endpoint and the resource
- **AND** does not return an error from reconcile (the Ingress is otherwise healthy)
- **AND** does not record a `pincode-hash` annotation
