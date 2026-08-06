# pangolin-name-resolution Specification

## Purpose
TBD - created by archiving change add-private-endpoint-crd. Update Purpose after archive.
## Requirements
### Requirement: Name to identifier resolution

The controller SHALL resolve Pangolin role names, usernames, and client names to their Pangolin identifiers before sending them to the API. Resolutions that hit the cache SHALL NOT cost an API call.

#### Scenario: Role name resolves to its identifier

- **WHEN** an object references a role by name that exists in the organization
- **THEN** the controller sends that role's Pangolin identifier

#### Scenario: Client name resolves to its identifier

- **WHEN** an object references a client by name that exists in the organization
- **THEN** the controller sends that client's Pangolin identifier

#### Scenario: Cached names cost no API calls

- **GIVEN** every referenced name is already cached
- **WHEN** an object is reconciled
- **THEN** no name-listing or lookup call is made

### Requirement: Refresh on miss

The controller SHALL refetch the relevant name mapping when a referenced name is not found in the cache, and SHALL retry the lookup against the refreshed mapping before reporting a resolution failure.

#### Scenario: Principal created after controller startup resolves without restart

- **GIVEN** a role was created in Pangolin after the cache was populated
- **AND** the refresh interval has elapsed since the last refetch attempt
- **WHEN** an object referencing that role is reconciled
- **THEN** the controller refetches the role mapping
- **AND** the name resolves against the refreshed mapping
- **AND** no controller restart is required

#### Scenario: Name is genuinely absent

- **GIVEN** the refresh interval has elapsed since the last refetch attempt
- **WHEN** a referenced name matches nothing before or after the refetch
- **THEN** the controller reports a resolution failure for that name

### Requirement: Refresh rate limiting

The controller SHALL refetch each name mapping at most once per configured refresh interval, counted from the last refetch **attempt** regardless of whether it succeeded, and SHALL retain the existing mapping when a refetch fails.

#### Scenario: Second miss within the interval does not refetch

- **GIVEN** a refetch attempt occurred less than one refresh interval ago
- **WHEN** another name fails to resolve
- **THEN** no refetch call is made
- **AND** the resolution fails from the existing cache

#### Scenario: Many unresolvable references share one refetch

- **GIVEN** the refresh interval has elapsed
- **WHEN** several objects referencing distinct unknown names are reconciled in sequence
- **THEN** at most one refetch call is made per mapping

#### Scenario: Failed refetch preserves the existing mapping

- **GIVEN** a populated cache
- **WHEN** a refetch fails because the Pangolin API is unavailable
- **THEN** the previously cached mapping is retained
- **AND** the failed attempt still consumes the rate limit

### Requirement: Ambiguous names are refused

The controller SHALL refuse to resolve a name that matches more than one Pangolin object rather than selecting one of them.

#### Scenario: Duplicate role names are refused

- **GIVEN** two roles in the organization share a name
- **WHEN** an object references that name
- **THEN** the controller reports a resolution failure identifying the ambiguity
- **AND** sends no identifier for that reference
- **AND** does not create or update the Pangolin object

### Requirement: Unresolvable names are operator-fixable

The controller SHALL report a name that cannot be resolved as an expected, operator-fixable condition rather than a controller fault.

#### Scenario: Unknown name is reported and requeued

- **WHEN** a referenced name cannot be resolved after a refresh
- **THEN** the controller sets `ResolvedRefs=False` on the referencing object with a reason naming the unresolved reference
- **AND** emits a Warning event on that object
- **AND** requeues at approximately the refresh interval
- **AND** does not return a reconcile error
- **AND** does not increment the controller's reconcile error metric

#### Scenario: Resolution recovers once the principal exists

- **GIVEN** an object requeuing because a referenced role does not exist
- **WHEN** the role is created in Pangolin
- **AND** the next requeue occurs after the refresh interval has elapsed
- **THEN** the name resolves
- **AND** `ResolvedRefs` becomes `True`

