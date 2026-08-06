## MODIFIED Requirements

### Requirement: Access principals

The controller SHALL send the resolved client, role, and user identifiers with the private resource, and SHALL surface an endpoint that grants access to no named principal as a non-fatal, visible condition.

A role that Pangolin grants on its own SHALL be treated as server-owned: the controller SHALL NOT send it, SHALL NOT count its presence as a difference, and SHALL NOT attempt to remove it. The controller SHALL write the principal set only when the principals it manages actually differ from those in place, so that a steady state produces no writes.

#### Scenario: Named principals are sent as resolved identifiers

- **WHEN** an endpoint names clients, roles, and users
- **THEN** the private resource is created with the corresponding Pangolin identifiers

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
