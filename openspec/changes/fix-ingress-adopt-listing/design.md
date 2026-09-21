## Context

`findExistingResource` runs only on the create-conflict path, i.e. when an Ingress host has no recorded resource ID but Pangolin already holds a resource for the host. It was written against the resource *object* shape (`GET /resource/{id}`), which has `subdomain`. The *listing* shape does not.

Live listing entry keys (tunnel.tf, 2026-09-21): `domainId, enabled, fullDomain, headerAuthId, health, labels, mode, name, niceId, passwordId, pincodeId, proxyPort, resourceId, sites, ssl, sso, targets, whitelist, wildcard`.

The same resource read both ways: listing `{subdomain: null, domainId: "pfvsw…", fullDomain: "nav.vandra.boats"}`, GET `{subdomain: "nav", domainId: "pfvsw…", fullDomain: "nav.vandra.boats"}`.

## Decisions

### Match on `(fullDomain, domainId)`

`fullDomain` is present in the listing and equals the Ingress host by construction: the controller splits the host into `(subdomain, domainId)` and Pangolin joins them back. Keeping `domainId` in the key guards against two Pangolin domains producing the same string, e.g. an overlapping base domain. The comparison is case-insensitive, since DNS names are.

**Alternative: list, then `GET` each candidate on the domain for its `subdomain`.** Rejected. It costs N extra calls to reconstruct a value the listing already gives in joined form.

### HTTP resources only

A raw TCP/UDP resource has a null `fullDomain`, so it cannot match anyway. The filter (`proxyPort == 0`, mode not `tcp`/`udp`) still states the intent and protects against a server that someday fills `fullDomain` for them.

### Refuse ambiguity

Pangolin should hold at most one resource per full domain, which is why create returned 409 in the first place. If the listing nevertheless shows two, picking one would reprogram a resource that may belong to someone else. The lookup returns an error naming the candidates, and the reconcile fails visibly and retries. This is the same stance the `PangolinEndpoint` identity lookups take.

### One paginated listing

`ListResources` becomes the paginated implementation added in `add-public-raw-endpoint` as `ListAllResources`, and that name is removed. A single-page variant has no remaining caller, and keeping one invites the same bug back.

## Risks / Trade-offs

- **Wildcard resources.** A wildcard resource's `fullDomain` (`*.example.com`) never equals a concrete host, so it is never adopted. That is correct: an Ingress host is not the wildcard resource.
- **Cost.** A listing is now several requests in a large org, but it runs only on the create-conflict path.
