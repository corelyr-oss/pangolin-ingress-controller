## Context

A private resource is reachable only by a principal that has been granted it.
Pangolin exposes the grant as three independent lists on the resource — roles,
users, machines — and the controller writes them from
`spec.private.access.{roles,users,clients}`.

Both defects sit on the machine (`clients`) side, and both are invisible to the
current test suite for the same reason: the fake's only client is
`{ID: 12, Name: "vinzenz-laptop"}`. It has no nice ID, so the fallback branch is
the one that gets exercised; and its create honours the grant arrays faithfully,
so nothing ever asks what happens when a server does not.

## Goals / Non-Goals

**Goals:**
- A machine client can be named by the identifier an operator has in hand.
- A grant is verified to be in place on every path that can create one.
- The steady state still issues no principal writes.

**Non-Goals:**
- Reshaping `access`. Three lists matching three grant types is the right model.
- Suppressing or managing Pangolin's implicit admin role — settled in
  `fix-implicit-admin-role-loop` and unchanged here.
- Reading a machine's secret, or provisioning clients. The controller grants
  access to clients that already exist.

## Decisions

### D1: Match a machine on its name **or** its nice ID, unconditionally

`resolveClients` currently supplies one identifier per client — the name, or the
nice ID when the name is empty — and `lookupByName` compares against it. That
makes the two identifiers mutually exclusive: a named client is reachable only by
name.

The nice ID is the better of the two to support, not the worse. It is what
Pangolin displays, what a machine client's credentials are issued against, and
it is stable — a name is free-text and can be edited in the UI without anything
noticing. Making it a fallback for the unnamed case means it works precisely for
the clients an operator is least likely to be referencing.

`lookupByName` therefore takes a `matches func(T, string) bool` predicate rather
than a `nameOf func(T) string` accessor. Roles pass name equality and are
unchanged; clients pass `c.Name == name || c.NiceID == name`.

**Ambiguity is preserved, and now spans both identifier spaces.** If one client
is *named* `web` and another has *nice ID* `web`, the reference matches two
distinct clients and is refused with `PrincipalAmbiguous` rather than resolved to
either. This is the existing D8 rule — refuse rather than guess — applied to a
larger match set, and it is why the predicate returns a bool per entry instead of
the resolver checking two maps in priority order: a priority order would silently
pick one of two real clients, which is exactly the wrong-grant outcome D8 exists
to prevent.

*Alternative rejected:* a separate `access.machineIds` field for nice IDs.
Rejected — it doubles a field to express one relationship, and forces an operator
to know which of two identifiers they are holding before they can write the spec.

### D2: Reconcile principals after create, not only after update

`reconcileEndpoint`'s create branch returns as soon as the resource is created,
trusting that Pangolin applied the `userIds`/`roleIds`/`clientIds` sent with it.
That trust is unusually load-bearing here because of how the controller is
triggered: `GenerationChangedPredicate`, no requeue on a successful reconcile.
For an endpoint that is created once and never edited, **the create pass is the
only pass there will ever be**. A grant dropped there is dropped permanently,
and the symptom — resource present, alias correct, client still refused — points
at everything except the grant.

The fix is to fall through to `reconcilePrincipals` after create, exactly as the
update branch does. `reconcilePrincipals` reads each list and writes only on a
difference, so when create behaved the follow-up costs three reads and no
writes, and when it did not, the grant is repaired inside the same reconcile.

*Alternative rejected:* stop sending the arrays at create and always grant in the
follow-up step. Rejected on two counts. The fields are required by the API — a
create that omits them fails — so they would have to be sent as empty arrays
anyway; and it would open a window where the resource exists with no grants, so a
failure between the two calls leaves an endpoint nobody can reach. Sending the
grant with the create keeps the good path a single round trip and makes the
verification a read.

*Alternative rejected:* verify by comparing the create **response**. Rejected —
the create response is a `SiteResource`, which carries no principal lists at all,
so there is nothing in it to compare.

### D3: The fake has to be able to misbehave

Neither defect is reachable through the current fake, which is the reason both
survived a suite that already covers principal convergence in five tests. Two
changes make them reachable:

- its client gains both identifiers (`{ID: 12, NiceID: "40hf1wm4whxgx4n", Name: "vinzenz-laptop"}`),
  so name-only matching becomes observable as a failure;
- a `createDropsGrants` flag makes create store empty principal lists while still
  returning a valid resource, standing in for a server that accepts the fields
  and ignores them.

The second is deliberately modelled as *silent* success rather than an error.
A create that rejected the arrays outright would already surface as a failed
reconcile; the case worth a regression test is the one that looks like it worked.

## Risks / Trade-offs

- **[Trade-off] Three extra reads on every create.** Accepted: it is once per
  endpoint per creation, against a failure mode that is permanent and whose
  symptom does not point at its cause.
- **[Risk] A client renamed to another client's nice ID turns a working
  reference ambiguous**, and the endpoint stops reconciling with
  `PrincipalAmbiguous`. Accepted, and preferable to the alternative — under a
  priority order the same rename would silently move the grant to a different
  machine. The condition names both.
- **[Risk] Pangolin could grant a machine implicitly**, the way it grants the
  admin role, which would produce the write-per-reconcile loop
  `fix-implicit-admin-role-loop` fixed for roles. Not observed on the live
  instance, and the client listing carries no `isAdmin`-equivalent flag to key
  off. Left unhandled rather than guessed at; the symptom would be a client write
  on every reconcile, which the steady-state test would catch.

## Migration Plan

None. No schema change and no stored state changes meaning. An endpoint whose
machine grant was dropped at create is repaired on its next reconcile — that is,
on controller restart or on the next edit to its spec. An endpoint referencing a
client by nice ID that currently reports `PrincipalNotFound` resolves on the next
reconcile once the cache refresh interval has elapsed.
