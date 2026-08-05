## Context

See `proposal.md` — Why. What matters for the approach is what the live
instance at `api.tunnel.tf` actually serves, captured 2026-08-05 by proxying the
controller's own traffic:

| Route | Result |
| --- | --- |
| `PUT /org/{orgId}/site-resource` | works — create, accepts caller-supplied `niceId` |
| `POST /site-resource/{id}` | untested — unreachable behind the broken read |
| `DELETE /site-resource/{id}` | works |
| `GET /org/{orgId}/site/{siteId}/resources` | works — returns the full object |
| `GET /org/{orgId}/site/{siteId}/private-resources` | works — same payload |
| `GET /org/{orgId}/site-resources` | works — org-wide |
| `GET /site-resource/{id}` | **400** `expected string, received undefined at "orgId"` |
| `GET /private-resource/{id}` | **400** — same |
| `GET /org/{orgId}/site/{siteId}/resource/nice/{niceId}` | **404** `Cannot GET` — route absent |

The instance's own OpenAPI document lists all of these, including the two that
do not work, and types the point-read's `orgId` and `siteId` as *path*
parameters on a template that has no place for them. The document is therefore
not a reliable source for this API surface; the captured payloads are.

The real object shape, from the site listing (entries are nested under a
`siteResources` key inside each element):

```json
{"siteResourceId": 5, "orgId": "tunnel-tf", "niceId": "pgtest-...-live-explicit",
 "name": "...", "mode": "host", "destination": "demo-svc.ns.svc.cluster.local",
 "destinationPort": 8080, "alias": "live-explicit.k8s-test.internal",
 "aliasAddress": "100.96.128.12", "tcpPortRangeString": "8080",
 "udpPortRangeString": "*", "disableIcmp": false, "enabled": true,
 "status": "approved", "ssl": false, "scheme": null, "proxyPort": null,
 "domainId": null, "subdomain": null, "fullDomain": null,
 "authDaemonPort": 22123, "authDaemonMode": "site", "pamMode": "push",
 "networkId": 3, "defaultNetworkId": null}
```

`udpPortRangeString: "*"` in that capture is the defect, not the intent: the
create sent no UDP range at all.

## Goals / Non-Goals

**Goals:**

- A programmed endpoint converges on every subsequent reconcile.
- Identity recovery cannot produce a duplicate, including when a route is absent.
- The port set Pangolin ends up with is the port set the user declared.
- Tests are pinned to captured live payloads, not to assumed ones.

**Non-Goals:**

- Supporting Pangolin instances whose only read route is the point-read. No such
  instance is known; the list routes have been present on every version tested.
- Reworking the access-principal model, multi-site fan-out, or the public
  branch. Those questions from the parent change's section 1 spike remain open.
- Restoring the `niceId` point-lookup if a later Pangolin ships it. The
  list-and-match path is correct on every version and is worth keeping as the
  single code path.

## Decisions

### D1: Read through the site listing, not a point route

`GetSiteResource(id)` is replaced by a read that lists the site's private
resources and selects the entry whose `siteResourceId` matches.

*Why:* it is the only read that works, and it is the same call recovery needs,
so one request per reconcile serves both. *Alternative rejected:* the org-wide
`GET /org/{orgId}/site-resources` — it grows with the whole org rather than the
one site the endpoint is attached to, and the controller always knows the site.

The cost is O(resources on the site) per reconcile instead of O(1). At the scale
this controller targets (tens of resources per site) that is a single small
response, and it is bounded by the same reconcile rate as before. No cache is
introduced: a cache here would reintroduce exactly the staleness class that
`domain_cache.go` exists to manage, for a call that is already cheap.

### D2: Recovery matches `niceId` within that listing

Recovery filters the same listing by the derived `niceId`.

*Why:* the `niceId` is caller-supplied and deterministic, so matching it is not
the attribute-guessing that D4 of the parent change rules out — it is the same
identity the controller itself wrote. The distinction that matters is between
*the listing succeeded and contained no match* (absent — safe to create) and
*the listing failed* (unknown — must not create). The current code cannot draw
that distinction because it asks a route that 404s and reads the 404 as "absent".

Only the first case may proceed to a create. Any listing failure sets
`Programmed=False` and requeues.

### D2a: An ambiguous identity is refused, never resolved by choice

When more than one resource on the site carries the derived `niceId`, recovery
reports an ambiguity rather than returning a candidate. The controller writes
nothing: no create, and no update to either candidate.

*Why:* spike task 1.7 established that Pangolin accepts a duplicate `niceId`
with `201`, while refusing a duplicate `alias` with `409`. So the uniqueness
D4 of the parent change assumed is not enforced anywhere, and "the first match
in listing order" is an arbitrary choice between two resources the controller
may or may not own. Adopting the wrong one would then reprogram a resource
belonging to something else — the precise failure the identity model exists to
prevent, arrived at through the recovery path instead of through adoption.

The controller's own derivation cannot collide with itself: `niceId` is
injective over (namespace, name) within one cluster. A collision means
something outside this controller's control produced it — two clusters sharing
an organization under the same `--resource-prefix`, or a hand-made resource —
and that is an operator's to resolve, not the controller's to guess at.

*Reported as:* `Programmed=False` with reason `IdentityAmbiguous`, a Warning
event, and a non-error requeue. It sits on `Programmed` rather than `Accepted`
because the spec is fine — it is the server's state that is unusable — and it
follows D8's existing treatment of an ambiguous principal name, which likewise
refuses rather than selecting.

*Alternative rejected:* preferring the lowest `siteResourceId`, or the oldest.
Both are deterministic, and both are still a guess; determinism does not make
the choice correct, it only makes the wrong choice repeatable.

A recorded `.status.siteResourceId` is still honoured when it resolves — an
endpoint that already knows which resource is its own has no identity question
to answer, so a collision elsewhere on the site does not disturb it.

### D3: Both port range strings are non-`omitempty`, empty means none

Both `tcpPortRangeString` and `udpPortRangeString` lose `omitempty` on create
and update, and a protocol with no declared ports is sent as `""`.

*Why:* this is the same trap CLAUDE.md already documents for
`UpdateSiteResourceRequest.DestinationPort` — a field the server fills in when
absent must be sent explicitly or the controller can never converge on it. Here
the server's fill-in value is `*`, so the bug is also a security defect.

**Confirmed 2026-08-06 against the live instance (task 1):** the empty string is
the representation of "no ports".

| `udpPortRangeString` sent | Result |
| --- | --- |
| field omitted | `201`, reads back as `*` — the defect |
| `""` | `201`, reads back as `""` |
| `null` | `400` `expected string, received null at "udpPortRangeString"` |

Both were created on the same site in the same session, so the contrast is
direct: resource 6 (field omitted, by the controller) read back `udp='*'` while
resource 7 (explicit `""`) read back `udp=''`. `null` is not an option, so the
field must be present and empty — which is why `omitempty` has to go rather than
being replaced by a pointer.

`POST /site-resource/{id}` was also exercised for the first time and returns
`200` with the full updated object, using the same path the broken `GET`
rejects. The defect is specific to that one handler, not to the path shape.

One limitation: `""` is confirmed to *persist* as distinct from `*`, but that it
denies UDP on the data path is inferred from the contrast, not observed with a
mesh client. Task 6.2 checks it end to end.

### D4: Alias validated by pattern at admission

`spec.private.alias` gets a CEL/pattern rule requiring at least two
dot-separated labels.

*Why:* Pangolin's own error names the rule ("must be a fully qualified domain
name with optional wildcards"), and admission is where an operator sees it
immediately. The rule is deliberately looser than Pangolin's — it permits the
wildcard and glob forms Pangolin's message advertises (`*.example.com`,
`host-0?.example.internal`) rather than trying to mirror the server's regex
exactly. Validation that is stricter than the server rejects working configs;
validation that is looser only forfeits an early error.

### D5: Assigned address is added to status, `address` keeps its meaning

`.status.address` continues to report the alias. The Pangolin-assigned
`aliasAddress` is reported in a new field with its own printer column.

*Why:* changing what `address` means would silently repoint anything already
reading it. Both values are useful — the alias is what a user configures, the
assigned address is what a client dials.

### D6: Fixtures are pinned to captured payloads

The `httptest` fixtures encode the routes and the create body the client was
assumed to use, which is why the suite passed against a client that cannot read
its own resources. They are re-pinned to the payloads captured above, and a
create-then-read round trip becomes a test in its own right.

*Why:* the class of defect this change fixes is "the assumed API and the real
API differ", and only fixtures taken from the real API can catch a recurrence.

## Risks / Trade-offs

- **Narrowing UDP on upgrade breaks a user who came to depend on the accidental
  wildcard** → It is a security fix and the narrowing is the point, but it is a
  behaviour change on resources that already exist. Called out in the proposal
  and in the migration note below; anyone who genuinely wants all UDP ports can
  declare `all: true`.
- **A listing-based read is O(n) per reconcile** → Bounded by site size and by
  the existing reconcile rate; no cache, per D1. If a site ever holds enough
  resources for this to matter, the fix is pagination on the listing, not a
  cache.
- **Task 1 could find that "no ports" is inexpressible** → Then D3's fallback
  applies and the change returns through the proposal rather than being patched
  around in code. This is why task 1 gates the rest.
- **The parent change is unarchived, so this delta stacks on a spec that is not
  yet published** → The two must be archived in order. If `add-private-endpoint-crd`
  is instead revised in place before it ever ships, these fixes should be folded
  into it and this change withdrawn.

## Migration Plan

No schema migration. On upgrade, each existing `PangolinEndpoint` reconciles
once and issues a single update that narrows the wildcard range on any protocol
it never declared; a second reconcile issues nothing. Rollback to the previous
version re-widens those ranges to `*` on the next write, which is the defect
being fixed — so rollback is safe for availability and not for exposure.

## Open Questions

- Whether `GET /org/{orgId}/site/{siteId}/private-resources` and
  `.../resources` are guaranteed to stay equivalent, or whether one is the
  forward-looking alias. Either works today; the constant is already isolated
  per D3 of the parent change, so switching later is a one-line change.
