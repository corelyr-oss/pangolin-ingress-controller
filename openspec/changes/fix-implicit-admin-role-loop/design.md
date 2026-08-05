## Context

See `proposal.md` — Why. The captured exchange, repeated once per reconcile and
never converging:

```
GET  /v1/site-resource/6/roles
  -> {"roles":[{"roleId":1,"name":"Admin",
                "description":"Admin role with the most permissions","isAdmin":true}]}
POST /v1/site-resource/6/roles   {"roleIds": []}
  -> {}                          # accepted, and Admin is still attached
```

`ListSiteResourceRoles` returns `[1]`, the desired set is `[]`,
`intSetsEqual` reports a difference, and `SetSiteResourceRoles` writes. The
write succeeds and changes nothing, because Pangolin re-attaches (or never
detaches) the admin role. Six reconciles, six writes, no progress.

The same code path handles users and clients and is fine there: both read back
empty against an empty desired set. Only the role list has an entry the server
puts in by itself.

`Role` already decodes `roleId` and `name`; the response carries an `isAdmin`
flag that the struct drops, which is the signal needed to tell a server-owned
role from one an operator asked for.

## Goals / Non-Goals

**Goals:**

- A steady state issues no principal writes.
- Roles an operator names still converge, including removals.
- Status stops claiming nobody has access when Pangolin has granted admin.

**Non-Goals:**

- Managing or suppressing Pangolin's implicit grant. It is not the controller's
  to remove, and trying was the bug.
- Reworking name resolution, or the access model in the CRD. `spec.private.access`
  keeps its current shape and meaning.
- Detecting server-owned *users* or *clients*. Neither shows this behaviour;
  adding speculative handling would be guessing at an API this project has
  already been burned by guessing at.

## Decisions

### D1: Identify the server-owned role by `isAdmin`, not by name or id

The controller filters the observed role list to those with `isAdmin` false
before comparing, and never includes an admin role in what it sends.

*Why `isAdmin`:* it is the server's own statement about the role's nature.
*Alternatives rejected:* matching the name `"Admin"` breaks on a renamed or
localized role and would silently resume looping; hardcoding `roleId 1` assumes
an ordering the API never promises and would differ per organisation.

This means `ListSiteResourceRoles` must stop discarding the flag — it currently
returns bare IDs, so the caller has nothing to filter on. It returns roles.

### D2: Compare only what the controller manages

The comparison becomes: *observed roles that are not server-owned* against
*desired roles*. A difference in the server-owned part is invisible by
construction, so it cannot drive a write.

An operator who explicitly names the admin role in `spec.private.access.roles`
gets it resolved and sent like any other name. Pangolin already has it attached,
so the effect is nil — but the request is honoured rather than silently
dropped, and the comparison stays symmetric because the desired set is filtered
the same way the observed set is.

### D3: `NoPrincipalsGranted` keeps its name, and gains an honest message

The condition still fires when no named principal has access, because that is
the thing an operator needs to notice. The message states that org
administrators reach the endpoint through Pangolin's implicit role.

**Confirmed on the data path 2026-08-06.** An endpoint with no named principal
was reached from a mesh client over the mesh (`HTTP 200`), so the implicit grant
is real access and not a bookkeeping artefact. The reworded message is accurate:
such an endpoint is reachable, just not by anyone the spec names.

*Alternative rejected:* treating the implicit admin grant as "principals
granted" and reporting `Ready=True`. Every endpoint would then be ready on
creation, and the condition would stop carrying the one piece of information it
exists to carry.

### D4: The fake must grant the implicit role

The current fake attaches nothing, which is why a suite of twenty-odd endpoint
tests never saw a loop that a live instance produces on every reconcile. It is
changed to attach an admin role at create, exactly as the server does.

This is the same lesson as the listing envelope in
`fix-private-endpoint-live-defects`: a fake that is more convenient than the
server hides the bugs the server will produce.

## Risks / Trade-offs

- **A future Pangolin grants some other role implicitly, without `isAdmin`** →
  The loop returns, in the same shape. The regression test asserts "repeated
  reconciles issue no writes" rather than "admin is filtered", so it catches a
  recurrence even if the mechanism differs, though only against a fake that
  models the new grant.
- **An operator wants the admin grant gone** → Not possible through this
  controller, and it was not possible before either; the difference is that the
  controller now stops pretending it can. Worth stating in the CRD field
  documentation for `access.roles`.
- **Filtering hides a genuine drift in the admin role** → Accepted: the
  controller does not manage that role, so there is no drift it could act on.

## Open Questions

- Whether Pangolin's implicit grant is per-organisation policy or fixed
  behaviour. It does not change the fix — the controller filters on the flag
  either way — but it decides whether the CRD documentation should describe the
  grant as always present or as instance-dependent.
