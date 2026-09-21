## ADDED Requirements

### Requirement: Adoption after a create conflict

When creating the Pangolin resource for an Ingress host is refused as a conflict, the controller SHALL look for the existing resource in a complete listing of the organisation's resources, matching HTTP resources whose full domain equals the host (case-insensitively) and whose domain ID equals the host's resolved domain. It SHALL adopt the resource only if exactly one matches. A failed listing SHALL be reported as an error and SHALL NOT be treated as "no match".

#### Scenario: Subdomain host is adopted

- **GIVEN** Pangolin holds a resource for `app.example.com`
- **AND** the Ingress has no recorded resource ID for that host
- **WHEN** the create is refused as a conflict
- **THEN** the controller records that resource's ID for the host
- **AND** does not create another resource

#### Scenario: Apex host does not adopt a sibling

- **GIVEN** Pangolin holds resources for `nav.example.com` and `example.com` on the same domain
- **WHEN** the create for host `example.com` is refused as a conflict
- **THEN** the controller adopts the `example.com` resource
- **AND** does not modify `nav.example.com`

#### Scenario: Resource beyond the first page is adopted

- **GIVEN** the matching resource is not on the first page of the listing
- **WHEN** the create is refused as a conflict
- **THEN** the controller follows the pagination and adopts it

#### Scenario: Ambiguous match is refused

- **GIVEN** two HTTP resources carry the host's full domain and domain ID
- **WHEN** the create is refused as a conflict
- **THEN** the reconcile fails with an error naming both candidates
- **AND** neither resource is modified
