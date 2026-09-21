## Why

When an Ingress host's create is refused with `409 Conflict`, the Ingress path adopts the existing Pangolin resource by listing the organisation's resources and matching `(subdomain, domainId)`. Probing the live test org (55 resources) shows two defects in that lookup:

1. **The listing is never paginated.** `Client.ListResources` reads one page, and Pangolin's default page size is 20. Any resource past the first page is invisible, and the adopt fails.
2. **The listing carries no `subdomain`.** `GET /org/{orgId}/resources` returns `fullDomain` and `domainId` but no `subdomain` field. `subdomain` only appears on `GET /resource/{id}`. Every listed resource therefore decodes with `Subdomain == ""`, and:
   - a host with a subdomain (`app.example.com`) **never** matches, so the adopt fails for every such host, on every org size;
   - an apex host (`example.com`, subdomain `""`) matches the **first resource on the same domain**, which may be `nav.example.com`. The Ingress then records and reprograms a resource it does not own.

The unit-test fake returned `subdomain` in its listing and never paginated, which is how both went unnoticed.

## What Changes

- `Client.ListResources` paginates to completion (`page`/`pageSize`; the endpoint rejects `limit`/`offset`), and either returns every resource or an error. The separate `ListAllResources` added for the public endpoint branch is folded into it.
- The adopt lookup matches on `(fullDomain, domainId)` instead of `(subdomain, domainId)`, restricted to HTTP resources. `fullDomain` is what the listing actually carries and is exactly the Ingress host.
- More than one match is refused with an error instead of taking the first.
- The Ingress test fake's listing mirrors the real one: no `subdomain` field, and paginated.

## Capabilities

### New Capabilities

- `ingress-resource-adoption`: how the Ingress path re-finds an existing Pangolin resource after a create conflict. It had no spec; this records the corrected behaviour.

## Impact

- **Code**: `internal/pangolin/resources.go`, `internal/pangolin/raw_resources.go`, `internal/controller/ingress_controller.go`, `internal/controller/pangolinendpoint_public.go` (rename only), `internal/controller/multi_route_test.go`.
- **Behaviour**: adoption starts working for subdomain hosts and large orgs, and apex hosts stop adopting a sibling resource. No API, CRD, flag or RBAC change.
- **Risk**: low. The only other caller of the listing is the public endpoint branch, which already used the paginated version.
