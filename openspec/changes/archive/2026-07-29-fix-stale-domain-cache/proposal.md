## Why

`loadDomains` fetches the Pangolin domain list once per process and caches it forever (`r.domainCache`, `internal/controller/ingress_controller.go:963`). Any domain registered in Pangolin *after* the controller pod started is invisible to it, so every Ingress on that host fails to reconcile permanently — and the error blames the host rather than the cache:

```
no matching Pangolin domain found for host "mod.tf" (parsed domain: "mod.tf")
```

This is not hypothetical. On 2026-07-27 the `coder/coder` Ingress had been failing since 2026-07-15 with exactly this error, repeated 78 times. `mod.tf` was registered and verified in Pangolin the whole time (id `k6e2cywsow0eoj4`); the controller pod had simply started on 2026-07-03 and cached a domain list that predated it. The only remedy was a manual `kubectl rollout restart`, which requires an operator to first correctly guess that a message about an unmatched host actually means a stale cache.

The failure is silent (no Kubernetes Event, no status condition), self-perpetuating (controller-runtime's backoff retries forever against an unchanging cache), and hits precisely the common workflow of adding a domain to Pangolin and then creating the Ingress for it.

## What Changes

- **Refresh on miss**: when a host matches no cached domain, `resolveHostDomain` forces one refetch of the domain list and retries the match before returning an error. A domain added after startup now resolves on the next reconcile instead of never.
- **Cooldown to prevent API stampede**: forced refetches are rate-limited by a minimum interval (default 60s, configurable via a new `--domain-cache-refresh-interval` flag; `0` disables refresh-on-miss). Within the cooldown a miss fails from cache without calling the API, so a genuinely unknown host across many Ingresses cannot amplify into sustained API load.
- **Single-flight refetch**: concurrent misses across parallel reconciles collapse into one in-flight API call rather than one per reconcile.
- **Actionable error message**: on a miss after a refresh, the error states that the domain list was refreshed, when, and how many domains are known — so the message distinguishes "not registered in Pangolin" from "controller cache is stale".
- **Observability**: log at info when a refresh-on-miss occurs and when it changes the domain set; emit a Kubernetes Event on the Ingress when a host cannot be resolved, so the failure is visible via `kubectl describe` instead of only in controller logs.

Non-goals: periodic/background polling of the domain list (refresh-on-miss covers the failure precisely without steady-state API traffic — see design.md); a watch or webhook from Pangolin (no such API); and the parallel `siteCache` staleness issue, which has a different blast radius (a stale `proxyIp` in Ingress status, not a hard reconcile failure) and is tracked separately.

## Capabilities

### New Capabilities
- `domain-cache-refresh`: freshness semantics for the cached Pangolin domain list — when the controller refetches, how refetches are rate-limited and deduplicated, and how an unresolvable host is reported to operators.

### Modified Capabilities
<!-- none — openspec/specs/ contains no pre-existing specs -->

## Impact

- **Code**:
  - `internal/controller/ingress_controller.go`: `loadDomains` gains a force/refresh path plus `domainsFetchedAt` and single-flight state on `IngressReconciler`; `resolveHostDomain` retries after a forced refresh and produces the richer error; new field for the configured refresh interval.
  - `cmd/main.go`: new `--domain-cache-refresh-interval` flag (duration, default `60s`) wired into `IngressReconciler`.
  - RBAC: `deploy/clusterrole.yaml` and `chart/templates/clusterrole.yaml` already grant `events` `create;patch`, so no YAML change is needed; only the matching `//+kubebuilder:rbac:groups="",resources=events,verbs=create;patch` marker is missing above `IngressReconciler` and should be added for accuracy.
  - The reconciler has no `EventRecorder` today — one must be wired from the manager in `cmd/main.go`.
  - `deploy/deployment.yaml` and `chart/templates/deployment.yaml`: expose the new flag (chart value `domainCacheRefreshInterval`).
- **Behavior**: no breaking changes. Ingresses that resolve today continue to resolve with identical results and no extra API calls — the refetch only triggers on a path that currently returns a hard error. Steady-state Pangolin API load is unchanged.
- **Docs**: `CLAUDE.md` currently states "The cache is per-process and never invalidated — a controller restart is required to pick up newly added Pangolin domains" (Architecture §3); this becomes inaccurate and must be rewritten. `README.md` gains the new flag.
- **Tests**: `resolveHostDomain` and `loadDomains` need a fake/injectable Pangolin domain lister — the existing `TestMatchHostToDomains` covers only the pure matching helper and needs no change.
- **Dependencies**: none added (`sync` and `time` are already in use).
