## ADDED Requirements

### Requirement: PangolinEndpoint custom resource

The controller SHALL serve a namespaced custom resource `PangolinEndpoint` in group `pangolin.ingress.k8s.io`, version `v1alpha1`, with a status subresource. In `v1alpha1` the resource SHALL require a `spec.private` block and SHALL reject a `spec.public` block, which is reserved for a future public raw TCP/UDP branch.

#### Scenario: Private endpoint is accepted

- **WHEN** a `PangolinEndpoint` with `spec.backendRef` and `spec.private` is created
- **THEN** the API server accepts it
- **AND** the controller reconciles it into a Pangolin private resource

#### Scenario: Reserved public block is rejected

- **WHEN** a `PangolinEndpoint` is created with a `spec.public` block
- **THEN** the API server rejects it at admission
- **AND** the rejection message states that the public branch is not implemented in `v1alpha1`

#### Scenario: Neither branch is rejected

- **WHEN** a `PangolinEndpoint` is created with no `spec.private` block
- **THEN** the API server rejects it at admission

#### Scenario: Status writes do not retrigger reconciliation

- **WHEN** the controller writes `.status` after a successful reconcile
- **THEN** `metadata.generation` is unchanged
- **AND** no further reconcile is enqueued as a result of that write

### Requirement: Service-backed destination

The controller SHALL resolve `spec.backendRef` to a Kubernetes `Service` in the `PangolinEndpoint`'s own namespace and SHALL use that Service's cluster DNS name as the Pangolin destination. Services that have no stable cluster-local address SHALL be rejected.

#### Scenario: Destination is the Service cluster DNS name

- **GIVEN** a `PangolinEndpoint` named `postgres` in namespace `data` with `backendRef.name: postgres`
- **WHEN** the endpoint is reconciled
- **THEN** the private resource is created with destination `postgres.data.svc.cluster.local`

#### Scenario: Referenced Service does not exist

- **WHEN** `backendRef` names a Service that is absent
- **THEN** the controller sets `ResolvedRefs=False` with a reason identifying the missing Service
- **AND** emits a Warning event on the `PangolinEndpoint`
- **AND** requeues without returning a reconcile error

#### Scenario: Headless Service is rejected

- **WHEN** `backendRef` names a Service with `clusterIP: None`
- **THEN** the controller sets `ResolvedRefs=False`
- **AND** no private resource is created or updated

#### Scenario: ExternalName Service is rejected

- **WHEN** `backendRef` names a Service of `type: ExternalName`
- **THEN** the controller sets `ResolvedRefs=False`
- **AND** no private resource is created or updated

### Requirement: Alias derivation

The controller SHALL address each private endpoint by an internal FQDN. When `spec.private.alias` is set it SHALL be used verbatim. When it is unset the controller SHALL derive `<name>.<namespace>.<suffix>` from the configured alias suffix, and SHALL refuse to derive an alias when no suffix is configured. The effective alias SHALL be reported in status.

#### Scenario: Alias is derived from name, namespace and suffix

- **GIVEN** the alias suffix is configured as `corp.internal`
- **WHEN** a `PangolinEndpoint` named `postgres` in namespace `data` is reconciled without an explicit alias
- **THEN** the private resource alias is `postgres.data.corp.internal`
- **AND** `.status.address` reports `postgres.data.corp.internal`

#### Scenario: Explicit alias overrides derivation

- **GIVEN** the alias suffix is configured
- **WHEN** a `PangolinEndpoint` sets `spec.private.alias` explicitly
- **THEN** that value is sent to Pangolin unchanged
- **AND** the configured suffix is not consulted

#### Scenario: Derivation without a configured suffix is refused

- **GIVEN** no alias suffix is configured
- **WHEN** a `PangolinEndpoint` without an explicit alias is reconciled
- **THEN** the controller sets `Accepted=False` with reason `AliasSuffixNotConfigured`
- **AND** emits a Warning event
- **AND** requeues without returning a reconcile error
- **AND** no private resource is created

### Requirement: Structured port declaration

The controller SHALL accept ports as structured entries — a single port, an inclusive range, or all ports — and SHALL serialize them into Pangolin's per-protocol port range strings. When no ports are declared the controller SHALL derive them from the backing Service's ports on every reconcile.

#### Scenario: Structured ports are serialized per protocol

- **WHEN** an endpoint declares TCP port `5432`, TCP range `8000`–`9000`, and all UDP ports
- **THEN** the TCP range string sent to Pangolin covers `5432` and `8000-9000`
- **AND** the UDP range string sent to Pangolin is `*`

#### Scenario: Exactly one port form per entry

- **WHEN** a port entry sets both a single port and a range
- **THEN** the API server rejects the resource at admission

#### Scenario: Inverted range is rejected

- **WHEN** a port entry declares a range whose start exceeds its end
- **THEN** the API server rejects the resource at admission

#### Scenario: Ports default to the backing Service

- **GIVEN** a Service exposing ports `5432` and `9187`
- **WHEN** a `PangolinEndpoint` referencing it declares no ports
- **THEN** both `5432` and `9187` are exposed on the private resource
- **AND** `.status.resolvedPorts` reports the port set that was sent

#### Scenario: Service port change is tracked when ports are unset

- **GIVEN** a reconciled `PangolinEndpoint` with no declared ports
- **WHEN** a port is added to the backing Service
- **AND** the endpoint is reconciled again
- **THEN** the added port is included in the port set sent to Pangolin

### Requirement: Port set comparison is semantic

The controller SHALL compare the desired and current port sets by their meaning rather than by their serialized text, so that server-side normalization does not produce updates on every reconcile.

#### Scenario: Reordered port string is not a change

- **GIVEN** the desired TCP ports are `5432` and `8080`
- **AND** Pangolin reports them in a different order
- **WHEN** the endpoint is reconciled
- **THEN** no update call is made

#### Scenario: Merged adjacent ports are not a change

- **GIVEN** the desired TCP ports are `5432` and `5433`
- **AND** Pangolin reports the equivalent range `5432-5433`
- **WHEN** the endpoint is reconciled
- **THEN** no update call is made

#### Scenario: A genuine port change is applied

- **GIVEN** a reconciled endpoint exposing TCP `5432`
- **WHEN** TCP `9187` is added to the spec
- **THEN** the controller issues exactly one update call carrying both ports

### Requirement: Deterministic identity and recovery

The controller SHALL derive a deterministic Pangolin `niceId` from the resource's namespace and name and SHALL use it to re-find its private resource when the recorded identifier is unavailable. The controller SHALL NOT adopt a Pangolin object by matching any other attribute.

#### Scenario: niceId is derived from the resource coordinates

- **WHEN** a `PangolinEndpoint` named `postgres` in namespace `data` is created
- **THEN** the private resource is created with a `niceId` derived from the configured prefix, `data`, and `postgres`
- **AND** the same `niceId` is reported in status

#### Scenario: Lost status is recovered without creating a duplicate

- **GIVEN** a private resource exists in Pangolin for a `PangolinEndpoint`
- **AND** `.status.siteResourceId` has been lost
- **WHEN** the endpoint is reconciled
- **THEN** the controller looks the resource up by its derived `niceId`
- **AND** re-records the identifier in status
- **AND** does not create a second private resource

#### Scenario: Over-long identity is refused rather than truncated

- **WHEN** the derived `niceId` would exceed the length Pangolin accepts
- **THEN** the controller sets `Accepted=False` with a reason naming the limit
- **AND** no private resource is created

### Requirement: Access principals

The controller SHALL send the resolved client, role, and user identifiers with the private resource, and SHALL surface an endpoint that grants access to no principal as a non-fatal, visible condition.

#### Scenario: Named principals are sent as resolved identifiers

- **WHEN** an endpoint names clients, roles, and users
- **THEN** the private resource is created with the corresponding Pangolin identifiers

#### Scenario: Endpoint with no principals is created but flagged

- **WHEN** an endpoint declares no clients, roles, or users
- **THEN** the private resource is created with empty principal lists
- **AND** the controller sets `Ready=False` with reason `NoPrincipalsGranted`

#### Scenario: Removing a principal converges

- **GIVEN** a reconciled endpoint granting access to two roles
- **WHEN** one role is removed from the spec
- **THEN** the private resource is updated to grant only the remaining role

### Requirement: Lifecycle and deletion

The controller SHALL add a finalizer to each managed `PangolinEndpoint`, delete the corresponding Pangolin private resource on deletion, and remove the finalizer only after the deletion has been confirmed.

#### Scenario: Deletion removes the Pangolin resource

- **WHEN** a reconciled `PangolinEndpoint` is deleted
- **THEN** the controller deletes the corresponding private resource in Pangolin
- **AND** removes the finalizer so the object is released

#### Scenario: Deletion of an already-absent resource succeeds

- **GIVEN** the private resource has already been deleted in Pangolin
- **WHEN** the `PangolinEndpoint` is deleted
- **THEN** the controller removes the finalizer without reporting an error

#### Scenario: Deletion is not finalized while Pangolin is unreachable

- **GIVEN** the Pangolin API is failing
- **WHEN** a `PangolinEndpoint` is deleted
- **THEN** the finalizer remains in place
- **AND** the controller retries

### Requirement: Status reporting

The controller SHALL report the observed state of each `PangolinEndpoint` using `Accepted`, `ResolvedRefs`, and `Programmed` conditions, together with the Pangolin identifier, the effective address, and the port set that was sent.

#### Scenario: Successful reconcile reports all conditions true

- **WHEN** an endpoint is reconciled successfully
- **THEN** `Accepted`, `ResolvedRefs`, and `Programmed` are all `True`
- **AND** `.status.siteResourceId`, `.status.address`, and `.status.resolvedPorts` are populated

#### Scenario: Pangolin rejects the write

- **WHEN** Pangolin returns an error for a create or update
- **THEN** `Programmed` is set to `False` with the reported reason
- **AND** the previously recorded identifier in status is retained

### Requirement: Server capability tolerance

The controller SHALL treat a Pangolin instance that does not implement private resources as an operator-visible configuration condition rather than a controller fault.

#### Scenario: Instance does not support private resources

- **WHEN** the private-resource endpoints return not-implemented
- **THEN** the controller sets `Accepted=False` with reason `UnsupportedByServer`
- **AND** emits a Warning event
- **AND** requeues without returning a reconcile error
- **AND** does not increment the controller's reconcile error metric
