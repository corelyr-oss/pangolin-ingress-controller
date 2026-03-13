# Feature: API Client Resilience

**Spec ID:** 0004
**Status:** Draft
**Author:**
**Created:** 2026-03-13
**Priority:** High

## Summary

The Pangolin API client has no retry logic, no rate limiting, and caches (domain map, site info) that are never invalidated. Transient API failures cause immediate reconciliation errors that rely entirely on controller-runtime's exponential backoff for recovery. This feature adds client-level retries with backoff, configurable rate limiting, and TTL-based cache invalidation.

## Motivation

In production environments, transient network failures, API rate limits (429), and server errors (5xx) are expected. The current client treats all non-2xx responses (except 409) as terminal errors, forcing the controller to requeue the entire reconciliation. This is wasteful because:

1. **No retries:** A single dropped packet causes a full reconciliation retry including re-fetching the Ingress, re-resolving the domain, etc.
2. **No rate limiting:** A burst of Ingress changes can overwhelm the Pangolin API with sequential calls (domain resolve + create/update resource + get site + list targets + create/update target per rule per path).
3. **Stale caches:** The `domainMap` and `siteCache` are populated once and never refreshed. If a domain is added to Pangolin or a site's `ProxyIP` changes, the controller must be restarted.
4. **No Not Found typing:** The API client has `IsConflict()` but no `IsNotFound()`. A 404 from `GetResource` becomes a generic error string, making it impossible to distinguish "resource was externally deleted" from other failures.

## Detailed Design

### Overview

Enhance the `internal/pangolin/` client package with retry logic, rate limiting, typed error responses, and cache TTLs.

### API / Configuration Changes

New CLI flags:

| Flag | Default | Description |
|---|---|---|
| `--api-max-retries` | `3` | Maximum retry attempts for transient failures |
| `--api-retry-base-delay` | `1s` | Base delay for exponential backoff |
| `--api-rate-limit` | `10` | Maximum API requests per second |
| `--cache-ttl` | `5m` | TTL for domain and site caches |

Corresponding Helm values under `pangolin.api`:
```yaml
pangolin:
  api:
    maxRetries: 3
    retryBaseDelay: "1s"
    rateLimit: 10
    cacheTTL: "5m"
```

### Implementation Details

1. **Typed errors in `client.go`:**
   ```go
   type NotFoundError struct{ Body string }
   func IsNotFound(err error) bool { ... }
   
   type RateLimitError struct{ RetryAfter time.Duration }
   func IsRateLimited(err error) bool { ... }
   ```
   
   Update `checkResponse` to return typed errors for 404 and 429 status codes.

2. **Retry wrapper:**
   ```go
   func (c *Client) doWithRetry(ctx context.Context, req *http.Request) (*http.Response, error)
   ```
   - Retries on: 429 (rate limited), 500, 502, 503, 504, network timeouts.
   - Does NOT retry on: 400, 401, 403, 404, 409 (client errors are not transient).
   - Exponential backoff: `baseDelay * 2^attempt` with jitter.
   - Respects `Retry-After` header from 429 responses.
   - Respects context cancellation.

3. **Rate limiter:**
   Use `golang.org/x/time/rate.Limiter` to throttle outgoing requests. Apply `limiter.Wait(ctx)` before each request in `doWithRetry`.

4. **Cache invalidation:**
   ```go
   type cachedValue[T any] struct {
       value     T
       fetchedAt time.Time
   }
   ```
   - `domainMap` and `siteCache` entries expire after `cacheTTL`.
   - On cache miss or expiration, re-fetch from the API.
   - Cache is still protected by `sync.RWMutex` for concurrent access.

5. **Controller integration:**
   - Use `IsNotFound()` in `createOrUpdatePangolinResource` to detect externally deleted resources and recreate them instead of failing.
   - Remove the hard-coded 30s HTTP client timeout; make it configurable.

### Error Handling

- Retries are exhausted: Return the last error, allowing controller-runtime to handle requeue.
- Rate limiter blocks: `limiter.Wait(ctx)` respects context deadline, so it will fail gracefully if the reconciliation times out.
- Cache refresh failure: Return the stale value and log a warning, rather than failing the reconciliation.

## Alternatives Considered

1. **Rely entirely on controller-runtime requeue:** Simpler but inefficient. Each requeue re-fetches the Ingress and re-runs the entire reconcile loop, even if only one API call failed transiently.
2. **Circuit breaker pattern:** More sophisticated but overkill for a single upstream API. Could be added later if needed.
3. **Invalidate caches on every reconciliation:** Too aggressive. TTL-based provides a good balance.

## Testing Strategy

- Unit test: `doWithRetry` with mock HTTP responses (429, 500, timeout sequences).
- Unit test: Rate limiter blocks when limit is exceeded.
- Unit test: Cache expiration triggers re-fetch.
- Unit test: `IsNotFound` and `IsRateLimited` error type assertions.
- Unit test: Retry respects `Retry-After` header.
- Unit test: Retries stop on non-retriable errors (400, 401).

## Rollout Plan

- Backwards compatible: default values match current behavior (except caches now expire).
- The `x/time/rate` dependency needs to be added via `go get golang.org/x/time`.
- Document new CLI flags and Helm values.

## Open Questions

- Should the rate limiter be per-endpoint or global?
- Should cache TTL be configurable per cache (domain vs site) or a single global TTL?
- Should retry metrics be exposed via Prometheus (retry count, rate limit waits)?
