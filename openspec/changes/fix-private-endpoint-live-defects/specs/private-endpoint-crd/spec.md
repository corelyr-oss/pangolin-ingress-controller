## ADDED Requirements

### Requirement: Both port range strings are always explicit

The controller SHALL send an explicit port range string for **both** TCP and UDP on every create and update, including for a protocol the user did not declare. A protocol with no declared ports SHALL be sent as an empty range rather than omitted, because Pangolin substitutes a wildcard for an absent range and would otherwise expose every port of that protocol to every principal granted access.

#### Scenario: Undeclared protocol is not left to the server default

- **WHEN** an endpoint declares TCP port `8080` and no UDP ports
- **THEN** the request carries a UDP range that exposes no UDP port
- **AND** the resource read back from Pangolin does not report the UDP range as `*`

#### Scenario: Wildcard drift on the undeclared protocol is corrected

- **GIVEN** a private resource whose UDP range is `*` from an earlier release
- **AND** the endpoint declares no UDP ports
- **WHEN** the endpoint is reconciled
- **THEN** the controller issues an update that narrows the UDP range
- **AND** a subsequent reconcile issues no further update

#### Scenario: An explicitly declared wildcard is preserved

- **WHEN** an endpoint declares all UDP ports
- **THEN** the UDP range sent to Pangolin is `*`

### Requirement: Alias is a fully qualified domain name

The API server SHALL reject a `PangolinEndpoint` whose explicit alias is not a fully qualified domain name, so that an unusable alias fails at admission rather than in the reconcile loop. A derived alias SHALL be well-formed by construction.

#### Scenario: Single-label alias is rejected at admission

- **WHEN** a `PangolinEndpoint` sets `spec.private.alias` to a single label with no dot
- **THEN** the API server rejects the resource
- **AND** no private resource is created

#### Scenario: Qualified alias is accepted

- **WHEN** a `PangolinEndpoint` sets `spec.private.alias` to a multi-label domain name
- **THEN** the API server accepts the resource

## MODIFIED Requirements

### Requirement: Deterministic identity and recovery

The controller SHALL derive a deterministic Pangolin `niceId` from the resource's namespace and name and SHALL use it to re-find its private resource when the recorded identifier is unavailable. Recovery SHALL match the `niceId` against a listing of the site's private resources, and SHALL NOT depend on a point-lookup route being present. The controller SHALL NOT adopt a Pangolin object by matching any other attribute, and SHALL NOT treat an absent route as evidence that no resource exists.

Pangolin does not enforce `niceId` uniqueness. When more than one private resource carries the derived `niceId`, the controller SHALL refuse to select one: it SHALL report the ambiguity as an operator-fixable condition, SHALL NOT create a further resource, and SHALL NOT modify any of the candidates.

#### Scenario: niceId is derived from the resource coordinates

- **WHEN** a `PangolinEndpoint` named `postgres` in namespace `data` is created
- **THEN** the private resource is created with a `niceId` derived from the configured prefix, `data`, and `postgres`
- **AND** the same `niceId` is reported in status

#### Scenario: Lost status is recovered without creating a duplicate

- **GIVEN** a private resource exists in Pangolin for a `PangolinEndpoint`
- **AND** `.status.siteResourceId` has been lost
- **WHEN** the endpoint is reconciled
- **THEN** the controller finds the resource by matching its derived `niceId` against the site's private resources
- **AND** re-records the identifier in status
- **AND** does not create a second private resource

#### Scenario: An unroutable lookup does not cause a duplicate create

- **GIVEN** `.status.siteResourceId` has been lost
- **WHEN** the lookup used for recovery is unavailable on the server
- **THEN** the controller sets `Programmed=False` with a reason naming the failure
- **AND** requeues
- **AND** does not create a second private resource

#### Scenario: An ambiguous identity is refused, not guessed

- **GIVEN** two private resources on the site carry the endpoint's derived `niceId`
- **AND** `.status.siteResourceId` is empty
- **WHEN** the endpoint is reconciled
- **THEN** the controller reports a condition naming the ambiguity
- **AND** emits a Warning event
- **AND** requeues without returning a reconcile error
- **AND** neither candidate is modified
- **AND** no further private resource is created

#### Scenario: A recorded identifier is used even when the nice ID is ambiguous

- **GIVEN** `.status.siteResourceId` records a resource that still exists
- **AND** another resource on the site shares the endpoint's derived `niceId`
- **WHEN** the endpoint is reconciled
- **THEN** the recorded resource is used
- **AND** no ambiguity is reported

#### Scenario: Over-long identity is refused rather than truncated

- **WHEN** the derived `niceId` would exceed the length Pangolin accepts
- **THEN** the controller sets `Accepted=False` with a reason naming the limit
- **AND** no private resource is created

### Requirement: Port set comparison is semantic

The controller SHALL compare the desired and current port sets by their meaning rather than by their serialized text, so that server-side normalization does not produce updates on every reconcile. The comparison SHALL cover both protocols, including a protocol for which no ports are declared, so that a server-side wildcard on that protocol is detected as a difference.

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

#### Scenario: Wildcard on an undeclared protocol is a difference

- **GIVEN** an endpoint declaring only TCP ports
- **AND** Pangolin reports the UDP range as `*`
- **WHEN** the endpoint is reconciled
- **THEN** the port sets compare unequal
- **AND** the controller issues an update

### Requirement: Status reporting

The controller SHALL report the observed state of each `PangolinEndpoint` using `Accepted`, `ResolvedRefs`, and `Programmed` conditions, together with the Pangolin identifier, the effective address, the address assigned by Pangolin, and the port set that was sent.

#### Scenario: Successful reconcile reports all conditions true

- **WHEN** an endpoint is reconciled successfully
- **THEN** `Accepted`, `ResolvedRefs`, and `Programmed` are all `True`
- **AND** `.status.siteResourceId`, `.status.address`, and `.status.resolvedPorts` are populated

#### Scenario: Assigned mesh address is reported

- **GIVEN** Pangolin has assigned an address to the private resource
- **WHEN** the endpoint is reconciled successfully
- **THEN** status reports that assigned address alongside the alias
- **AND** the assigned address is visible as a printer column

#### Scenario: Pangolin rejects the write

- **WHEN** Pangolin returns an error for a create or update
- **THEN** `Programmed` is set to `False` with the reported reason
- **AND** the previously recorded identifier in status is retained

#### Scenario: A programmed endpoint continues to converge

- **GIVEN** an endpoint that has been created in Pangolin
- **WHEN** it is reconciled again with no spec change
- **THEN** the controller reads its current state successfully
- **AND** `Programmed` remains `True`
- **AND** no reconcile error is returned
