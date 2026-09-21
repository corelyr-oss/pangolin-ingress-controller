## MODIFIED Requirements

### Requirement: PangolinEndpoint custom resource

The controller SHALL serve a namespaced custom resource `PangolinEndpoint` in group `pangolin.corelyr.com`, version `v1alpha1`, with a status subresource. The resource SHALL require exactly one of a `spec.private` block or a `spec.public` block, and SHALL NOT allow an existing object to switch between them.

#### Scenario: Private endpoint is accepted

- **WHEN** a `PangolinEndpoint` with `spec.backendRef` and `spec.private` is created
- **THEN** the API server accepts it
- **AND** the controller reconciles it into a Pangolin private resource

#### Scenario: Public endpoint is accepted

- **WHEN** a `PangolinEndpoint` with `spec.backendRef` and `spec.public` is created
- **THEN** the API server accepts it
- **AND** the controller reconciles it into a Pangolin raw public resource

#### Scenario: Both branches are rejected

- **WHEN** a `PangolinEndpoint` is created with both `spec.private` and `spec.public`
- **THEN** the API server rejects it at admission

#### Scenario: Neither branch is rejected

- **WHEN** a `PangolinEndpoint` is created with neither `spec.private` nor `spec.public`
- **THEN** the API server rejects it at admission

#### Scenario: Switching branch is rejected

- **GIVEN** an existing `PangolinEndpoint` with `spec.private`
- **WHEN** it is updated to carry `spec.public` instead
- **THEN** the API server rejects the update

#### Scenario: Status writes do not retrigger reconciliation

- **WHEN** the controller writes `.status` after a successful reconcile
- **THEN** `metadata.generation` is unchanged
- **AND** no further reconcile is enqueued as a result of that write

## ADDED Requirements

### Requirement: Public raw endpoint

For a `PangolinEndpoint` with `spec.public`, the controller SHALL maintain one Pangolin raw public resource with the declared protocol and proxy port, with one target per resolved site pointing at the backing Service's cluster DNS name and service port. `servicePort` SHALL default to `proxyPort`. The controller SHALL NOT set a domain, subdomain, or any HTTP or authentication setting on the resource.

#### Scenario: Public TCP endpoint is created

- **WHEN** an endpoint declares `public: {proxyPort: 7443}` and its Service exposes TCP port 7443
- **THEN** a raw resource with mode `tcp` and proxy port 7443 is created
- **AND** a target for `<service>.<namespace>.svc.cluster.local:7443` is created on the configured site
- **AND** `.status.resourceId` and `.status.resolvedPorts.tcp` are populated
- **AND** `Ready` is `True`

#### Scenario: Service port that the Service does not expose is refused

- **WHEN** `servicePort` (or its default) is not a port of the backing Service with the declared protocol
- **THEN** the controller sets `ResolvedRefs=False` with reason `BackendUnsupported`
- **AND** creates nothing in Pangolin

#### Scenario: Changing the proxy port updates in place

- **GIVEN** a reconciled public endpoint
- **WHEN** `proxyPort` is changed
- **THEN** the existing resource is updated with the new proxy port
- **AND** no second resource is created

#### Scenario: Stale targets are removed

- **GIVEN** a public resource carrying a target that does not match the desired `(site, host, port)`
- **WHEN** the endpoint is reconciled
- **THEN** the desired target is created and the stale one deleted

#### Scenario: Unchanged endpoint issues no writes

- **GIVEN** a public endpoint whose resource and targets already match
- **WHEN** it is reconciled again
- **THEN** no create, update or delete is sent to Pangolin

#### Scenario: Deletion removes the public resource

- **WHEN** a reconciled public endpoint is deleted
- **THEN** the controller deletes the Pangolin resource recorded in `.status.resourceId`
- **AND** removes the finalizer

### Requirement: Public endpoint identity

The controller SHALL give each public resource the same deterministic identity derivation as the private branch. Because Pangolin's raw-resource create accepts no `niceId`, the controller SHALL create the resource with its `name` set to the identity, SHALL record the created identifier in status before setting the `niceId` by update, and SHALL re-find the resource by recorded identifier, then by `niceId`, then by `name` in a complete listing of the organisation's resources. It SHALL NOT adopt a resource by matching its protocol or proxy port, and SHALL NOT treat a failed listing as absence.

#### Scenario: Created resource is named deterministically

- **WHEN** a public endpoint is created
- **THEN** the resource is created with `name` set to the derived identity
- **AND** is then updated to carry the same value as its `niceId`

#### Scenario: Lost status is recovered by nice ID

- **GIVEN** a public resource carrying the derived `niceId` exists
- **AND** `.status.resourceId` has been lost
- **WHEN** the endpoint is reconciled
- **THEN** the controller finds the resource in the listing by `niceId`
- **AND** does not create a second resource

#### Scenario: Interrupted naming is recovered by name

- **GIVEN** a raw resource of the declared protocol whose `name` is the derived identity and whose `niceId` is not
- **AND** `.status.resourceId` is empty
- **WHEN** the endpoint is reconciled
- **THEN** the controller adopts that resource and sets its `niceId`
- **AND** does not create a second resource

#### Scenario: Failed naming does not lose the resource

- **GIVEN** the create succeeds and the naming update fails
- **WHEN** the reconcile ends
- **THEN** `.status.resourceId` records the created resource
- **AND** the next reconcile names that resource instead of creating another

#### Scenario: A failed listing does not cause a create

- **GIVEN** `.status.resourceId` is empty
- **WHEN** the resource listing fails
- **THEN** the reconcile returns an error
- **AND** no resource is created

#### Scenario: A nice-ID match of the wrong kind is refused

- **WHEN** the resource found by `niceId` is not a raw resource of the declared protocol
- **THEN** the controller sets `Programmed=False` with reason `IdentityAmbiguous`
- **AND** does not modify that resource

### Requirement: Public proxy port exclusivity

Pangolin does not prevent two raw resources from sharing a protocol and proxy port. Before creating a public resource, and before changing an existing one's proxy port, the controller SHALL check the organisation's resources for another raw resource with the same protocol and proxy port, and SHALL refuse the write if one exists.

#### Scenario: A taken proxy port is reported, not adopted

- **GIVEN** another raw resource holds TCP port 7443
- **WHEN** an endpoint declaring TCP proxy port 7443 is reconciled with no resource of its own
- **THEN** the controller sets `Programmed=False` with reason `ProxyPortInUse` naming the holder
- **AND** emits a Warning event and requeues without a reconcile error
- **AND** creates nothing and modifies nothing

#### Scenario: Moving to a taken port is refused

- **GIVEN** a reconciled public endpoint on port 7443
- **WHEN** `proxyPort` is changed to a port another raw resource holds
- **THEN** the controller sets `Programmed=False` with reason `ProxyPortInUse`
- **AND** the existing resource keeps its port

#### Scenario: The same port on the other protocol is allowed

- **GIVEN** a UDP raw resource holds port 7443
- **WHEN** an endpoint declaring TCP proxy port 7443 is reconciled
- **THEN** the resource is created
