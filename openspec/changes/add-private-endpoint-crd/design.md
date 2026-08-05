## Context

Pangolin's Integration API (`https://api.pangolin.net/v1/docs`, OpenAPI 3.0) exposes two distinct resource families. They are not variants of one model:

```
  PUBLIC RESOURCE                        PRIVATE RESOURCE  (a.k.a. site resource)
  ────────────────────────────────       ──────────────────────────────────────────
  PUT /org/{orgId}/resource              PUT /org/{orgId}/site-resource
    ≡ /org/{orgId}/public-resource         ≡ /org/{orgId}/private-resource
  mode: http|ssh|rdp|vnc|tcp|udp         mode: host|cidr|http|ssh
  proxyPort (raw variant)                destination + destinationPort
  domainId + subdomain (http variant)    tcpPortRangeString / udpPortRangeString
                                         alias           ← internal FQDN clients dial
  + separate Target objects:             siteIds[]       ← multi-site is native
      siteId, ip, port, mode, hc*        userIds / roleIds / clientIds  (REQUIRED)
                                         disableIcmp, ssl, scheme,
                                         authDaemonPort/Mode, pamMode
  reachable: public internet             reachable: mesh clients only
```

Three facts from the spec shape everything below:

1. **Pangolin is mid-rename.** `resource`↔`public-resource` and `site-resource`↔`private-resource` are live aliases for the same objects — both private aliases still take a `siteResourceId` path parameter.
2. **Responses are untyped.** Every 2xx documents only the generic `{data, success, error, message, status}` envelope with `data` as `additionalProperties: nullable`. Response field names are not derivable from the docs and must be confirmed empirically — the same position whoever wrote the current client was in.
3. **`http` and `protocol` are deprecated** on public-resource create in favour of `mode`, and `Target` has gained a `mode` field. The existing client sends the deprecated fields and no target `mode`. Out of scope here; flagged for follow-up.

The deployment target is a **self-hosted Pangolin instance**, not Pangolin Cloud, so the published spec is an upper bound on what the server actually implements.

## Goals / Non-Goals

**Goals**

- A Kubernetes-native, schema-validated way to declare a private endpoint backed by a `Service`.
- The CR is the source of truth; the reconcile converges and is safe to run repeatedly without generating API writes when nothing changed.
- Failures that an operator can fix (missing Service, unknown role name, unsupported server) are reported on the object and requeued — not returned as controller errors.
- Identity survives loss of `.status`: the controller can always re-find the object it owns without guessing.
- Room for the public raw branch later without a schema break.

**Non-Goals**

- Public raw TCP/UDP (see proposal — unresolved identity problem).
- `mode: cidr | http | ssh` for private resources.
- Replacing or wrapping the `Ingress` path.
- Reading Pangolin-side manual edits back as authoritative. Last write from the cluster wins.

## Decisions

### D1: One kind with nested exclusive blocks (`PangolinEndpoint`)

```yaml
apiVersion: pangolin.ingress.k8s.io/v1alpha1
kind: PangolinEndpoint
metadata: {name: postgres, namespace: data}
spec:
  backendRef: {name: postgres}
  siteRefs: [my-cluster]
  enabled: true
  private:                                  # v1alpha1: required
    alias: postgres.data.corp.internal      # optional — derived if unset
    ports:                                  # optional — from the Service if unset
      - {protocol: TCP, port: 5432}
    access:
      clients: [vinzenz-laptop]
      roles:   [developers]
      users:   [office@corelyr.com]
    disableIcmp: false
status:
  siteResourceId: "88"
  niceId: pangolin-controller-data-postgres
  address: postgres.data.corp.internal
  resolvedPorts: {tcp: "5432"}
  conditions: [Accepted, ResolvedRefs, Programmed]
```

Shared fields (`backendRef`, `siteRefs`, `enabled`) sit at the top level; everything branch-specific lives inside a named block. `spec.public` is **reserved** and rejected by validation in `v1alpha1`.

**Alternative considered — a flat `exposure: Public|Private` discriminator.** Rejected: with the real API, the branches share only three fields and diverge on roughly a dozen (`proxyPort` and 14 `hc*` fields on one side; `alias`, port ranges, `clientIds`, `disableIcmp` on the other). A flat schema would make most fields conditionally invalid and push validation into a thicket of CEL conditionals.

**Alternative considered — two separate kinds.** Rejected, narrowly: it is the more honest schema, but it duplicates `backendRef`, site resolution, finalizer, and status plumbing across two reconcilers, and forecloses expressing "the same backend, exposed both ways" as one object. Nested blocks keep both doors open — relaxing "exactly one block" to "at least one" later is not a breaking change, whereas merging two kinds is.

### D2: Only `mode: host`, hardcoded and not exposed

`backendRef` always points at a Kubernetes `Service`, which is exactly one destination host. `cidr` describes a subnet and cannot come from a Service; `http` and `ssh` are Pangolin-side protocol handling that would drag in `scheme`, `ssl`, `authDaemonPort`, `authDaemonMode`, and `pamMode`. The controller sends `mode: host` unconditionally and the field does not appear in the CRD. Adding it later is additive.

### D3: Target the **old** API path names

The controller calls `/org/{orgID}/site-resource` and `/site-resource/{id}`, not the `private-resource` aliases. Both exist in the current published spec, but only the old names can be present on older self-hosted builds, so the old names are the strictly wider compatibility set. The path prefix lives in a single constant in `internal/pangolin/` so the switch is a one-line change once the rename settles.

### D4: Deterministic `niceId` is the identity; no adoption heuristics

Private-resource create accepts a caller-supplied `niceId`. The controller sets:

```
  niceId = <resource-prefix>-<namespace>-<name>      e.g. pangolin-controller-data-postgres
```

and recovers a lost `.status.siteResourceId` via `GET /org/{orgID}/site/{siteID}/resource/nice/{niceId}`. This makes identity a pure function of the CR's coordinates. There is no list-and-match adoption path and therefore no risk of claiming an object the controller does not own — a materially better position than the `Ingress` path's adopt-on-409, and the reason `v1alpha1` is private-only (public create accepts no `niceId`).

`niceId` must match `^[a-zA-Z0-9-]+$` and is capped at 255 characters. Namespaces are DNS-1123 *labels* and always fit, but object names are DNS-1123 *subdomains* and may contain dots — `db.primary` is a legal name that has no `niceId` expression. Both violations are refused (`Accepted=False`, reason `IdentityInvalid` or `IdentityTooLong`) rather than truncated or character-substituted: rewriting `db.primary` to `db-primary` would collide with an endpoint genuinely named `db-primary`, and two CRs sharing one identity would fight over a single Pangolin resource.

### D5: Structured ports, string serialization, semantic comparison

```
  spec.private.ports[]                          tcpPortRangeString / udpPortRangeString
  ┌───────────────────────────────┐             ┌──────────────────────────┐
  │ {protocol: TCP, port: 5432}   │──group by──▶│ tcp: "5432,8000-9000"    │
  │ {protocol: TCP, from: 8000,   │  protocol,  │ udp: "*"                 │
  │                 to:   9000}   │  sort, join └──────────────────────────┘
  │ {protocol: UDP, all: true}    │
  └───────────────────────────────┘
     CEL: exactly one of port / (from,to) / all
```

`all: true` exists as its own field because `"*"` is a real API capability that cannot be expressed as an integer. `from <= to` is enforced in CEL.

**Comparison is semantic, not textual.** Pangolin may normalize what it stores (reordering, merging adjacent ports into a range, deduplicating). Comparing the serialized string against the read-back string would then report a difference on every reconcile and issue an endless stream of no-op updates. Both sides are parsed into a canonical sorted set of ranges before comparison. This is a requirement, not an implementation detail, because the failure is invisible in tests that only exercise the happy path.

When `ports` is unset, the port set is derived from the Service's `spec.ports[].port` (the Service port, since the destination is the Service, not the pods) **on every reconcile**. "Unset" therefore means "track the Service", and adding a port to the Service widens the endpoint. This is deliberate and must be documented, because it is a change made without touching the CR.

### D6: Alias derivation via a suffix flag with no default

```
  alias = <name>.<namespace>.<suffix>       postgres.data.corp.internal
                              └── --private-alias-suffix, no default
```

Aliases are unique **org-wide**, so two clusters sharing one Pangolin org would derive identical aliases for same-named workloads. Shipping a default suffix would make that collision the out-of-the-box behaviour, so the flag has no default and a `PangolinEndpoint` with a derived alias fails validation (`Accepted=False`, `Reason=AliasSuffixNotConfigured`) until the operator sets one. An explicit `spec.private.alias` bypasses the flag entirely.

Changing the flag rewrites every derived alias in the cluster — a mass update that changes the DNS name every client uses. Documented as an operational hazard, not defended against in code.

The effective alias is always reported in `.status.address`, so the CR answers "what do I dial" without the reader having to know the derivation rule.

### D7: Destination is the Service's cluster DNS name

```
  destination = <service>.<namespace>.svc.cluster.local
```

This mirrors what the `Ingress` path already does for target IPs, keeps the controller free of `Service` watches, and stays correct across Service recreation (the DNS name is stable, the `ClusterIP` is not).

**This is contingent on spike Q3.** Private resources travel a different data path from public targets, so the fact that cluster DNS resolves for targets today does not prove it resolves for private resources. If it does not, the fallback is the Service's `ClusterIP`, which requires the controller to watch `Service` objects and reconcile affected endpoints on `ClusterIP` change — new machinery, and a new class of drift. The spike runs before any code is written precisely because this branch point changes the shape of the reconciler.

Headless Services (`clusterIP: None`) and `type: ExternalName` are rejected with `ResolvedRefs=False`: the former resolves to a shifting set of pod IPs, the latter has no cluster-local destination at all.

### D8: Access principals by name, resolved through a generalized lookup cache

The CR names roles, users, and clients; the controller resolves them to the IDs the API requires:

```
  roles    GET /org/{orgID}/roles              name     → roleId   (int)
  users    GET /org/{orgID}/user-by-username   username → userId   (string)
  clients  GET /org/{orgID}/clients            name     → clientId (int)
```

This is structurally identical to the domain-resolution problem the archived `fix-stale-domain-cache` change already solved: a name→ID map where a miss means either "does not exist" or "was created after we cached", and where a stale cache can only cause a spurious miss, never a spurious hit. `domainCache` is therefore generalized into a reusable lookup cache and instantiated four times. The proven semantics carry over unchanged — refresh on miss, rate-limited from the last *attempt*, never discard a good cache on a failed refetch, and a persistent miss becomes a Warning event plus a non-error requeue.

Users are resolved through `/org/{orgID}/user-by-username` (a direct lookup) rather than by listing every user, so the user resolver is a cache in front of point queries rather than a cached list.

**Ambiguous names are a hard error, never a guess.** If two roles or clients share a name, the controller reports `ResolvedRefs=False` with both IDs in the message and refuses to proceed.

**Empty access is permitted** — `clients`, `roles`, and `users` all absent produces a private resource reachable by nobody. Pangolin requires the arrays to be present, not non-empty, and "create it now, grant access later" is a legitimate workflow. The controller surfaces `Ready=False, Reason=NoPrincipalsGranted` so it is visible rather than silent.

**Alternative considered — accepting raw Pangolin IDs**, matching the existing `role-ids`/`user-ids` annotations. Rejected as the primary interface: requiring operators to look up internal integer IDs is exactly the ergonomic problem a CRD is supposed to solve. Not offered as an escape hatch either, to avoid two code paths and two failure vocabularies in `v1alpha1`.

### D9: State lives in `.status`, not in annotations

The `Ingress` path stores `resource-id` in an annotation, which forces `controllerManagedAnnotations` and a bespoke predicate so the controller's own writes do not re-trigger it. A CRD with a status subresource has no such problem: status writes do not bump `metadata.generation`, so a plain `GenerationChangedPredicate` is correct. The two reconcilers diverge here deliberately.

Conditions use the Gateway API vocabulary rather than an invented one, because it maps cleanly onto the three failure classes that already exist:

| Condition | False means |
|---|---|
| `Accepted` | the spec itself is unusable (no alias suffix, over-long `niceId`, server does not support private resources) |
| `ResolvedRefs` | something the spec points at is missing or ambiguous (Service, site, role, user, client) |
| `Programmed` | Pangolin rejected or has not yet accepted the write |

### D10: Version-skew tolerance

Against a self-hosted instance, `site-resource` may not exist at all. A `404`/`405` from the site-resource endpoints maps to the `NotImplementedError` type introduced by `add-pangolin-auth-methods` and produces `Accepted=False, Reason=UnsupportedByServer` plus a slow requeue — not an error return. A CR created against an instance that cannot serve it should sit there explaining itself, not burn exponential backoff and inflate `controller_runtime_reconcile_errors_total`.

This follows the `errDomainNotFound` precedent already established in this repo: operator-fixable conditions are events and requeues, never errors.

### D11: CRD packaging in the Helm chart uses `templates/`, not `crds/`

Helm's `crds/` directory is install-only — it never upgrades a CRD that already exists. For a `v1alpha1` API that is expected to change, that would silently strand users on the schema they first installed. The CRD therefore ships in `chart/templates/` behind a `crds.install` value (default `true`), so `helm upgrade` applies schema changes, and operators managing CRDs out-of-band can opt out. `deploy/crds/` gets the same manifest for the raw-manifest path, which finally makes the `Makefile`'s existing `install-crds` target functional.

### D12: A separate `--name-cache-refresh-interval`

Principal lookups get their own interval rather than sharing
`--domain-cache-refresh-interval`. The two answer different questions -- how
long a newly registered *domain* stays unresolvable, versus how long a newly
created *role or client* does -- and they churn on different cadences. Sharing
one knob would force an operator tuning principal lookups to also change
domain-resolution latency. Both default to `60s`, so the distinction costs
nothing until someone needs it.

### D13: controller-gen v0.17.3, not the version matching k8s 0.28

`controller-gen` is installed as its own module, so its dependencies are
independent of `go.mod`. The version that pairs with k8s 0.28 (v0.13.0) pulls
`golang.org/x/tools` v0.12.0, which does not compile under the Go 1.25 toolchain
this module requires. v0.17.3 builds and emits the same `apiextensions/v1`
output.

## Risks / Trade-offs

- **[Risk] Cluster DNS may not resolve on the private data path (D7).** The highest-impact unknown; determines whether the controller needs `Service` watches. → **Mitigation**: spike Q3 runs before implementation; both branches are designed, and the fallback is scoped in tasks.
- **[Risk] Alias collisions across clusters sharing a Pangolin org.** → **Mitigation**: no default suffix, forcing an explicit operator choice (D6). Not preventable in code — the controller cannot see the other cluster.
- **[Risk] Port auto-derivation silently widens exposure.** Adding a port to a Service opens that port on the private endpoint with no CR change. → **Mitigation**: documented as the explicit meaning of an unset `ports` field; `.status.resolvedPorts` always shows what was actually sent, so the widening is visible on the object.
- **[Risk] Untyped API responses.** Field names in create/read responses are guesses until confirmed. → **Mitigation**: spike Q2 confirms them before any client code is written; all decoding stays in one file.
- **[Risk] The API rename lands and the old paths are removed.** → **Mitigation**: single path-prefix constant (D3).
- **[Trade-off] `v1alpha1` will change.** No conversion webhooks, no storage-version migration. Users are told the API is alpha and may break. This is why the CRD ships in `templates/` rather than `crds/` (D11).
- **[Trade-off] Two reconcilers, no shared core.** The private branch shares almost nothing with the HTTP path beyond the API client, so extracting a shared core now would be speculative. It becomes worth doing when the public raw branch lands, since *that* branch reuses targets, health checks, and stale-target cleanup.

## Open Questions

Resolved by the spike in tasks section 1, before any implementation:

1. Which aliases exist on the target instance — `site-resource`, `private-resource`, or both?
2. What is the actual response shape of `PUT /org/{orgID}/site-resource` — what is the ID field called?
3. **Does `destination` accept a cluster DNS name, and does Pangolin resolve it on the private path?** (D7)
4. Do `destinationPort` and `tcpPortRangeString` interact, or is `destinationPort` only meaningful for `http`/`ssh` modes?
5. Does Pangolin normalize port range strings on read-back? (determines how much D5's canonicalization has to handle)
6. What happens on an alias collision — `409`, or silent overwrite?
7. Does a caller-supplied duplicate `niceId` fail loudly? (D4 depends on it)
8. Is `siteIds` (plural) honoured on create, and what does a multi-site private resource mean for the destination?
