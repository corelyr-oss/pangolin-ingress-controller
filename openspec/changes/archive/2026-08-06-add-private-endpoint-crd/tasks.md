## 1. Discovery spike — BLOCKING GATE

Run against a live Pangolin instance before writing any implementation code. Record findings in `design.md` under "Open Questions" and **re-review the design** before starting section 2. Q1.3 in particular can change the shape of the reconciler.

> **This gate was skipped, and the change shipped on assumptions taken from the
> OpenAPI document instead.** Three of those assumptions were wrong, two
> seriously; see `fix-private-endpoint-live-defects`. The questions answered
> below were answered on 2026-08-06, after the fact. The rest remain open.

- [x] 1.1 Determine which resource path aliases the target instance serves: `/org/{orgID}/site-resource`, `/org/{orgID}/private-resource`, or both. Confirms D3. **Answered 2026-08-06: both.** `site-resource` and `private-resource` are live aliases; create is `PUT /org/{orgID}/site-resource` only. D3's choice of the older name stands.
- [x] 1.2 Capture the actual response body of `PUT /org/{orgID}/site-resource` and `GET /site-resource/{id}`. **Answered 2026-08-06**, with a correction: `GET /site-resource/{id}` does not work at all -- it rejects every request with `expected string, received undefined at "orgId"`, and so does `/private-resource/{id}`. The only working read is the listing `GET /org/{orgID}/site/{siteID}/resources`. Full field names in `fix-private-endpoint-live-defects/design.md` -- Context. Two fields matter beyond those guessed: `aliasAddress` (the assigned mesh address) and `status`. The listing does **not** carry `siteId` on the resource; it is in the sibling `siteNetworks` row.
- [x] 1.3 **Create a private resource whose `destination` is a cluster DNS name (`<svc>.<ns>.svc.cluster.local`) and verify a mesh client can reach it.** **Answered 2026-08-06: it works, and D7 stands.** A `PangolinEndpoint` backed by a Service produced `destination: demo-svc.pangolin-mesh.svc.cluster.local`, and a connected mesh client (`utun12`, `100.90.128.1`) reached it over the mesh: `HTTP 200` from the backing nginx, both at the assigned address `100.96.128.11:8080` and via the alias `meshtest.pangolin-mesh.k8s-test.internal:8080`, which mesh DNS resolves to that address. Pangolin therefore resolves cluster DNS on the private data path, so **no Service watch and no ClusterIP fallback are needed**. Port changes propagate to the data path: narrowing the endpoint to TCP 9999 stopped 8080 answering, and restoring 8080 brought it back.
- [x] 1.4 Determine whether `destinationPort` is meaningful alongside `tcpPortRangeString` in `mode: host`, or only for `http`/`ssh` modes. **Answered 2026-08-06: it is accepted and stored in `mode: host`** (created with `destinationPort: 8080` alongside `tcpPortRangeString: "8080"`, read back intact). Hand-made resources on the same site carry `null`, so it is optional rather than required.
- [x] 1.5 Send a deliberately non-canonical port range string (`"5433,5432"`, `"5432,5433"`) and record exactly what is read back. **Answered 2026-08-06: Pangolin normalises nothing.** Every form was stored and read back verbatim -- `"5433,5432"`, `"80,80"`, `"100-200,150-250"`, `"8080-8080"`. D5's stated premise (the server may reorder, merge or deduplicate) is **false** for create. The semantic comparison is still correct and still wanted -- it is what keeps a hand-edited but equivalent string from being rewritten on every reconcile -- but its justification is the opposite of what the design claims and should be restated.
- [x] 1.6 Attempt to create two private resources with the same `alias`. **Answered 2026-08-06: `409 "Alias already in use by another site resource"`.** Aliases are enforced unique org-wide, and the failure is loud.
- [x] 1.7 Attempt to create two private resources with the same caller-supplied `niceId`. D4's identity model depends on this failing loudly. **Answered 2026-08-06: it does not fail. `201 Created`.** Two resources were left holding `niceId: pgtest-norm-a` at once, confirmed in the site listing. Contrast task 1.6: a duplicate *alias* is refused with a `409`, so Pangolin enforces uniqueness where it chose to and simply does not for `niceId`. **D4's stated premise is false**, and recovery-by-niceId as implemented takes the first match in listing order, which under a collision can adopt the wrong resource. See the note in `design.md` under D4.
- [x] 1.8 Create a private resource with `siteIds` containing two sites and record what the destination means in that case. **Answered 2026-08-06: multi-site is usable.** A create with `siteIds: [1, 3]` was accepted and the resource appears in the listing of both sites (and not of the third site in the org), carrying one `destination` string. The destination is therefore resolved per site -- the same name means whatever it resolves to on each site. `spec.siteRefs` can stay plural. Not tested: whether a name that resolves on one site and not another produces a partial failure.
- [x] 1.9 Confirm the response shapes of `GET /org/{orgID}/roles`, `GET /org/{orgID}/clients`, and `GET /org/{orgID}/user-by-username`, and whether role and client names are unique. **Answered 2026-08-06**, with one part unobservable in this organization.

  - **Roles** (`listRoles`): `200`, `{"roles": [...], "pagination": {"total", "page", "pageSize"}}`. A role carries `roleId`, `name`, `orgId`, `orgName`, `description`, `isAdmin`, `allowSsh`, `requireDeviceApproval` and four `ssh*` fields. `isAdmin` is `true` on the admin role and **absent** on others. The org's role names are distinct. Verified end to end: an endpoint naming `Member` reconciles to `Ready=True`.
  - **User by username** (`getOrgUser`): `200`, `data` is a single user object with `userId`, `username`, `email`, `name`, `type`, `orgId`, `isOwner`, `isAdmin`, `roleIds`, `roles`, `twoFactorEnabled`, `autoProvisioned` and four `idp*` fields. The client's `User` struct decodes `userId`/`username`/`email`, all present. An unknown username returns `404 "User with username '...' not found in organization"`. Both paths verified end to end: naming `pangolin@pgp.one` resolved to `userId i6x752e2zfvws8e`, attached it to the resource and reported `Ready=True`; naming an unknown user reported `ResolvedRefs=False, PrincipalNotFound`.
  - **Clients** (`listClients`): `200`, `{"clients": [...], "pagination": {...}}`, paged by `page`/`pageSize` like roles and **not** by `limit`/`offset` like the site-resource listing. **The organization contains no clients** (`total: 0`) even though a client is connected to the mesh, so the per-client field names and whether client names are unique were not observable. This is recorded rather than resolved: the resolver refuses an ambiguous name instead of choosing (D8), so uniqueness is not load-bearing for correctness -- a duplicate client name surfaces as `PrincipalAmbiguous` rather than a wrong grant. Re-run this part against an org that has clients before relying on the client field names.

  **Operational note:** granting `listClients` and `getOrgUser` initially *removed* `listRoles`, and roles returned `403` until it was granted again. Whatever edits API-key permissions appears to replace the set rather than add to it, so re-check the whole set after any change.

## 2. API types and codegen tooling

- [x] 2.1 Add `controller-gen` to the `Makefile` (pinned version, downloaded to `bin/` like standard kubebuilder projects). Add `generate` (deepcopy) and `manifests` (CRD YAML) targets; wire `generate` ahead of `build` and `test`.
- [x] 2.2 Create `api/v1alpha1/groupversion_info.go` — group `pangolin.corelyr.com`, version `v1alpha1`, `SchemeBuilder`, `AddToScheme`.
- [x] 2.3 Create `api/v1alpha1/pangolinendpoint_types.go`: `PangolinEndpointSpec` (`BackendRef`, `SiteRefs`, `Enabled`, `Private`), `PrivateEndpointSpec` (`Alias`, `Ports`, `Access`, `DisableIcmp`), `EndpointPort` (`Protocol`, `Port`, `From`, `To`, `All`), `AccessSpec` (`Clients`, `Roles`, `Users`), `PangolinEndpointStatus` (`SiteResourceID`, `NiceID`, `Address`, `ResolvedPorts`, `Conditions`, `ObservedGeneration`).
- [x] 2.4 Add kubebuilder markers: `+kubebuilder:object:root=true`, `+kubebuilder:subresource:status`, printer columns for `Address`, `Ready`, and `Age`.
- [x] 2.5 Add CEL validation (`+kubebuilder:validation:XValidation`): `spec.private` required; `spec.public` rejected with a message naming the unimplemented branch; per-port-entry "exactly one of `port` / (`from`,`to`) / `all`"; `from <= to`.
- [x] 2.6 Run `make generate manifests`; commit `zz_generated.deepcopy.go` and the generated CRD.

## 3. Pangolin client: site resources

- [x] 3.1 Add `internal/pangolin/site_resources.go` with the path prefix in a single constant (D3).
- [x] 3.2 Add `SiteResource` struct using the field names confirmed in task 1.2.
- [x] 3.3 Add `CreateSiteResource(ctx, req) (*SiteResource, error)` → `PUT /org/{orgID}/site-resource`. Request carries `name`, `niceId`, `mode: "host"`, `siteId`/`siteIds`, `destination`, `destinationPort`, `alias`, `tcpPortRangeString`, `udpPortRangeString`, `disableIcmp`, and the required `userIds`/`roleIds`/`clientIds`.
- [x] 3.4 Add `GetSiteResource`, `UpdateSiteResource` (`POST /site-resource/{id}`), `DeleteSiteResource`.
- [x] 3.5 Add `GetSiteResourceByNiceID(ctx, siteID, niceID)` → `GET /org/{orgID}/site/{siteID}/resource/nice/{niceId}`; map 404 to a not-found signal distinct from `NotImplementedError`.
- [x] 3.6 Route all site-resource calls through `checkResponseWithNotImplemented` so a server without the endpoints yields `*NotImplementedError` (D10).
- [x] 3.7 Add `ListRoles`, `ListClients`, and `GetUserByUsername` for name resolution.

## 4. Name→ID lookup cache

- [x] 4.1 Generalize `internal/controller/domain_cache.go` into a reusable cache: a keyed mapping, refresh-on-miss, rate limiting from the last *attempt*, and retention of the existing mapping on a failed refetch.
- [x] 4.2 Re-express the domain cache in terms of the generalized type. **Every existing test in `domain_cache_test.go` must pass unmodified** — this refactor has no behavioural delta.
- [x] 4.3 Instantiate role and client caches over `ListRoles` / `ListClients`.
- [x] 4.4 Implement user resolution as a cache in front of `GetUserByUsername` point queries rather than a cached list.
- [x] 4.5 Return a distinct ambiguity error when a name matches more than one object; never select one (D8).
- [x] 4.6 **Decided: a separate `--name-cache-refresh-interval` flag** (default `60s`), recorded as D12 in `design.md`. Domains and principals churn on different cadences, and one knob for both would force an operator tuning principal lookups to also change domain-resolution latency.

## 5. Reconciler

- [x] 5.1 Add `internal/controller/pangolinendpoint_controller.go` with `PangolinEndpointReconciler`, registered with `GenerationChangedPredicate` (no annotation predicate needed — D9).
- [x] 5.2 Finalizer handling: add on first reconcile; on deletion call `DeleteSiteResource`, tolerate already-absent, remove the finalizer only after confirmation.
- [x] 5.3 Resolve `backendRef` → Service; reject headless and `ExternalName`; missing Service sets `ResolvedRefs=False`, emits a Warning event, and requeues **without** returning an error.
- [x] 5.4 Resolve `siteRefs` → site IDs via the site cache; make the site cache keyed by identifier rather than the current single-value `siteCache`.
- [x] 5.5 Derive the alias: explicit value, else `<name>.<namespace>.<suffix>`; with no suffix configured set `Accepted=False, Reason=AliasSuffixNotConfigured` and requeue without an error.
- [x] 5.6 Derive the port set: declared ports, else the Service's `spec.ports[].port`, re-derived every reconcile when unset. Serialize per protocol; `all` → `*`.
- [x] 5.7 Implement canonicalization and **semantic** comparison of port sets so server-side normalization does not cause an update every reconcile (D5).
- [x] 5.8 Derive the `niceId`; refuse rather than truncate when over length; look up by `niceId` when `.status.siteResourceId` is empty, before considering a create.
- [x] 5.9 Resolve access principal names to identifiers; ambiguity or an unknown name sets `ResolvedRefs=False` + Warning event + non-error requeue.
- [x] 5.10 Create or update the site resource; treat `*NotImplementedError` as `Accepted=False, Reason=UnsupportedByServer` with a slow requeue and no error return.
- [x] 5.11 Write status: `siteResourceId`, `niceId`, `address`, `resolvedPorts`, `observedGeneration`, and the `Accepted` / `ResolvedRefs` / `Programmed` conditions. Set `Ready=False, Reason=NoPrincipalsGranted` when no principals are granted.
- [x] 5.12 Register the reconciler and `--private-alias-suffix` in `cmd/main.go`; add `AddToScheme` for `api/v1alpha1`.

## 6. RBAC and deployment artifacts

- [x] 6.1 Add kubebuilder RBAC markers for `pangolinendpoints`, `pangolinendpoints/status`, and `pangolinendpoints/finalizers`.
- [x] 6.2 Update `deploy/clusterrole.yaml` **and** `chart/templates/clusterrole.yaml` to match — these are not generated in this repo.
- [x] 6.3 Write the generated CRD to `deploy/crds/`, which makes the existing `make install-crds` target functional for the first time.
- [x] 6.4 Add the CRD to `chart/templates/` behind a `crds.install` value defaulting to `true` — **not** `chart/crds/`, which Helm never upgrades (D11).
- [x] 6.5 Add `privateAliasSuffix` to `chart/values.yaml` and wire it into `chart/templates/deployment.yaml` and `deploy/deployment.yaml`.

## 7. Tests

- [x] 7.1 Alias derivation: explicit override, derivation, missing suffix, and name/namespace combinations that produce an invalid FQDN.
- [x] 7.2 Port serialization: single ports, ranges, `all`, mixed protocols, and ordering stability.
- [x] 7.3 Port comparison: reordered, merged-adjacent, and duplicate representations all compare equal; a genuine change compares unequal and produces exactly one update call.
- [x] 7.4 Service resolution: found, missing, headless, `ExternalName`, and port defaulting from the Service.
- [x] 7.5 `niceId` derivation and recovery-by-`niceId` when status is empty; assert no duplicate create occurs.
- [x] 7.6 Name resolution: hit, miss-then-refresh, rate-limited miss, failed refetch retaining the mapping, and ambiguity refusal.
- [x] 7.7 Condition transitions for each failure class, asserting non-error requeue (not a returned error) for missing Service, unresolved name, missing alias suffix, and `UnsupportedByServer`.
- [x] 7.8 Finalizer lifecycle: create, delete, already-absent delete, and delete blocked by an unreachable API.
- [x] 7.9 Site-resource client methods against an `httptest` server, following the pattern in `auth_reconcile_test.go`.
- [x] 7.10 Confirm `domain_cache_test.go` passes unmodified after the generalization in 4.2.

## 8. Documentation

- [x] 8.1 README: a `PangolinEndpoint` section with a full example, the alias derivation rule, the `--private-alias-suffix` requirement, and the field reference.
- [x] 8.2 README: document that an unset `ports` field tracks the Service, so adding a Service port widens the endpoint without a CR change.
- [x] 8.3 README: document that changing `--private-alias-suffix` rewrites every derived alias and changes the address clients dial.
- [x] 8.4 `IMPLEMENTATION.md`: add the site-resource and name-listing endpoints.
- [x] 8.5 `CLAUDE.md`: record that the repo now has a CRD, where the API types live, and that `deploy/crds` and `chart/templates` must be kept in step alongside the existing clusterrole pairing.

## 9. Validation

- [x] 9.1 `make fmt vet test` clean and `make generate manifests` produces no diff. **Pre-existing failure**: `TestIngressReconciler_Reconcile` fails on `main` for unrelated reasons (it configures no API-key Secret, so `initPangolinClient` returns `secrets "" not found`) — already flagged in `add-pangolin-auth-methods` task 8.1. Untouched by this change.
- [x] 9.2 Install the CRD on a real cluster and confirm the CEL rules reject a `spec.public` block, a port entry with two forms, and an inverted range. **Performed 2026-08-05/06** against Talos k8s v1.31.2: an 18-case matrix (valid and deliberately invalid) behaved exactly as specified -- `spec.public` rejected by CEL, port `oneOf`, `from <= to`, bounds and protocol enum all enforced, and the alias FQDN pattern added later rejects a bare label while accepting wildcard and glob forms.
- [x] 9.3 End-to-end against the live instance: create a `PangolinEndpoint`, verify the private resource, reach it from a mesh client, mutate ports and principals, then delete and confirm cleanup. **Completed 2026-08-06.** Create programmed the resource and reported `Ready=True`; a mesh client reached it (`HTTP 200`, `Server: nginx/1.31.3`); mutating ports moved the data path with the spec (9999 broke 8080, restoring 8080 fixed it) and earlier produced `tcp="8080,9000-9010" udp="5353"` server-side with matching `.status.resolvedPorts`; removing the named role returned `Ready=False/NoPrincipalsGranted`; deletion removed the Pangolin resource, the address stopped answering, and the org was left with only its two pre-existing resources. The Pangolin UI was not opened -- everything was verified through the API and the data path instead.

> **Observed while removing principals:** with no named principal the endpoint
> was **still reachable** (`HTTP 200`). This is direct confirmation that
> Pangolin's implicit admin grant conveys real access, which
> `fix-implicit-admin-role-loop` had argued from the API alone. The
> `NoPrincipalsGranted` message is accurate as reworded there: what such an
> endpoint lacks is access for *named* principals, not access altogether.
- [x] 9.4 Covered by `TestEndpoint_UnchangedReconcileIssuesNoUpdate` and `TestEndpoint_ServerNormalisedPortsAreNotAChange`. Writing this test found a real convergence bug: `destinationPort` was never cleared when a port set stopped being a single port, so every reconcile saw a difference and updated forever. Fixed by making the field non-`omitempty` so a nil pointer is sent as an explicit `null`.
- [x] 9.5 `openspec validate add-private-endpoint-crd` → valid.
