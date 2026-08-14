## Why

Pangolin's authentication model for a private resource is three grant types —
roles, users and **machines** — and the docs are explicit that the third is not
reducible to the other two:

> Access can be assigned to resources for specific machines. Only those machine
> clients will gain access to the resource when they connect. Note that machines
> can not be put into roles.

`PangolinEndpoint` already models all three (`spec.private.access.roles` /
`.users` / `.clients`, where `clients` is Pangolin's *Machines*). Two defects
stop the machine grant from actually working end to end.

**1. A machine cannot be named by the identifier operators actually hold.**
`resolveClients` matches a client on its `Name`, and falls back to `NiceID`
*only when the name is empty*. Its own doc comment claims a client "is matched
on its name or its nice ID" — the code does not do that. A machine client is
provisioned with an ID and secret, and the nice ID (`40hf1wm4whxgx4n`) is what
Pangolin shows and what the CLI is configured with. Today naming that value in
`access.clients` reports `PrincipalNotFound` for any client that has a name,
which is every client created through the UI.

**2. A grant that create does not apply is never repaired.** `reconcileEndpoint`
returns immediately after `createSiteResource`; `reconcilePrincipals` runs only
on the update branch. The reconciler registers `GenerationChangedPredicate` and
does not requeue on success, so for an endpoint whose spec never changes, the
create pass is the *only* pass. If Pangolin's create ignores or partially
applies `clientIds`, the machine grant silently never lands and nothing ever
notices — the resource exists, the alias is right, and the client still gets
NXDOMAIN, because the alias only resolves through the tunnel's DNS proxy for a
client that has been granted the resource.

The create-time `userIds`/`roleIds`/`clientIds` contract has never been verified
against a live instance holding clients. The spike behind
`add-private-endpoint-crd` recorded the gap and asked for it to be closed:

> **The organization contains no clients** (`total: 0`) even though a client is
> connected to the mesh […] Re-run this part against an org that has clients
> before relying on the client field names.

So the controller is trusting an unverified write on the one code path that
never gets a second chance.

## What Changes

- Resolve a machine client by **either** its name or its nice ID, always, rather
  than by nice ID only as a fallback for an unnamed client. A value matching two
  different clients across the two identifier spaces is refused as ambiguous,
  which is how every other name collision is already handled.
- Run the principal reconcile **after create as well as after update**, so the
  grants are read back and repaired on the one pass a stable endpoint gets. In
  the expected case this is three reads and no writes.
- Document that `access.clients` is Pangolin's *Machines* grant and accepts a
  nice ID, in the CRD field documentation and the README.

Non-goals: changing the shape of `access`, granting machines through roles
(Pangolin forbids it), and the public raw TCP/UDP branch.

## Capabilities

### New Capabilities

None.

### Modified Capabilities

- `pangolin-name-resolution`: client resolution matches a name **or** a nice ID
  rather than treating the nice ID as a fallback.
- `private-endpoint-crd`: the access-principal requirement gains the guarantee
  that grants are verified on the create path, not only on update.

## Impact

- `internal/controller/name_resolution.go` — `lookupByName` takes a match
  predicate instead of a single name accessor; `resolveClients` matches both
  identifiers.
- `internal/controller/pangolinendpoint_controller.go` — the create branch of
  `reconcileEndpoint` calls `reconcilePrincipals`.
- `api/v1alpha1/pangolinendpoint_types.go`, `README.md` — documentation only;
  the CRD manifests are regenerated because a field description changes.
- Tests: the fake's clients carry only a name, so neither defect is currently
  observable. It gains a client with both identifiers and a mode in which create
  drops the grant arrays.
- No schema change, no migration. An endpoint already reconciled repairs itself
  the next time it is reconciled; one whose create dropped a grant is repaired on
  controller restart or on the next spec change.
