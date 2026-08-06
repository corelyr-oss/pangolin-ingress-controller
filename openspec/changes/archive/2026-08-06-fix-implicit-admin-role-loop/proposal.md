## Why

Pangolin attaches its `Admin` role to every private resource by itself, and
refuses to let that role be removed. The controller does not know this: it
reads the resource's roles, sees a role the spec did not ask for, and writes
the role set back without it. Pangolin keeps `Admin` anyway, so the next
reconcile reads the same difference and writes again.

Observed live on 2026-08-06 against org `tunnel-tf`: **6 reconciles produced 6
role writes**, every one of them `{"roleIds": []}` against a resource that
reported `[{"roleId": 1, "name": "Admin", "isAdmin": true}]` both before and
after. There is no state in which the controller stops writing. Users and
clients do not have this problem — both read back empty and compare equal — so
the defect is specific to the implicitly granted role.

This is the same failure class as the port-range loop that
`fix-private-endpoint-live-defects` fixed, and it was found in that change's
live verification: a value the server maintains on its own, compared as though
the controller owned it. It was left out of that change because the fix is a
question about the access model rather than about how a field is serialized.

A second, quieter problem comes with it: an endpoint that grants no principals
reports `Ready=False` with reason `NoPrincipalsGranted`, which is not true.
Pangolin has granted `Admin`. The endpoint is reachable by org administrators;
what it lacks is access for the principals an operator would name.

## What Changes

- Treat a role Pangolin marks `isAdmin` as **server-owned**: never send it, and
  never count its presence as a difference. The controller reconciles only the
  roles it was asked to manage.
- Write the role set only when the managed roles actually differ, so a steady
  state produces no writes.
- Report the implicit grant honestly rather than claiming nobody has access.
  `NoPrincipalsGranted` keeps its meaning — no *named* principal has access —
  but the message says that org administrators still reach the endpoint through
  Pangolin's implicit role.
- Carry `isAdmin` on the role type, which the client currently drops.

## Capabilities

### New Capabilities

None.

### Modified Capabilities

- `private-endpoint-crd`: the access-principal requirement changes — a
  server-granted role is excluded from what the controller manages and from the
  comparison that decides whether to write, and the readiness message for an
  endpoint with no named principals is corrected. The capability is introduced
  by the pending `add-private-endpoint-crd` change and carries a delta from
  `fix-private-endpoint-live-defects`; this change stacks on both and must be
  archived after them.

## Impact

- `internal/controller/pangolinendpoint_controller.go` — `reconcilePrincipals`
  and the readiness condition.
- `internal/pangolin/site_resources.go` — `Role.IsAdmin`.
- Tests: the fake grants no implicit role today, which is why the suite never
  saw this. It has to model the implicit `Admin` grant, and the regression is
  "N reconciles produce zero role writes".
- No API or schema change; no migration. Existing resources converge on the
  next reconcile by stopping a write, not by starting one.
