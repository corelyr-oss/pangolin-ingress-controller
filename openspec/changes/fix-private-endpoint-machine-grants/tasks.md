## 1. Name resolution: match a machine on either identifier

- [x] 1.1 Change `lookupByName` in `internal/controller/name_resolution.go` to take `matches func(T, string) bool` in place of `nameOf func(T) string`; thread it through `matchByName` (D1).
- [x] 1.2 Point `resolveRoles` at name equality — behaviour unchanged.
- [x] 1.3 Point `resolveClients` at `c.Name == name || c.NiceID == name`, and correct the doc comment that already claimed this.
- [x] 1.4 Confirm the dedupe-by-identifier guard in `matchByName` still holds: one client matching on both its name and its nice ID is one match, not an ambiguity. Covered by `TestEndpoint_MachineMatchingItselfTwiceIsNotAmbiguous`.

## 2. Verify grants on the create path

- [x] 2.1 In `reconcileEndpoint`, replace the create branch's bare `return nil` with a call to `reconcilePrincipals` against the created resource's ID (D2).
- [x] 2.2 Keep sending `userIds`/`roleIds`/`clientIds` on the create request — they are required, and the follow-up is a verification, not a replacement.

## 3. Test fake: make both defects reachable

- [x] 3.1 Give the fake's client a nice ID alongside its name (D3) — `{ID: 12, NiceID: "40hf1wm4whxgx4n", Name: "vinzenz-laptop"}`.
- [x] 3.2 Add a `createDropsGrants` flag: create stores empty principal lists and still returns a valid resource, standing in for a server that accepts the fields and ignores them.
- [x] 3.3 Count client and user writes as well as role writes, so a steady-state assertion covers all three grant types.

## 4. Tests

- [x] 4.1 `TestEndpoint_MachineResolvesByNiceID` — an endpoint naming the client's nice ID is granted that client's identifier.
- [x] 4.2 `TestEndpoint_MachineResolvesByName` — the existing behaviour still holds (guard against fixing one identifier by breaking the other).
- [x] 4.3 `TestEndpoint_AmbiguousMachineIdentifierIsRefused` — one client named `web`, another with nice ID `web`; the reference reports `ResolvedRefs=False`/`PrincipalAmbiguous`, and no resource is created.
- [x] 4.4 `TestEndpoint_GrantDroppedAtCreateIsRepaired` — with `createDropsGrants`, one reconcile leaves the resource holding the named client, role and user.
- [x] 4.5 `TestEndpoint_CreateThatHonoursGrantsIssuesNoWrites` — without the flag, create is followed by reads only; zero role, user and client writes.
- [x] 4.6 Re-run the existing principal tests unchanged — `ServerGrantedRoleCausesNoWrite`, `ServerGrantedRoleIsNotWithdrawn`, `ManagedRolesStillConverge`, `UnknownPrincipalRequeuesWithoutError`, `NoPrincipalsIsCreatedButNotReady` — none of their assertions needed editing.
- [x] 4.7 Verify each new guard fails with its fix reverted, not merely that it passes with it. `MachineResolvesByNiceID`, `AmbiguousMachineIdentifierIsRefused` and `GrantDroppedAtCreateIsRepaired` fail against `git stash`ed sources; the three describing unchanged behaviour pass. (The first draft of 4.1 dereferenced a nil resource on failure, which panicked the test binary and stopped the other guards from running at all — it now fails with the unmet condition instead.)

## 5. Documentation

- [x] 5.1 Document in `api/v1alpha1/pangolinendpoint_types.go` that `access.clients` is Pangolin's *Machines* grant, accepts a name or a nice ID, and that machines cannot be granted through roles.
- [x] 5.2 `make manifests` to regenerate `deploy/crds/` and the chart CRD template with the new description. The diff is confined to that description.
- [x] 5.3 README: note the Machines wording and the nice ID in the `access` example and the spec-field table, with a link to Pangolin's private-resource authentication docs.

## 6. Validation

- [x] 6.1 `make fmt vet test` clean — `internal/controller` 73.6% coverage, `internal/pangolin` 34.1%.
- [x] 6.2 `openspec validate fix-private-endpoint-machine-grants --strict`.
- [x] 6.3 Live: grant a machine client by nice ID against the real instance, confirm the resource shows it under Machines, and confirm the alias resolves from that client. **Performed by the maintainer on 2026-08-14.** The observations were not captured here, so unlike the live checks recorded in `fix-implicit-admin-role-loop` and `fix-private-endpoint-live-defects` this entry attests that the check was run, not what it returned. In particular it does not document the client listing's field names (`clientId`/`niceId`/`name`) — spike question 1.9 in `add-private-endpoint-crd/tasks.md` stays open on that point, and a wrong field name would surface as `PrincipalNotFound` for every client reference.
