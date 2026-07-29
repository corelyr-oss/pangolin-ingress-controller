## 1. Extract the domain cache into a testable type

- [x] 1.1 Add `internal/controller/domain_cache.go` with a `domainLister` interface exposing `ListDomains(context.Context) ([]pangolin.Domain, error)`, verified to be satisfied by `*pangolin.Client`
- [x] 1.2 Define a `domainCache` struct holding `lister`, `entries`, `fetchedAt`, `lastAttempt`, `interval`, `now func() time.Time`, `domainMu sync.RWMutex`, and `fetchMu sync.Mutex`
- [x] 1.3 Add a constructor defaulting `now` to `time.Now`, and move the existing length-descending sort into the cache's store path
- [x] 1.4 Implement `get(ctx)` returning the cached entries, fetching once if the cache is cold
- [x] 1.5 Implement `refreshIfStale(ctx)` using double-checked locking: acquire `fetchMu`, re-read `lastAttempt` under `domainMu.RLock`, skip if another goroutine already refreshed, otherwise fetch with no cache lock held and store under `domainMu.Lock`
- [x] 1.6 Ensure `lastAttempt` advances on every attempt (success or failure) while `fetchedAt` and `entries` advance only on success, so a failed refetch never empties the cache
- [x] 1.7 Make `interval == 0` skip refresh entirely

## 2. Wire the cache into the reconciler

- [x] 2.1 Replace the `domainMu` / `domainCache` fields on `IngressReconciler` with a single `domains *domainCache`, and add a `DomainCacheRefreshInterval time.Duration` field
- [x] 2.2 Delete the old `loadDomains` method and repoint its callers at the new type
- [x] 2.3 Add sentinel `errDomainNotFound` and have `resolveHostDomain` wrap it with `%w` on both the PSL-fallback failure path and the final no-match path
- [x] 2.4 In `resolveHostDomain`, on a miss call `refreshIfStale` and retry both the suffix match and the PSL exact-match against the refreshed list before returning the sentinel
- [x] 2.5 Build the failure message from the host, `fetchedAt`, and the domain count — no full domain list
- [x] 2.6 Log the full domain list at debug level after a successful refetch
- [x] 2.7 Construct the cache in the reconciler's lazy client init alongside `PangolinClient`, so it picks up the same authenticated client

## 3. Requeue instead of hard-erroring on unresolvable hosts

- [x] 3.1 Add an `EventRecorder` field to `IngressReconciler` and populate it from `mgr.GetEventRecorderFor(...)` in `SetupWithManager` or `cmd/main.go`
- [x] 3.2 In `Reconcile`, detect `errors.Is(err, errDomainNotFound)` from the rule-processing call at `ingress_controller.go:372` and return `ctrl.Result{RequeueAfter: ...}` with a nil error
- [x] 3.3 Use a requeue delay slightly longer than the refresh interval so a requeue never lands just before the cooldown expires (see design Open Questions)
- [x] 3.4 Emit a Warning event on the Ingress naming the unresolved host; log at info, not error, with no stack trace
- [x] 3.5 Confirm all other reconcile failure paths still return errors and keep exponential backoff
- [x] 3.6 Add the `//+kubebuilder:rbac:groups="",resources=events,verbs=create;patch` marker above `IngressReconciler` (the ClusterRole YAML already grants this — verify, do not duplicate)

## 4. Configuration and deployment artifacts

- [x] 4.1 Add `--domain-cache-refresh-interval` (duration, default `60s`) in `cmd/main.go` and pass it into the reconciler
- [x] 4.2 Add the flag to `deploy/deployment.yaml`
- [x] 4.3 Add a `domainCacheRefreshInterval` value to `chart/values.yaml` and render the flag in `chart/templates/deployment.yaml`
- [x] 4.4 Bump the chart `appVersion`/`version` per repo convention

## 5. Tests

- [x] 5.1 Add a counting fake `domainLister` supporting canned results, injected errors, and call counting
- [x] 5.2 Test: cached hit resolves with zero `ListDomains` calls
- [x] 5.3 Test: miss after interval elapsed refetches and resolves the newly added domain
- [x] 5.4 Test: miss within the interval makes no call and fails from cache
- [x] 5.5 Test: ten distinct unmatched hosts after one interval produce at most one call
- [x] 5.6 Test: failed refetch retains prior entries, and a cached host still resolves afterwards
- [x] 5.7 Test: failed refetch still consumes the rate limit (no immediate retry)
- [x] 5.8 Test: `interval == 0` disables refresh-on-miss
- [x] 5.9 Test: N concurrent misses produce exactly one `ListDomains` call (run under `-race`)
- [x] 5.10 Test: `errors.Is(err, errDomainNotFound)` holds through the full `Reconcile` path, guarding against a `%v` wrapping slip
- [x] 5.11 Test: refreshed entries are stored sorted longest-first
- [x] 5.12 Confirm `TestMatchHostToDomains` still passes unchanged
- [x] 5.13 Run `make test` and `go test ./... -race`

## 6. Documentation

- [x] 6.1 Rewrite the `CLAUDE.md` Architecture §3 sentence claiming the cache "is never invalidated — a controller restart is required", describing refresh-on-miss and the cooldown instead
- [x] 6.2 Note in `CLAUDE.md` that `siteCache` still has restart-only semantics, so the remaining caveat is not lost
- [x] 6.3 Document the new flag and its default in `README.md`
- [x] 6.4 Note in the release/changelog entry that `controller_runtime_reconcile_errors_total` no longer increments for unresolvable hosts, and that the Warning event is the replacement signal for any alert relying on it

## 7. Verification

- [x] 7.1 Run `make build` and `make fmt vet`
- [ ] 7.2 Manually verify against a live cluster: register a new domain in Pangolin *after* the controller is running, create an Ingress for it, and confirm it reconciles within one interval with no restart
- [ ] 7.3 Verify `kubectl describe ingress` shows the Warning event for a deliberately unregistered host
- [x] 7.4 Confirm steady-state Pangolin API call volume is unchanged when all hosts resolve
