## MODIFIED Requirements

### Requirement: Access principals

The controller SHALL send the resolved client, role, and user identifiers with the private resource, and SHALL surface an endpoint that grants access to no named principal as a non-fatal, visible condition.

A role that Pangolin grants on its own SHALL be treated as server-owned: the controller SHALL NOT send it, SHALL NOT count its presence as a difference, and SHALL NOT attempt to remove it. The controller SHALL write the principal set only when the principals it manages actually differ from those in place, so that a steady state produces no writes.

The controller SHALL verify the granted principals against Pangolin on every path that creates or updates a private resource, including creation, and SHALL repair a difference it finds there. Sending the principals with the create request SHALL NOT be treated as evidence that they were applied.

#### Scenario: Named principals are sent as resolved identifiers

- **WHEN** an endpoint names clients, roles, and users
- **THEN** the private resource is created with the corresponding Pangolin identifiers

#### Scenario: A grant dropped at creation is repaired in the same reconcile

- **GIVEN** a Pangolin instance that accepts the principal lists on create and does not apply them
- **WHEN** an endpoint naming a client, a role and a user is reconciled for the first time
- **THEN** the controller reads back the granted principals
- **AND** writes the named client, role and user
- **AND** the endpoint does not depend on a later reconcile to become reachable

#### Scenario: A creation that applies the grant issues no principal writes

- **GIVEN** a Pangolin instance that applies the principal lists sent with create
- **WHEN** an endpoint naming a client, a role and a user is reconciled for the first time
- **THEN** the controller reads back the granted principals
- **AND** issues no role, user or client write

#### Scenario: Endpoint with no principals is created but flagged

- **WHEN** an endpoint declares no clients, roles, or users
- **THEN** the private resource is created with empty principal lists
- **AND** the controller sets `Ready=False` with reason `NoPrincipalsGranted`
- **AND** the reported message states that the endpoint is still reachable through the role Pangolin grants implicitly

#### Scenario: Removing a principal converges

- **GIVEN** a reconciled endpoint granting access to two roles
- **WHEN** one role is removed from the spec
- **THEN** the private resource is updated to grant only the remaining role

#### Scenario: A server-granted role does not cause a write

- **GIVEN** Pangolin has granted a role of its own to the private resource
- **AND** the endpoint names no roles
- **WHEN** the endpoint is reconciled repeatedly
- **THEN** no principal write is issued
- **AND** the server-granted role remains in place

#### Scenario: A server-granted role is not removed while managing others

- **GIVEN** Pangolin has granted a role of its own to the private resource
- **WHEN** an endpoint naming one role is reconciled
- **THEN** the principal write carries the named role
- **AND** does not attempt to withdraw the server-granted role

#### Scenario: Managed roles still converge alongside a server-granted role

- **GIVEN** a reconciled endpoint granting one named role, alongside a server-granted role
- **WHEN** the named role is removed from the spec
- **THEN** exactly one principal write is issued
- **AND** a subsequent reconcile issues none
