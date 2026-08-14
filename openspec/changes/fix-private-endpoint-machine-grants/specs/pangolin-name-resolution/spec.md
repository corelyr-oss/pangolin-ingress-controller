## MODIFIED Requirements

### Requirement: Name to identifier resolution

The controller SHALL resolve Pangolin role names, usernames, and client names to their Pangolin identifiers before sending them to the API. Resolutions that hit the cache SHALL NOT cost an API call.

A machine client SHALL be resolvable by either its name or its nice ID, whichever the reference carries. Neither identifier SHALL take precedence over the other: a reference matching two distinct clients across the two identifier spaces is ambiguous and SHALL be refused.

#### Scenario: Role name resolves to its identifier

- **WHEN** an object references a role by name that exists in the organization
- **THEN** the controller sends that role's Pangolin identifier

#### Scenario: Client name resolves to its identifier

- **WHEN** an object references a client by name that exists in the organization
- **THEN** the controller sends that client's Pangolin identifier

#### Scenario: Client nice ID resolves to its identifier

- **GIVEN** a client that has both a name and a nice ID
- **WHEN** an object references that client by its nice ID
- **THEN** the controller sends that client's Pangolin identifier

#### Scenario: A client matching on both of its own identifiers is not ambiguous

- **GIVEN** a client whose name and nice ID are the same string
- **WHEN** an object references that string
- **THEN** the controller sends that client's Pangolin identifier

#### Scenario: A reference matching two clients across identifier spaces is refused

- **GIVEN** one client named `web` and a different client whose nice ID is `web`
- **WHEN** an object references `web` as a client
- **THEN** the controller reports a resolution failure identifying the ambiguity
- **AND** sends no client identifier for that reference
- **AND** does not create or update the Pangolin object

#### Scenario: Cached names cost no API calls

- **GIVEN** every referenced name is already cached
- **WHEN** an object is reconciled
- **THEN** no name-listing or lookup call is made
