## Context

`PangolinEndpoint` was shaped in `add-private-endpoint-crd` so that a `public` block could be added without a schema break: shared fields (`backendRef`, `siteRefs`, `enabled`) at the top level, branch-specific fields in named blocks. This change fills in the `public` block for Pangolin's raw TCP/UDP resources.

API surface used:

```
PUT    /org/{orgId}/resource           raw variant: {name, mode: tcp|udp, proxyPort}   (no niceId)
POST   /resource/{resourceId}          raw variant: {name, niceId, proxyPort, enabled, ...}
GET    /resource/{resourceId}          404 JSON when absent
GET    /org/{orgId}/resources          ?page=&pageSize= (default 20); rejects limit/offset with 400
DELETE /resource/{resourceId}          child targets are deleted with it
PUT    /resource/{resourceId}/target   {siteId, ip, port, enabled}
GET    /resource/{resourceId}/targets
DELETE /target/{targetId}
```

### Live findings (tunnel.tf test org, 2026-09-21)

Probed with a throwaway resource on an unused port, deleted afterwards:

| Question | Answer |
|---|---|
| Raw create `{name, mode: tcp, proxyPort}` | 201; `mode: "tcp"`, `domainId`/`subdomain`/`http`/`protocol` null |
| Update sets `niceId` | 200; persisted |
| Duplicate `niceId` on update | **409** `A resource with niceId "…" already exists` |
| Second tcp resource on the same `proxyPort` | **201**: Pangolin does *not* enforce port exclusivity |
| udp resource on a port held by tcp | 201 |
| Update `proxyPort`, `enabled`, `name` together | 200 |
| `GET /org/{orgId}/resource/{niceId}` | **absent**: Express `Cannot GET`, same as a bogus route |
| Listing | `page`/`pageSize`, `pagination.total`; entries carry `mode`, `proxyPort`, `niceId`, `name` |

## Goals / Non-Goals

**Goals:** expose a Service port on a public Pangolin port as raw TCP/UDP; deterministic identity with no adoption-by-attributes; converge on spec change and on drift found during reconcile.

**Non-Goals:** health checks, PROXY protocol, sticky sessions, access control (Pangolin has none for raw L4), creating edge entrypoints.

## Decisions

### One object, one proxy port

A Pangolin raw resource has exactly one `(protocol, proxyPort)`. The spec mirrors that — `spec.public` holds a single port rather than a list. A list would need one Pangolin resource per entry, turning one object's status into a map of IDs and its deletion into a partial-failure problem. Two ports are two `PangolinEndpoint` objects.

```yaml
spec:
  backendRef: {name: my-service}
  public:
    protocol: TCP      # default TCP
    proxyPort: 7443    # public Pangolin port
    servicePort: 7443  # optional, defaults to proxyPort
```

`servicePort` must be a port of the Service with a matching protocol; otherwise `ResolvedRefs=False` / `BackendUnsupported`. Checking this up front turns a silent black-hole (Pangolin forwarding to a port nothing listens on) into a visible condition.

### Identity: create named, record, then set niceId

Create accepts no `niceId` but does accept `name`; update accepts `niceId` and Pangolin enforces its uniqueness. The derived identity `<prefix>-<namespace>-<name>` is used for both. The reconcile:

1. creates the raw resource with `name = identity`,
2. writes the returned ID into `ep.Status.ResourceID` before anything else can fail (the error path persists status, so the ID survives a failure in step 3),
3. updates the resource with `niceId = identity`.

Lookup on later reconciles:

1. **Recorded ID** via `GET /resource/{id}`. Ours by construction; a differing `niceId` or `name` is converged.
2. **`niceId` match** in the full org listing. Server-enforced unique, so the strongest identity available.
3. **`name` match**, restricted to raw resources of the declared protocol whose `niceId` is not the identity. This recovers a resource whose ID was never recorded (crash between create and status write). `name` is caller-supplied and deterministic, which is the same standard the private branch applies to its `niceId`. More than one candidate is refused as `IdentityAmbiguous`.

The point route `GET /org/{orgId}/resource/{niceId}` is not used: it is in the OpenAPI document and absent on the live server, which is the same defect `fix-private-endpoint-live-defects` found for the private point route. Listing is paginated to completion. A listing failure is an error and is never read as "absent", so a failed lookup cannot cause a duplicate create.

A resource matched by `niceId` that is not a raw resource of the declared protocol is refused with `IdentityAmbiguous` rather than reprogrammed.

### Port exclusivity is enforced client-side

Pangolin accepts two raw resources on the same `(mode, proxyPort)`. Traefik can route an entrypoint to only one of them, so a second one either black-holes or silently steals traffic. Before a create, and before an update that changes `proxyPort`, the controller scans the same listing for a raw resource with the same mode and port that is not this endpoint's. If it finds one, it reports `Programmed=False` / `ProxyPortInUse` naming the holder's `niceId`. It never adopts the holder: a port held by someone else and an orphan look the same, and taking over the former would be a hijack.

The check is racy against a concurrent writer outside this controller; Pangolin offers nothing to make it atomic. Two endpoints *in this controller* racing for one port are serialised by nothing either, but the loser sees the winner on its next reconcile. The residual window is small and the failure is visible in Pangolin.

### Branch immutability

`spec.private` and `spec.public` map to different Pangolin object types with different status identifiers (`siteResourceId` vs `resourceId`). Allowing a switch in place would require deleting one kind and creating the other inside one reconcile with correct failure handling on both halves. A CEL transition rule forbids it instead; delete and recreate is the explicit path.

### Targets

Desired targets are `{(siteID, <svc>.<ns>.svc.cluster.local, servicePort)}` for every resolved site — one per site, so `siteRefs` with several sites gives Pangolin several backends to fail over between, the same shape the Ingress path produces. Existing targets are matched on that triple; unmatched desired targets are created and unmatched existing ones deleted. There is nothing to update in place: every field the controller sets is part of the match key.

### Status

- `.status.resourceId` — the public resource ID (new field; `siteResourceId` stays private-only).
- `.status.niceId` — as for private.
- `.status.resolvedPorts.tcp|udp` — the proxy port, so the existing `Ports` printer column shows it.
- `.status.address` — left empty. The edge hostname belongs to the Pangolin deployment, not to anything the API returns for a raw resource.
- `Ready=True` once programmed. The `NoPrincipalsGranted` rule is private-only.

### Deletion

Dispatch on which status identifier is set, not on the spec: a public object with a recorded `resourceId` is deleted via `DELETE /resource/{id}`; 404 counts as already gone.

## Risks / Trade-offs

- **[Trade-off] Recovery by `name`.** `name` is editable in the Pangolin UI and not unique. Renaming a resource to another endpoint's identity string is the only way to make the controller claim it, and a duplicate is refused rather than chosen. Recovery by `name` only runs when neither the recorded ID nor the `niceId` finds anything.
- **Client-side port check is racy** (see above).
- **Edge prerequisite is invisible to the controller.** If the Pangolin host has no entrypoint for `proxyPort`, Pangolin accepts the resource and traffic goes nowhere. The README states the prerequisite; the controller cannot check it.
