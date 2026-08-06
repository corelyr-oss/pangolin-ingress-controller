## 1. Client: keep the signal that identifies a server-owned role

- [x] 1.1 Add `IsAdmin bool \`json:"isAdmin"\`` to `pangolin.Role`.
- [x] 1.2 Change `ListSiteResourceRoles` to return `[]Role` rather than bare IDs, so callers can tell a server-granted role from a named one. Update its decode to read `roleId`, `name` and `isAdmin` from the sub-resource response.
- [x] 1.3 Update the client test for the roles sub-resource to assert `isAdmin` survives decoding.

## 2. Reconciler: manage only the roles the endpoint asked for

- [x] 2.1 In `reconcilePrincipals`, filter the observed roles to those with `isAdmin` false before comparing (D1, D2).
- [x] 2.2 Compare the filtered observed set against the desired set, and issue `SetSiteResourceRoles` only on a real difference.
- [x] 2.3 Never include a server-owned role in the written set; a role an operator names explicitly is still resolved and sent normally.
- [x] 2.4 Leave the users and clients paths unchanged — neither exhibits the behaviour, and speculative handling would be guessing (Non-Goals).

## 3. Status: report the implicit grant honestly

- [x] 3.1 Keep reason `NoPrincipalsGranted`, and reword its message to say that no *named* principal has access and that org administrators still reach the endpoint through Pangolin's implicit role (D3).
- [x] 3.2 Note the implicit grant in the `access.roles` field documentation in `api/v1alpha1/pangolinendpoint_types.go`, and regenerate the CRD so the description ships.

## 4. Tests

- [x] 4.1 Make the fake attach an admin role (`isAdmin: true`) on create, and refuse to drop it on write, exactly as the live instance behaves (D4). Confirm this reproduces the loop **before** the fix.
- [x] 4.2 Regression: repeated reconciles of an endpoint naming no roles issue **zero** role writes.
- [x] 4.3 An endpoint naming one role writes that role, and does not attempt to withdraw the admin role.
- [x] 4.4 Removing a named role issues exactly one write, and a further reconcile issues none.
- [x] 4.5 Assert the `NoPrincipalsGranted` message mentions the implicit grant.
- [x] 4.6 Confirm each new test fails against the current code and passes after the fix — a test that passes both ways guards nothing.
- [x] 4.7 Run the full suite; the two pre-existing `TestIngressReconciler_Reconcile` failures are expected and unrelated.

## 5. Live verification

- [x] 5.1 Run the controller against the live instance as in `fix-private-endpoint-live-defects` (logging proxy in front of `--pangolin-base-url`), create an endpoint with no named roles, and confirm the captured traffic shows role reads with **no** role writes across several reconciles.
- [x] 5.2 Add a named role, confirm exactly one write, then confirm the following reconciles are silent. **Verified 2026-08-06** once the API key was granted role-list permission: naming `Member` issued exactly one role write and two further reconciles issued none; removing it issued exactly one more and then settled. Afterwards only the admin role remained attached, so the named role was withdrawn and the implicit grant was not.

> Note on the wire shape: the org role list and the resource role list both
> report `isAdmin` as `true` for the admin role and as **absent/null** for other
> roles. Go decodes both absent and null into `false`, so the filter treats only
> the admin role as server-owned, which is the intended reading. A future
> Pangolin that reported `isAdmin: false` explicitly would behave identically.
- [x] 5.3 Confirm the admin role is still attached afterwards — the fix must stop writing, not start removing.
- [x] 5.4 Delete the test objects and confirm the Pangolin side is clean.
