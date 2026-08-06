## 1. Confirm the wire representation of "no ports" — BLOCKING GATE

D3 in `design.md` depends on this and section 3 cannot be written without the
answer. Run against a live instance, following the method in the
`add-private-endpoint-crd` session: point `--pangolin-base-url` at a local
logging proxy so the real request and response are captured.

- [x] 1.1 Create a private resource sending `tcpPortRangeString: "8080"` and `udpPortRangeString: ""`. Record whether it is accepted, and what both ranges read back as.
- [x] 1.2 If `""` is rejected or re-defaulted to `*`, retry with the other plausible spellings (field present and `null`, and a range that matches nothing) and record each result.
- [x] 1.3 Record the answer in `design.md` under D3. **If no representation of "no ports" exists, stop** — D3's fallback changes the spec, so return through the proposal rather than coding around it.
- [x] 1.4 While connected, capture the response body of a successful `POST /site-resource/{id}` update — it has never been observed, and section 5 pins fixtures to it.

## 2. Client: reads that work

- [x] 2.1 Replace `GetSiteResource` with a read that lists the site's private resources and selects by `siteResourceId`, per D1. Unwrap the `siteResources` key each listing entry nests the object under.
- [x] 2.2 Add a listing call scoped to a site, with the path prefix kept in the single existing constant (D3 of the parent change).
- [x] 2.3 Replace `GetSiteResourceByNiceID` with a match against that same listing, per D2. Delete the point-lookup route — it does not exist on the server.
- [x] 2.4 Return a not-found signal **only** when the listing succeeded and held no match; surface every listing failure as an error distinct from not-found.
- [x] 2.5 Add `aliasAddress` and `status` to the `SiteResource` struct, and reconcile the struct's remaining fields against the captured payload in `design.md` — Context.

## 3. Client: port ranges are explicit

- [x] 3.1 Remove `omitempty` from `tcpPortRangeString` and `udpPortRangeString` on both the create and the update request, mirroring the existing `DestinationPort` treatment.
- [x] 3.2 Serialize a protocol with no declared ports using the representation confirmed in task 1.3.
- [x] 3.3 Extend the semantic comparison in `internal/controller/ports.go` to cover a protocol with no declared ports, so a server-side `*` on it compares unequal.

## 4. Reconciler and API

- [x] 4.1 Route recovery through the new listing match; ensure a listing failure sets `Programmed=False` and requeues without creating.
- [x] 4.2 Add the assigned address to `PangolinEndpointStatus` and populate it from `aliasAddress` (D5). Keep `.status.address` reporting the alias.
- [x] 4.3 Add a printer column for the assigned address.
- [x] 4.4 Add the alias FQDN validation rule to `spec.private.alias` per D4, permitting the wildcard and glob forms Pangolin advertises.
- [x] 4.5 Run `make generate manifests`; commit the regenerated deepcopy, `deploy/crds/`, and `chart/templates/crd-pangolinendpoint.yaml`.

## 4b. Refuse an ambiguous identity

- [x] 4b.1 Add an ambiguity error to `internal/pangolin` (type plus an `Is...` helper, following `NotFoundError`), and return it from `GetSiteResourceByNiceID` when a listing holds more than one match.
- [x] 4b.2 Add reason `IdentityAmbiguous` to `api/v1alpha1`.
- [x] 4b.3 In `findSiteResource`, map the ambiguity to an `endpointIssue` on `Programmed` so it becomes a Warning event and a non-error requeue, and ensure no create or update follows (D2a).
- [x] 4b.4 Confirm a resolvable `.status.siteResourceId` still short-circuits recovery, so an unrelated collision on the site does not disturb an endpoint that knows its own resource.
- [x] 4b.5 Client test: two matches yield the ambiguity error, one match still resolves, zero matches still yield not-found.
- [x] 4b.6 Controller tests: an ambiguous identity produces the condition, the event, no create, no update, and no returned error; and the recorded-identifier path is unaffected by a collision.
- [x] 4b.7 Confirm the new tests fail against the first-match-wins behaviour.

## 5. Tests

- [x] 5.1 Re-pin the `httptest` fixtures in the site-resource client tests to the payloads captured in `design.md` — Context and task 1.4, including the nested `siteResources` envelope.
- [x] 5.2 Add a create-then-read round trip asserting the resource is readable by the same client that created it. This is the regression guard for the defect that motivated the change.
- [x] 5.3 Assert both range strings are present in the create and update bodies, and that a TCP-only endpoint sends a UDP range that exposes nothing.
- [x] 5.4 Assert a resource reporting `udpPortRangeString: "*"` against an endpoint declaring no UDP ports compares unequal and produces exactly one update, and that a second reconcile produces none.
- [x] 5.5 Assert recovery matches by `niceId` from a listing, and that a failing listing produces no create.
- [x] 5.6 Assert an explicitly declared `all: true` still serializes to `*`.
- [x] 5.7 Alias validation: single-label rejected, multi-label accepted, wildcard form accepted; exercised against a real API server rather than only as a unit test if one is available.
- [x] 5.8 Confirm `TestEndpoint_UnchangedReconcileIssuesNoUpdate` still passes — it is the existing guard for the update-loop class and must not regress.

## 6. Live verification

- [x] 6.1 Re-run the live test that found these defects: create an endpoint, confirm `Programmed=True`, then force a second reconcile and confirm it converges with no error and no update.
- [x] 6.2 Confirm a TCP-only endpoint reads back with no UDP exposure.
- [x] 6.3 Clear `.status.siteResourceId` on a live object and confirm recovery re-adopts the existing resource rather than creating a second one.
- [x] 6.5 Live: create a second private resource carrying the same `niceId` as a managed endpoint, clear `.status.siteResourceId`, and confirm the controller refuses with `IdentityAmbiguous` instead of adopting either. Remove the planted resource afterwards.
- [x] 6.4 Delete the test objects and confirm the Pangolin side is clean.

## 7. Record the spike findings

- [x] 7.1 Answer questions 1.1, 1.2, 1.4 and 1.7 in `add-private-endpoint-crd/tasks.md` from the captures in `design.md` — Context, marking which remain open (1.3 mesh reachability, 1.5 normalization, 1.6 duplicate alias, 1.8 multi-site, 1.9 principal lookup).
- [x] 7.2 Note in `add-private-endpoint-crd/design.md` that its OpenAPI-derived route assumptions were wrong, so the next change does not re-derive from the same document.
