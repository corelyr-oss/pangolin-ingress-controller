## Why

The controller can only express what fits into a Kubernetes `Ingress`: an HTTP resource, reachable at a public hostname, proxied through Pangolin's public entrypoint. Pangolin also supports **private resources** (called *site resources* in the older API surface) — endpoints that have no public entrypoint at all and are reachable only by clients connected to the Pangolin mesh, addressed by an internal FQDN.

There is no way to express that as an `Ingress`. There is no hostname to route on, no TLS to terminate, no HTTP semantics to hang annotations off, and the access-control model is mandatory rather than optional (a private resource is created *with* its permitted clients, roles, and users). Encoding this in annotations on a dummy `Ingress` would be a lie about what the object is.

Today, teams running everything as IaC have to create private endpoints by hand in the Pangolin UI, which breaks the "the cluster is the source of truth" property the Ingress path already provides.

## What Changes

- Introduce the repository's **first CRD**: `PangolinEndpoint` (`pangolin.corelyr.com/v1alpha1`, namespaced), for Pangolin resources that cannot be modelled as an `Ingress`.
- Ship a second reconciler (`PangolinEndpointReconciler`) alongside `IngressReconciler`. The `Ingress` path is unchanged.
- `v1alpha1` implements the **private** branch only. A `spec.private` block declares an internal endpoint backed by a Kubernetes `Service`:
  - destination resolved from `backendRef` to the Service's cluster DNS name
  - ports declared **structurally** (`{protocol, port}` / `{protocol, from, to}` / `{protocol, all}`) and serialized into Pangolin's `tcpPortRangeString` / `udpPortRangeString`
  - `alias` (the internal FQDN clients dial) **auto-derived** as `<name>.<namespace>.<suffix>` from a new required-when-used `--private-alias-suffix` flag, overridable per object
  - access principals (`clients`, `roles`, `users`) referenced **by name**, resolved to Pangolin IDs by the controller
- Extend `internal/pangolin/` with site-resource CRUD (`PUT /org/{orgID}/site-resource`, `GET|POST|DELETE /site-resource/{id}`, `GET /org/{orgID}/site/{siteID}/resource/nice/{niceId}`) and org-scoped name listings for roles, users, and clients.
- Generalize the existing `domainCache` into a reusable name→ID lookup cache and instantiate it for roles, users, and clients. Domain resolution behaviour is preserved exactly; only the implementation is shared.
- Introduce CRD tooling the repo does not have yet: an `api/v1alpha1` package, `controller-gen` for deepcopy and CRD manifests, scheme registration in `cmd/main.go`, and CRD artifacts in both `deploy/crds/` (the `Makefile` already has dead `install-crds`/`uninstall-crds` targets pointing there) and the Helm chart.

**Non-goals for this change:**

- **The public raw TCP/UDP branch.** The `spec` is shaped so a `public` block slots in without a schema break, but it is not implemented here. Pangolin's public-resource create does not accept a caller-supplied `niceId`, so a resource whose ID is lost can only be re-found by matching `(mode, proxyPort)` — which is indistinguishable from another owner having taken that port. Adopting on that basis would be hijacking. That needs its own design.
- **Private `mode: cidr | http | ssh`.** Only `host` is implemented, because `backendRef` is always a Service. This drops `scheme`, `ssl`, `authDaemonPort`, `authDaemonMode`, `pamMode`, `domainId`, and `subdomain` from scope.
- **Migrating the `Ingress` path onto the CRD.** The two reconcilers stay independent.
- **Pangolin's newer `resource-policy` subsystem.** Noted as adjacent work; not touched here.

## Capabilities

### New Capabilities
- `private-endpoint-crd`: declaring Pangolin private (mesh-only) endpoints as Kubernetes custom resources backed by a `Service`, including alias derivation, structured port declaration, deterministic identity, and status reporting.
- `pangolin-name-resolution`: resolving Pangolin role, user, and client **names** to the internal IDs the API requires, with the same cache-freshness and operator-fixable-miss semantics already established for domains.

### Modified Capabilities
- `domain-cache-refresh`: **no behavioural delta.** The cache implementation is generalized to serve four lookup types; every requirement in the existing spec continues to hold unchanged. Listed here only so the refactor is traceable.

## Impact

- **Code**:
  - `api/v1alpha1/` (new): `PangolinEndpoint` types, `groupversion_info.go`, generated `zz_generated.deepcopy.go`.
  - `internal/controller/pangolinendpoint_controller.go` (new): the reconciler.
  - `internal/controller/domain_cache.go`: generalized into a reusable name→ID cache; domain behaviour preserved.
  - `internal/pangolin/site_resources.go` (new): site-resource CRUD + role/user/client listings.
  - `cmd/main.go`: scheme registration, `--private-alias-suffix`, second `SetupWithManager`.
- **Tooling**: `controller-gen` dependency; `make generate` / `make manifests` targets; the existing `install-crds` target starts working.
- **Deployment**: new CRD manifests in `deploy/crds/` and the Helm chart; new RBAC for the CRD, its `status`, and its `finalizers` in both `deploy/clusterrole.yaml` and `chart/templates/clusterrole.yaml`.
- **API surface**: purely additive. No `Ingress` behaviour changes; clusters that never create a `PangolinEndpoint` are unaffected.
- **Dependencies**: `controller-gen` (build-time only). No new runtime module dependencies expected.
- **Docs**: a new README section for the CRD; `IMPLEMENTATION.md` endpoint list extended.
- **Risk**: this change is **gated on a discovery spike** (tasks section 1) against a live Pangolin instance. One outcome — that Pangolin cannot resolve cluster DNS names on the private data path — would force the controller to watch `Service` objects for their `ClusterIP`, which it does for nothing today. The design records both branches.
