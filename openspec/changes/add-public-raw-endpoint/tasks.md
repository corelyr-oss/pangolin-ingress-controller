## 1. API

- [x] 1.1 `PublicEndpointSpec`: `protocol` (default TCP), `proxyPort` (required, 1–65535), `servicePort` (optional, 1–65535)
- [x] 1.2 Replace the private-required / public-reserved CEL rules with exactly-one-of, plus transition rules forbidding a branch switch and a protocol change
- [x] 1.3 `PangolinEndpointStatus.resourceId`; `ReasonProxyPortInUse`
- [x] 1.4 `make generate manifests` (deepcopy, `deploy/crds/`, chart template)

## 2. Pangolin client

- [x] 2.1 `Resource.ProxyPort`, `Resource.Mode`
- [x] 2.2 `CreateRawResource` / `UpdateRawResource` request types (raw variants of the create/update bodies)
- [x] 2.3 `ListAllResources`: page/pageSize pagination to `pagination.total`
- [x] 2.4 `GetResource` returns `*NotFoundError` on 404

## 3. Reconciler

- [x] 3.1 Dispatch on branch in `reconcileEndpoint`; private path unchanged
- [x] 3.2 Resolve and validate `servicePort` against the Service
- [x] 3.3 Find by recorded ID, then nice ID, then name in the listing; refuse wrong kind and duplicates
- [x] 3.4 Port pre-check → `ProxyPortInUse`; create (named) → record ID → set niceId
- [x] 3.5 Update only on difference (name, niceId, proxyPort, enabled)
- [x] 3.6 Target convergence: one per site, create missing, delete stale
- [x] 3.7 Status + conditions for the public branch (no `NoPrincipalsGranted`)
- [x] 3.8 Deletion dispatches on the recorded identifier

## 4. Tests

- [x] 4.1 Create, steady state issues no writes, proxy-port change updates in place
- [x] 4.2 Recovery by nice ID and by name; failed naming keeps the ID; failed listing creates nothing
- [x] 4.2a Taken port → `ProxyPortInUse` without writes (create and port change); other protocol allowed
- [x] 4.3 Wrong-kind nice-ID match refused; bad `servicePort` refused
- [x] 4.4 Stale target removed; deletion removes the resource
- [x] 4.5 CEL: both/neither/switch/protocol change rejected (throwaway kind cluster)
- [x] 4.6 Live run against the test org (reconciler driven in-cluster against tunnel.tf; create, steady state, port change, lost status, port conflict, delete)

## 5. Docs and release

- [x] 5.1 README: public branch, spec shape, host-side entrypoint prerequisite
- [x] 5.2 CLAUDE.md: `spec.public` is no longer rejected
- [ ] 5.3 Chart version / appVersion bump (separate release commit, after merge)
- [ ] 5.4 Follow-up change for the unpaginated `ListResources` on the Ingress adopt path (not in this change)
