## 1. Discovery spike — BLOCKING GATE

Run against a live Pangolin instance before writing any implementation code. Record findings in `design.md` under "Open Questions" and **re-review the design** before starting section 2. Q1.3 in particular can change the shape of the reconciler.

- [ ] 1.1 Determine which resource path aliases the target instance serves: `/org/{orgID}/site-resource`, `/org/{orgID}/private-resource`, or both. Confirms D3.
- [ ] 1.2 Capture the actual response body of `PUT /org/{orgID}/site-resource` and `GET /site-resource/{id}`. Record the real field names (identifier, `niceId`, `alias`, port range strings, `enabled`). The OpenAPI document types every 2xx as a generic envelope, so this cannot be derived from the docs.
- [ ] 1.3 **Create a private resource whose `destination` is a cluster DNS name (`<svc>.<ns>.svc.cluster.local`) and verify a mesh client can reach it.** If it fails, retest with the Service `ClusterIP` and add tasks for a `Service` watch + reconcile-on-ClusterIP-change; revise D7 before proceeding.
- [ ] 1.4 Determine whether `destinationPort` is meaningful alongside `tcpPortRangeString` in `mode: host`, or only for `http`/`ssh` modes.
- [ ] 1.5 Send a deliberately non-canonical port range string (`"5433,5432"`, `"5432,5433"`) and record exactly what is read back. Determines how much normalization the semantic comparison in D5 must absorb.
- [ ] 1.6 Attempt to create two private resources with the same `alias`. Record whether it 409s, overwrites, or silently accepts.
- [ ] 1.7 Attempt to create two private resources with the same caller-supplied `niceId`. D4's identity model depends on this failing loudly.
- [ ] 1.8 Create a private resource with `siteIds` containing two sites and record what the destination means in that case. If multi-site is not usable, reduce `spec.siteRefs` to a single `spec.siteRef` before the API ships.
- [ ] 1.9 Confirm the response shapes of `GET /org/{orgID}/roles`, `GET /org/{orgID}/clients`, and `GET /org/{orgID}/user-by-username`, and whether role and client names are unique.

## 2. API types and codegen tooling

- [ ] 2.1 Add `controller-gen` to the `Makefile` (pinned version, downloaded to `bin/` like standard kubebuilder projects). Add `generate` (deepcopy) and `manifests` (CRD YAML) targets; wire `generate` ahead of `build` and `test`.
- [ ] 2.2 Create `api/v1alpha1/groupversion_info.go` — group `pangolin.ingress.k8s.io`, version `v1alpha1`, `SchemeBuilder`, `AddToScheme`.
- [ ] 2.3 Create `api/v1alpha1/pangolinendpoint_types.go`: `PangolinEndpointSpec` (`BackendRef`, `SiteRefs`, `Enabled`, `Private`), `PrivateEndpointSpec` (`Alias`, `Ports`, `Access`, `DisableIcmp`), `EndpointPort` (`Protocol`, `Port`, `From`, `To`, `All`), `AccessSpec` (`Clients`, `Roles`, `Users`), `PangolinEndpointStatus` (`SiteResourceID`, `NiceID`, `Address`, `ResolvedPorts`, `Conditions`, `ObservedGeneration`).
- [ ] 2.4 Add kubebuilder markers: `+kubebuilder:object:root=true`, `+kubebuilder:subresource:status`, printer columns for `Address`, `Ready`, and `Age`.
- [ ] 2.5 Add CEL validation (`+kubebuilder:validation:XValidation`): `spec.private` required; `spec.public` rejected with a message naming the unimplemented branch; per-port-entry "exactly one of `port` / (`from`,`to`) / `all`"; `from <= to`.
- [ ] 2.6 Run `make generate manifests`; commit `zz_generated.deepcopy.go` and the generated CRD.

## 3. Pangolin client: site resources

- [ ] 3.1 Add `internal/pangolin/site_resources.go` with the path prefix in a single constant (D3).
- [ ] 3.2 Add `SiteResource` struct using the field names confirmed in task 1.2.
- [ ] 3.3 Add `CreateSiteResource(ctx, req) (*SiteResource, error)` → `PUT /org/{orgID}/site-resource`. Request carries `name`, `niceId`, `mode: "host"`, `siteId`/`siteIds`, `destination`, `destinationPort`, `alias`, `tcpPortRangeString`, `udpPortRangeString`, `disableIcmp`, and the required `userIds`/`roleIds`/`clientIds`.
- [ ] 3.4 Add `GetSiteResource`, `UpdateSiteResource` (`POST /site-resource/{id}`), `DeleteSiteResource`.
- [ ] 3.5 Add `GetSiteResourceByNiceID(ctx, siteID, niceID)` → `GET /org/{orgID}/site/{siteID}/resource/nice/{niceId}`; map 404 to a not-found signal distinct from `NotImplementedError`.
- [ ] 3.6 Route all site-resource calls through `checkResponseWithNotImplemented` so a server without the endpoints yields `*NotImplementedError` (D10).
- [ ] 3.7 Add `ListRoles`, `ListClients`, and `GetUserByUsername` for name resolution.

## 4. Name→ID lookup cache

- [ ] 4.1 Generalize `internal/controller/domain_cache.go` into a reusable cache: a keyed mapping, refresh-on-miss, rate limiting from the last *attempt*, and retention of the existing mapping on a failed refetch.
- [ ] 4.2 Re-express the domain cache in terms of the generalized type. **Every existing test in `domain_cache_test.go` must pass unmodified** — this refactor has no behavioural delta.
- [ ] 4.3 Instantiate role and client caches over `ListRoles` / `ListClients`.
- [ ] 4.4 Implement user resolution as a cache in front of `GetUserByUsername` point queries rather than a cached list.
- [ ] 4.5 Return a distinct ambiguity error when a name matches more than one object; never select one (D8).
- [ ] 4.6 Add a `--name-cache-refresh-interval` flag mirroring `--domain-cache-refresh-interval`, or reuse the existing flag if the two intervals should not diverge — decide and record in `design.md`.

## 5. Reconciler

- [ ] 5.1 Add `internal/controller/pangolinendpoint_controller.go` with `PangolinEndpointReconciler`, registered with `GenerationChangedPredicate` (no annotation predicate needed — D9).
- [ ] 5.2 Finalizer handling: add on first reconcile; on deletion call `DeleteSiteResource`, tolerate already-absent, remove the finalizer only after confirmation.
- [ ] 5.3 Resolve `backendRef` → Service; reject headless and `ExternalName`; missing Service sets `ResolvedRefs=False`, emits a Warning event, and requeues **without** returning an error.
- [ ] 5.4 Resolve `siteRefs` → site IDs via the site cache; make the site cache keyed by identifier rather than the current single-value `siteCache`.
- [ ] 5.5 Derive the alias: explicit value, else `<name>.<namespace>.<suffix>`; with no suffix configured set `Accepted=False, Reason=AliasSuffixNotConfigured` and requeue without an error.
- [ ] 5.6 Derive the port set: declared ports, else the Service's `spec.ports[].port`, re-derived every reconcile when unset. Serialize per protocol; `all` → `*`.
- [ ] 5.7 Implement canonicalization and **semantic** comparison of port sets so server-side normalization does not cause an update every reconcile (D5).
- [ ] 5.8 Derive the `niceId`; refuse rather than truncate when over length; look up by `niceId` when `.status.siteResourceId` is empty, before considering a create.
- [ ] 5.9 Resolve access principal names to identifiers; ambiguity or an unknown name sets `ResolvedRefs=False` + Warning event + non-error requeue.
- [ ] 5.10 Create or update the site resource; treat `*NotImplementedError` as `Accepted=False, Reason=UnsupportedByServer` with a slow requeue and no error return.
- [ ] 5.11 Write status: `siteResourceId`, `niceId`, `address`, `resolvedPorts`, `observedGeneration`, and the `Accepted` / `ResolvedRefs` / `Programmed` conditions. Set `Ready=False, Reason=NoPrincipalsGranted` when no principals are granted.
- [ ] 5.12 Register the reconciler and `--private-alias-suffix` in `cmd/main.go`; add `AddToScheme` for `api/v1alpha1`.

## 6. RBAC and deployment artifacts

- [ ] 6.1 Add kubebuilder RBAC markers for `pangolinendpoints`, `pangolinendpoints/status`, and `pangolinendpoints/finalizers`.
- [ ] 6.2 Update `deploy/clusterrole.yaml` **and** `chart/templates/clusterrole.yaml` to match — these are not generated in this repo.
- [ ] 6.3 Write the generated CRD to `deploy/crds/`, which makes the existing `make install-crds` target functional for the first time.
- [ ] 6.4 Add the CRD to `chart/templates/` behind a `crds.install` value defaulting to `true` — **not** `chart/crds/`, which Helm never upgrades (D11).
- [ ] 6.5 Add `privateAliasSuffix` to `chart/values.yaml` and wire it into `chart/templates/deployment.yaml` and `deploy/deployment.yaml`.

## 7. Tests

- [ ] 7.1 Alias derivation: explicit override, derivation, missing suffix, and name/namespace combinations that produce an invalid FQDN.
- [ ] 7.2 Port serialization: single ports, ranges, `all`, mixed protocols, and ordering stability.
- [ ] 7.3 Port comparison: reordered, merged-adjacent, and duplicate representations all compare equal; a genuine change compares unequal and produces exactly one update call.
- [ ] 7.4 Service resolution: found, missing, headless, `ExternalName`, and port defaulting from the Service.
- [ ] 7.5 `niceId` derivation and recovery-by-`niceId` when status is empty; assert no duplicate create occurs.
- [ ] 7.6 Name resolution: hit, miss-then-refresh, rate-limited miss, failed refetch retaining the mapping, and ambiguity refusal.
- [ ] 7.7 Condition transitions for each failure class, asserting non-error requeue (not a returned error) for missing Service, unresolved name, missing alias suffix, and `UnsupportedByServer`.
- [ ] 7.8 Finalizer lifecycle: create, delete, already-absent delete, and delete blocked by an unreachable API.
- [ ] 7.9 Site-resource client methods against an `httptest` server, following the pattern in `auth_reconcile_test.go`.
- [ ] 7.10 Confirm `domain_cache_test.go` passes unmodified after the generalization in 4.2.

## 8. Documentation

- [ ] 8.1 README: a `PangolinEndpoint` section with a full example, the alias derivation rule, the `--private-alias-suffix` requirement, and the field reference.
- [ ] 8.2 README: document that an unset `ports` field tracks the Service, so adding a Service port widens the endpoint without a CR change.
- [ ] 8.3 README: document that changing `--private-alias-suffix` rewrites every derived alias and changes the address clients dial.
- [ ] 8.4 `IMPLEMENTATION.md`: add the site-resource and name-listing endpoints.
- [ ] 8.5 `CLAUDE.md`: record that the repo now has a CRD, where the API types live, and that `deploy/crds` and `chart/templates` must be kept in step alongside the existing clusterrole pairing.

## 9. Validation

- [ ] 9.1 `make fmt vet test` clean; `make generate manifests` produces no diff.
- [ ] 9.2 Install the CRD on a real cluster and confirm the CEL rules reject a `spec.public` block, a port entry with two forms, and an inverted range.
- [ ] 9.3 End-to-end against the live instance: create a `PangolinEndpoint`, verify the private resource in the Pangolin UI, reach it from a mesh client, mutate ports and principals, then delete and confirm cleanup.
- [ ] 9.4 Reconcile an unchanged endpoint repeatedly and confirm **zero** update calls are issued after the first — the regression guard for D5.
- [ ] 9.5 `openspec validate add-private-endpoint-crd`.
