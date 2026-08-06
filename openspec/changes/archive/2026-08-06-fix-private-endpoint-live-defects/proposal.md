## Why

`add-private-endpoint-crd` was written against Pangolin's OpenAPI document
rather than a live instance, and its section 1 discovery spike was never run.
A live test on 2026-08-05 (org `tunnel-tf`, site `decent-giant-pangolin`,
Talos/k8s v1.31.2) created real private resources and found that the document
and the running server disagree on three routes. Two of those disagreements are
severe: **a `PangolinEndpoint` cannot converge after its own first successful
create**, and **a lost `.status.siteResourceId` silently produces a duplicate
resource**. A third is a security defect: declaring a TCP-only endpoint opens
every UDP port on the mesh address.

None of this is reachable by unit test — every one of these defects passed the
existing suite, because the suite asserts against the same assumed API shape the
client was written from.

## What Changes

- **Read site resources through the list routes.** `GET /site-resource/{id}` and
  `GET /private-resource/{id}` both return `400 "expected string, received
  undefined at orgId"` on the live server; there is no path form that satisfies
  them. `GET /org/{orgId}/site/{siteId}/resources` works and returns the full
  object. Today the failing GET aborts every reconcile after the first create,
  so the update path has never once executed in production.
- **Stop treating a missing route as a missing resource.** `GET
  /org/{orgId}/site/{siteId}/resource/nice/{niceId}` does not exist on the live
  server (Express `Cannot GET`, HTML 404) even though the instance's own
  OpenAPI advertises it. The client maps 404 to not-found, so recovery-by-niceId
  reports "no resource yet" and creates a second one. Recovery moves to
  list-and-match on the caller-supplied `niceId`.
- **BREAKING (behavioural, security): send both port range strings explicitly.**
  `udpPortRangeString` is `omitempty`, so a TCP-only endpoint omits it and
  Pangolin substitutes `*`. Two endpoints created during the test declaring only
  TCP 8080 came back with `udpPortRangeString: "*"` — every UDP port exposed to
  every principal granted access. Existing endpoints created by the current code
  are already in this state and are corrected on the next reconcile.
- **Refuse an ambiguous identity instead of picking one.** Pangolin accepts a
  duplicate `niceId` with `201` (spike task 1.7, answered 2026-08-06) while
  refusing a duplicate `alias` with `409`, so nothing server-side prevents two
  resources sharing the identity the controller recovers by. Matching the first
  one in listing order can adopt a resource the controller does not own, which
  is the failure the identity model was written to make impossible.
- **Reject a non-FQDN alias at admission.** Pangolin requires an FQDN; the CRD
  accepts any string, so `alias: demo-derived` is accepted by Kubernetes and
  then fails forever in the reconcile loop.
- **Surface the address clients actually dial.** Pangolin assigns an
  `aliasAddress` (e.g. `100.96.128.11`); `.status.address` currently reports the
  alias only.
- Record the live findings — real response field names, the working and missing
  routes — against the `add-private-endpoint-crd` section 1 spike questions they
  answer.

Out of scope: the CRD API group rename (`pangolin.ingress.k8s.io` →
`pangolin.corelyr.com`), which was the fourth defect found in the same session
and is already applied on this branch. Also out of scope: the pre-existing
`TestIngressReconciler_Reconcile` failures on `main`, which are unrelated to
the private-endpoint path.

## Capabilities

### New Capabilities

None. This change corrects behaviour introduced by `add-private-endpoint-crd`.

### Modified Capabilities

- `private-endpoint-crd`: the resource-read, identity-recovery, port-programming
  and alias-validation requirements change. The capability is introduced by the
  pending `add-private-endpoint-crd` change and is not yet published under
  `openspec/specs/`; this change carries a delta against it, to be merged in
  change order at archive time.

## Impact

- `internal/pangolin/site_resources.go` — read and recovery methods; the
  `omitempty` on both port-range fields in the create and update requests.
- `internal/controller/pangolinendpoint_controller.go` — recovery path,
  ambiguity refusal, port derivation for the undeclared protocol, status
  `address`.
- `internal/controller/ports.go` — the semantic comparison must cover the
  protocol that was not declared, or the `*` drift stays invisible.
- `api/v1alpha1/pangolinendpoint_types.go` — alias CEL validation; regenerated
  CRD in `deploy/crds/` and `chart/templates/`.
- Tests: the existing `httptest` fixtures encode the wrong routes and the wrong
  create body, and must be re-pinned to the captured live shapes.
- Operational: endpoints created by the current code carry a `*` UDP range and
  are silently narrowed on the next reconcile after upgrade.
