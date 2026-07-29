## Context

`IngressReconciler` holds two process-lifetime caches: `domainCache` (the Pangolin domain list) and `siteCache`. Both are populated lazily on first use and never invalidated. For `domainCache` this turns a routine operator action — register a domain in Pangolin, then create the Ingress — into a permanent reconcile failure that only a pod restart clears.

Current flow (`internal/controller/ingress_controller.go:924-987`):

1. `resolveHostDomain(ctx, host)` calls `loadDomains(ctx)`.
2. `loadDomains` returns `r.domainCache` if non-nil; otherwise it fetches via `ListDomains`, sorts by `BaseDomain` length descending, and stores the result.
3. `resolveHostDomain` suffix-matches via `matchHostToDomains`, then falls back to `publicsuffix.EffectiveTLDPlusOne` + exact match.
4. On no match it returns a plain error, which propagates out of `Reconcile` and is retried by controller-runtime's exponential backoff — against the same stale cache, forever.

Constraints shaping the design:

- **No push channel.** Pangolin exposes no watch/webhook for domain registration, so freshness must be pull-based.
- **`ListDomains` is a full-list fetch** over a 30s-timeout HTTP client. It is not free and should not run on the hot path.
- **The cache is process-wide, not per-host.** One refetch serves every waiting Ingress, which makes global rate limiting both sufficient and cheap.
- **`PangolinClient` is a concrete `*pangolin.Client`** with no interface seam, so the cache is currently untestable without a live API.
- **Correct resolutions must stay allocation- and API-free.** The overwhelmingly common case is a hit; it must not regress.

## Goals / Non-Goals

**Goals:**

- A domain registered in Pangolin after controller startup resolves without operator intervention.
- Bounded, predictable Pangolin API load: an unresolvable host cannot amplify into sustained request volume regardless of how many Ingresses reference it.
- Bounded self-heal latency that an operator can reason about, rather than controller-runtime's opaque exponential backoff.
- Failure to resolve is visible via `kubectl describe ingress`, not only in controller logs.
- The cache becomes unit-testable without network access.

**Non-Goals:**

- Background/periodic polling of the domain list (see Decision 1).
- Fixing `siteCache`. Its staleness produces a wrong `proxyIp` in Ingress status — cosmetic and self-correcting on restart — not a hard failure. Same pattern, different urgency; separate change.
- Per-host or negative caching. The domain list is small and the cooldown already bounds load.
- Invalidating the cache on Pangolin *removals*. A removed domain yields a stale hit rather than a stale miss; it is a distinct failure mode and out of scope.

## Decisions

### 1. Refresh on miss with a cooldown, not TTL expiry or background polling

**Chosen:** refetch only when a resolution misses, and at most once per configurable interval (default 60s).

The bug is exclusively a *miss* problem: a stale cache is only ever wrong when it lacks an entry. Refresh-on-miss targets that exact condition and adds zero steady-state API traffic — a cluster whose domains all resolve makes precisely as many calls as today (one, at startup).

*Alternatives considered:*

- **TTL expiry (e.g. refetch when cache older than 5m).** Refetches on the hit path too, so every cluster pays continuous API cost for a condition that is rare and self-announcing. It also couples worst-case self-heal latency to the TTL, forcing a choice between "slow to heal" and "chatty".
- **Background goroutine polling on an interval.** Same steady-state cost as TTL, plus lifecycle complexity (start/stop with the manager, leader-election interaction) and it would refresh even when the controller is idle.
- **Refresh on every miss, no cooldown.** A single Ingress with a typo'd host, retried under backoff across many Ingresses, becomes an unbounded API amplifier. The cooldown is what makes refresh-on-miss safe.

TTL and refresh-on-miss are not mutually exclusive; a TTL can be layered later if a removal-staleness case appears, without revisiting this design.

### 2. Cooldown is global, not per-host

A single `domainsFetchedAt` timestamp gates all refetches. Because one fetch refreshes the entire list, ten unknown hosts do not warrant ten fetches — the first one already gave every host the freshest available answer. This caps refetch load at `1/interval` for the whole process no matter how many Ingresses are failing, which is the property that makes the feature safe to enable by default.

### 3. Double-checked locking with a dedicated fetch mutex

Two locks with distinct jobs:

- `domainMu sync.RWMutex` guards the cache slice and timestamp. Held only for the duration of a field read or write, never across I/O.
- `fetchMu sync.Mutex` serializes fetches.

Sequence on a miss: release the read lock → acquire `fetchMu` → **re-check the timestamp** under a read lock → if another goroutine refreshed while we waited, use its result and skip the fetch → otherwise `ListDomains` with no cache lock held → store under the write lock → release `fetchMu`.

The re-check is what makes this single-flight: N concurrent misses produce exactly one API call, and the N-1 waiters observe the fresh result.

*Alternatives considered:*

- **`golang.org/x/sync/singleflight`.** Purpose-built and slightly clearer, but adds a direct dependency for ~10 lines of standard double-checked locking. Rejected on dependency-minimalism grounds; it remains an easy swap if the locking becomes more complex.
- **Fetching while holding `domainMu.Lock()`.** Trivially correct and trivially wrong operationally: a write lock held across a 30s HTTP timeout stalls every concurrent reconcile in the process, converting one unreachable API into a full controller stall.

### 4. Never discard a good cache on a failed refetch

If the forced `ListDomains` returns an error, keep the existing cache and leave `domainsFetchedAt` **unadvanced**... with one caveat: not advancing the timestamp means the next miss retries immediately, re-exposing the stampede this design exists to prevent. So track the last *attempt* separately from the last *success*, and gate the cooldown on the attempt.

This keeps a Pangolin outage from escalating: hosts that resolve from cache keep resolving, and the failure is confined to hosts that were already unresolvable. The refetch error is logged but the returned error remains the resolution failure, which is what the operator needs to act on.

### 5. Treat "domain not found" as a requeue, not a hard error

Introduce a sentinel `errDomainNotFound`. `Reconcile` detects it with `errors.Is` and returns `ctrl.Result{RequeueAfter: refreshInterval}` with a nil error, after logging and emitting a Warning event.

Returning a hard error hands retry timing to controller-runtime's exponential backoff, which climbs toward ~16 minutes. Since the cooldown already prevents API hammering, backoff adds nothing but unpredictable and steadily worsening self-heal latency — the operator registers a domain and then waits an unknowable time. Requeueing at the refresh interval makes worst-case self-heal ≈ one interval and easy to state in docs.

It also stops a routine, expected, operator-fixable condition from being logged at `error` with a stack trace on every retry — the exact noise that made the original incident hard to read.

### 6. Extract the cache into its own type

Move the cache into a `domainCache` struct owning `lister`, `entries`, `fetchedAt`, `lastAttempt`, `interval`, `now func() time.Time`, and its mutexes. `IngressReconciler` holds a `*domainCache`.

`lister` is a narrow interface (`ListDomains(context.Context) ([]pangolin.Domain, error)`) satisfied by `*pangolin.Client`, which gives tests a seam without touching the client. The injectable `now` makes cooldown behavior testable without sleeps. This is what turns the caching logic from "requires a live Pangolin org" into ordinary table-driven unit tests.

### 7. Bounded error message

The message reports the refresh timestamp and the number of known domains — enough to distinguish "stale cache" from "not registered" — but not the full domain list. The list can be long, and the message reaches Kubernetes Events, which are broadly readable and not the right surface for the org's full domain inventory. The full list is logged at debug level instead.

## Risks / Trade-offs

- **Unresolvable host still fails, just faster and louder** → By design. The change removes the *stale-cache* failure, not the *genuinely-unregistered* one. The improved message and Warning event are what make the remaining case diagnosable in seconds instead of an hour.
- **Cooldown adds up to one interval of self-heal latency** → Bounded and documented (default 60s worst case). Operators needing instant resolution can still restart the pod; the flag allows tuning down at the cost of more API calls.
- **Sustained 1 req/min against Pangolin while any host is unresolvable** → Bounded and global (Decision 2), and only while a real misconfiguration exists. Setting the interval to `0` disables refresh-on-miss entirely for operators who want today's behavior.
- **Warning event on every requeue could spam the Event stream** → The `EventRecorder` aggregates repeated identical events into a single object with a count, and the requeue cadence is one per interval rather than per backoff tick.
- **Requeue instead of error changes controller metrics** → `controller_runtime_reconcile_errors_total` will no longer increment for this condition, which could silence an existing alert. Called out explicitly for the implementer; the Warning event is the replacement signal.
- **Double-checked locking is easy to get subtly wrong** → Confined to one small type with direct unit tests for the concurrent-miss case (N goroutines, assert exactly one `ListDomains` call against a counting fake).
- **`errors.Is` plumbing must survive wrapping** → `resolveHostDomain` and its callers must wrap with `%w`. A test asserting `errors.Is(err, errDomainNotFound)` through the full `Reconcile` path guards this, since a `%v` slip would silently restore hard-error behavior.

## Migration Plan

No data migration, no API change, no config change required — the new flag defaults to the desired behavior and existing deployments pick it up on image bump.

1. Merge and build; roll out via the normal image update in `deploy/` or the Helm chart.
2. On rollout the process starts with an empty cache and fetches fresh, so any currently-stuck Ingress resolves immediately on first reconcile regardless of this change; the fix proves itself on the *next* domain added after startup.
3. Verify: register a new domain in Pangolin, create an Ingress for it, and confirm it reconciles within one refresh interval with no restart.

**Rollback:** revert the image. The caches are in-memory only, so there is no persisted state to undo and a downgraded controller behaves exactly as before.

## Open Questions

- Is 60s the right default interval? It is a guess balancing self-heal latency against API politeness; a Pangolin rate-limit budget could justify raising it.
- Should `RequeueAfter` use the refresh interval directly, or a slightly longer independent value? Using the interval directly means a requeue can land marginally before the cooldown expires and skip its refetch, costing one extra cycle. Adding a small jitter or margin would avoid it. Left to the implementer to pick a small margin.
- Should the same treatment be applied to `siteCache` in this change rather than a follow-up? Argued as non-goal above, but the mechanism is now generic enough that reusing it is cheap.
