## Why

Some workloads need a public port that Pangolin forwards as raw TCP or UDP, with no TLS termination and no HTTP semantics. The motivating case is a service that runs its own mutual TLS on dedicated ports: terminating TLS at the edge would break the client-certificate handshake, and an `Ingress` can only express HTTP. Pangolin supports this as a *raw* public resource (`mode: tcp|udp` + `proxyPort`), and the server-side switch (`flags.allow_raw_resources`) is already on for the instances this controller targets.

`v1alpha1` reserved `spec.public` for exactly this, and deferred it for one reason: public-resource create accepts no caller-supplied `niceId`, so a resource whose recorded ID was lost could only be re-found by matching its proxy port, which cannot be told apart from another owner holding that port. Probing the live test instance (see design, "Live findings") shows the gap can be closed without matching on the port: the raw-resource **update** accepts `niceId` and Pangolin enforces its uniqueness, and **create** accepts a caller-chosen `name`. Both carry the same deterministic value, so identity never depends on the port.

## What Changes

- Implement the `spec.public` branch of `PangolinEndpoint`: one raw public resource per object, with `protocol` (`TCP`|`UDP`, default `TCP`), `proxyPort` (the public edge port) and an optional `servicePort` (defaults to `proxyPort`).
- Replace the CEL rules "private is required" / "public is reserved" with "exactly one of `private` or `public`", and make the branch immutable (switching needs a delete and recreate — the two branches are different Pangolin object types).
- Identity: create the raw resource with `name` set to the deterministic identity (`<prefix>-<namespace>-<name>`, the same derivation as the private branch), record its ID in `.status.resourceId` immediately, then set the same value as `niceId` via update. Recovery uses the recorded ID, then the org resource listing: `niceId` match first, then `name` match (a resource whose naming update was interrupted). The point route `GET /org/{orgId}/resource/{niceId}` is documented but absent on the live server and is not used.
- Port exclusivity: Pangolin accepts a second raw resource on a `(protocol, proxyPort)` that is already taken. The controller checks the listing itself before create and before changing the port, and reports a taken port as `Programmed=False` / `ProxyPortInUse`. It never adopts on the port.
- Targets: one target per resolved site, pointing at `<service>.<namespace>.svc.cluster.local:<servicePort>`; missing targets are created, stale ones deleted.
- Extend `internal/pangolin/` with raw-resource create/update request types, `proxyPort`/`mode` on `Resource`, a fully paginated org resource listing, and `*NotFoundError` from `GetResource`.
- Docs: the CRD section of the README documents the branch and its host-side prerequisite (a Traefik TCP/UDP entrypoint and Gerbil port mapping per proxy port on the Pangolin host — the controller cannot create these).

**Non-goals:**

- Health checks, PROXY protocol, and sticky sessions on raw resources. The fields exist in Pangolin; they are not needed for the motivating case and can be added without a schema break.
- Access control. Pangolin applies no SSO/auth to raw L4 resources; the branch has no `access` block.
- Creating edge entrypoints on the Pangolin host.

## Capabilities

### Modified Capabilities

- `private-endpoint-crd`: the custom-resource requirement no longer rejects `spec.public`; new requirements cover the public raw branch, its identity model, and its targets.

## Impact

- **API**: `PublicEndpointSpec` gains fields; `PangolinEndpointStatus` gains `resourceId`. Existing private endpoints are unaffected — the new CEL rule admits every object the old rules admitted.
- **Code**: `api/v1alpha1/pangolinendpoint_types.go`, `internal/controller/pangolinendpoint_controller.go` (+ new `pangolinendpoint_public.go`), `internal/pangolin/resources.go`.
- **Deployment**: regenerated CRD in `deploy/crds/` and `chart/templates/`. No RBAC change.
- **Risk**: request shapes were verified against the live test instance (create, name, update, target, list, delete). The port pre-check is client-side and therefore racy against a concurrent writer outside this controller; see design.
- **Found in passing, out of scope**: `Client.ListResources`, used by the Ingress adopt-on-409 path, reads only the first page (Pangolin's default page size is 20), so adoption silently fails in any org with more than 20 resources. Needs its own change.
