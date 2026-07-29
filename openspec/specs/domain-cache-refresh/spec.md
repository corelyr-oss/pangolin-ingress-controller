# domain-cache-refresh Specification

## Purpose

Defines freshness semantics for the cached Pangolin domain list: when the controller refetches it, how refetches are rate-limited and deduplicated, and how a host that cannot be resolved to a domain is reported to operators.

A cached domain list can only ever be wrong by *omission* — a stale entry causes a spurious miss, never a spurious hit. This capability exists so that a domain registered in Pangolin after controller startup becomes resolvable without a restart, while keeping Pangolin API load bounded and keeping resolution failures diagnosable.

## Requirements
### Requirement: Domain list refresh on resolution miss

The controller SHALL refetch the Pangolin domain list when a host fails to match any cached domain, and SHALL retry the match against the refreshed list before reporting a resolution failure. Resolutions that match the cached list SHALL NOT trigger an API call.

#### Scenario: Host matches a cached domain

- **WHEN** an Ingress host suffix-matches a domain already in the cache
- **THEN** the host resolves to that domain's subdomain and domain ID
- **AND** no `ListDomains` call is made

#### Scenario: Domain registered after controller startup resolves without restart

- **GIVEN** the cache was populated before domain `mod.tf` existed in Pangolin
- **AND** the refresh interval has elapsed since the last refetch attempt
- **WHEN** an Ingress with host `mod.tf` is reconciled
- **THEN** the controller refetches the domain list
- **AND** the host resolves against the refreshed list
- **AND** no controller restart is required

#### Scenario: Host is genuinely not registered in Pangolin

- **GIVEN** the refresh interval has elapsed since the last refetch attempt
- **WHEN** an Ingress host matches no domain before or after the refetch
- **THEN** the controller reports a resolution failure for that host
- **AND** the refreshed list replaces the previous cache

#### Scenario: Refreshed list is sorted for longest-suffix matching

- **WHEN** the domain list is refetched
- **THEN** the stored list is ordered by base-domain length descending
- **AND** a host matching both `tunnel.tf` and `sub.tunnel.tf` resolves against `sub.tunnel.tf`

### Requirement: Refresh rate limiting

The controller SHALL refetch the domain list at most once per configured refresh interval, counted from the last refetch **attempt** regardless of whether that attempt succeeded. The rate limit SHALL apply process-wide rather than per host.

#### Scenario: Second miss within the interval does not refetch

- **GIVEN** a refetch attempt occurred less than one refresh interval ago
- **WHEN** another host fails to match the cached domains
- **THEN** no `ListDomains` call is made
- **AND** the resolution fails from the existing cache

#### Scenario: Many unresolvable hosts share one refetch

- **GIVEN** the refresh interval has elapsed
- **WHEN** ten Ingresses with ten distinct unmatched hosts are reconciled in sequence
- **THEN** at most one `ListDomains` call is made

#### Scenario: Failed refetch still consumes the rate limit

- **GIVEN** a refetch attempt failed with an API error
- **WHEN** another miss occurs before the refresh interval has elapsed
- **THEN** no further `ListDomains` call is made

#### Scenario: Refresh-on-miss disabled

- **GIVEN** the refresh interval is configured as `0`
- **WHEN** a host fails to match the cached domains
- **THEN** no `ListDomains` call is made
- **AND** the resolution fails from the existing cache

### Requirement: Concurrent refresh deduplication

Concurrent resolution misses SHALL collapse into a single in-flight domain list fetch. The controller SHALL NOT hold the cache lock across the network call.

#### Scenario: Parallel misses trigger one fetch

- **GIVEN** the refresh interval has elapsed
- **WHEN** multiple reconciles concurrently miss on the domain cache
- **THEN** exactly one `ListDomains` call is made
- **AND** every waiting reconcile observes the refreshed list

#### Scenario: Reads proceed during an in-flight fetch

- **WHEN** a refetch is in progress
- **THEN** a concurrent resolution for an already-cached host completes without waiting for the fetch

### Requirement: Cache retention on refresh failure

A failed refetch SHALL NOT discard or empty the existing cache. The error surfaced to the caller SHALL be the resolution failure for the requested host.

#### Scenario: Pangolin API unavailable during refetch

- **GIVEN** a populated domain cache
- **WHEN** a miss triggers a refetch and `ListDomains` returns an error
- **THEN** the previously cached domains are retained
- **AND** a subsequent resolution for a cached host still succeeds
- **AND** the refetch error is logged

#### Scenario: Cold cache fetch failure is reported

- **GIVEN** the cache has never been populated
- **WHEN** `ListDomains` returns an error
- **THEN** the reconcile fails with an error identifying the fetch failure

### Requirement: Operator-visible reporting of unresolvable hosts

When a host cannot be resolved after a refresh, the controller SHALL emit a Warning event on the Ingress and SHALL include the domain-list freshness in the failure message. The message SHALL NOT enumerate the full domain list.

#### Scenario: Warning event on unresolvable host

- **WHEN** an Ingress host cannot be resolved to a Pangolin domain
- **THEN** a Warning event is recorded on that Ingress naming the unresolved host
- **AND** the event is visible via `kubectl describe ingress`

#### Scenario: Failure message distinguishes stale cache from unregistered domain

- **WHEN** a resolution failure is reported
- **THEN** the message states the host, when the domain list was last refreshed, and how many domains are known
- **AND** the message does not list every known domain

#### Scenario: Full domain list available at debug level

- **WHEN** a refetch completes and log level is debug
- **THEN** the known domains are logged

### Requirement: Bounded retry cadence for unresolvable hosts

An unresolvable host SHALL be retried on a bounded, predictable schedule derived from the refresh interval, rather than controller-runtime's exponential error backoff. This condition SHALL NOT be reported as a reconcile error.

#### Scenario: Requeue at the refresh cadence

- **WHEN** an Ingress host cannot be resolved
- **THEN** the reconcile completes without returning an error
- **AND** the Ingress is requeued after approximately the refresh interval

#### Scenario: Retry delay does not grow with repeated failures

- **WHEN** the same Ingress fails to resolve across many consecutive reconciles
- **THEN** the delay between attempts stays approximately the refresh interval and does not grow unboundedly

#### Scenario: Other reconcile failures keep error semantics

- **WHEN** a reconcile fails for a reason other than an unresolvable host
- **THEN** the reconcile returns an error and retains exponential backoff

### Requirement: Configurable refresh interval

The refresh interval SHALL be configurable via a command-line flag with a default of 60 seconds, and SHALL be surfaced through both deployment artifacts.

#### Scenario: Default interval

- **WHEN** the controller starts without the flag
- **THEN** the refresh interval is 60 seconds

#### Scenario: Custom interval

- **WHEN** the controller starts with `--domain-cache-refresh-interval=5m`
- **THEN** refetches occur at most once per five minutes

#### Scenario: Configurable from both deployment artifacts

- **WHEN** the interval is set via the Helm chart value or the raw manifests
- **THEN** the rendered container arguments include the configured flag

