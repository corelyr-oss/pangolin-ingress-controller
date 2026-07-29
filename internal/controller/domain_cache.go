package controller

import (
	"context"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/vinzenz/pangolin-ingress-controller/internal/pangolin"
)

// defaultDomainCacheRefreshInterval bounds how often the domain list may be
// refetched, and therefore also bounds how long a newly registered Pangolin
// domain can stay unresolvable.
const defaultDomainCacheRefreshInterval = 60 * time.Second

// domainLister is the narrow slice of the Pangolin API the domain cache needs.
// It exists so the caching logic can be unit-tested without a live Pangolin org.
type domainLister interface {
	ListDomains(ctx context.Context) ([]pangolin.Domain, error)
}

// *pangolin.Client must satisfy domainLister.
var _ domainLister = (*pangolin.Client)(nil)

// domainCache caches the Pangolin domain list for the lifetime of the process.
//
// The list is fetched lazily on first use and refetched only when a host fails
// to match, which is the sole case in which a stale cache can be wrong: a stale
// entry can only ever cause a spurious miss, never a spurious hit. Resolutions
// that hit the cache therefore cost no API calls at all.
//
// Refetches are rate-limited by interval, counted from the last *attempt*
// rather than the last success. A miss that is retried across many Ingresses
// (or under controller-runtime's backoff) consequently cannot amplify into
// sustained Pangolin API load, and neither can a Pangolin outage.
type domainCache struct {
	lister   domainLister
	interval time.Duration
	now      func() time.Time

	// domainMu guards the fields below. It is held only for field access,
	// never across the network call.
	domainMu    sync.RWMutex
	entries     []pangolin.Domain
	fetchedAt   time.Time
	lastAttempt time.Time

	// fetchMu serializes fetches so that concurrent misses collapse into a
	// single in-flight request.
	fetchMu sync.Mutex
}

// newDomainCache builds a cache over lister. An interval of zero or less
// disables refresh-on-miss, restoring fetch-once-per-process behaviour.
func newDomainCache(lister domainLister, interval time.Duration) *domainCache {
	return &domainCache{
		lister:   lister,
		interval: interval,
		now:      time.Now,
	}
}

// get returns the cached domain list, fetching it if the cache is cold.
func (c *domainCache) get(ctx context.Context) ([]pangolin.Domain, error) {
	c.domainMu.RLock()
	entries := c.entries
	c.domainMu.RUnlock()
	if entries != nil {
		return entries, nil
	}

	c.fetchMu.Lock()
	defer c.fetchMu.Unlock()

	// Another goroutine may have populated the cache while we waited for
	// fetchMu; reuse its result rather than issuing a second request.
	c.domainMu.RLock()
	entries = c.entries
	c.domainMu.RUnlock()
	if entries != nil {
		return entries, nil
	}

	return c.fetchLocked(ctx)
}

// refreshIfStale refetches the domain list when the cooldown has elapsed,
// reporting the refreshed entries and whether a refresh took place. It is the
// recovery path for a domain registered in Pangolin after this process started.
func (c *domainCache) refreshIfStale(ctx context.Context) (entries []pangolin.Domain, refreshed bool, err error) {
	if c.interval <= 0 {
		return nil, false, nil
	}
	if !c.stale() {
		return nil, false, nil
	}

	c.fetchMu.Lock()
	defer c.fetchMu.Unlock()

	// Re-check under fetchMu: if another goroutine refreshed while we waited,
	// its result is exactly as fresh as ours would have been.
	if !c.stale() {
		c.domainMu.RLock()
		entries = c.entries
		c.domainMu.RUnlock()
		return entries, true, nil
	}

	entries, err = c.fetchLocked(ctx)
	if err != nil {
		return nil, false, err
	}
	return entries, true, nil
}

// fetchLocked performs the API call and stores the result. It must be called
// with fetchMu held.
//
// lastAttempt advances before the call and regardless of its outcome, so a
// failing Pangolin API is retried on the cooldown rather than on every
// reconcile. entries and fetchedAt advance only on success, so a failed refresh
// never discards a good cache.
func (c *domainCache) fetchLocked(ctx context.Context) ([]pangolin.Domain, error) {
	c.domainMu.Lock()
	c.lastAttempt = c.now()
	c.domainMu.Unlock()

	domains, err := c.lister.ListDomains(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to list Pangolin domains: %w", err)
	}

	// Sort by BaseDomain length descending so that suffix matching prefers the
	// longest (most specific) match.
	sort.Slice(domains, func(i, j int) bool {
		return len(domains[i].BaseDomain) > len(domains[j].BaseDomain)
	})

	c.domainMu.Lock()
	c.entries = domains
	c.fetchedAt = c.now()
	c.domainMu.Unlock()

	return domains, nil
}

// stale reports whether the cooldown since the last fetch attempt has elapsed.
func (c *domainCache) stale() bool {
	c.domainMu.RLock()
	defer c.domainMu.RUnlock()

	if c.lastAttempt.IsZero() {
		return true
	}
	return c.now().Sub(c.lastAttempt) >= c.interval
}

// describe reports cache freshness for diagnostics. It deliberately returns a
// count rather than the domains themselves: this text reaches Kubernetes
// Events, which are not the right surface for the org's full domain inventory.
func (c *domainCache) describe() (count int, lastRefresh string) {
	c.domainMu.RLock()
	defer c.domainMu.RUnlock()

	if c.fetchedAt.IsZero() {
		return len(c.entries), "never"
	}
	return len(c.entries), c.now().Sub(c.fetchedAt).Truncate(time.Second).String() + " ago"
}

// baseDomains extracts the base domains for debug logging.
func baseDomains(domains []pangolin.Domain) []string {
	out := make([]string, 0, len(domains))
	for _, d := range domains {
		out = append(out, d.BaseDomain)
	}
	return out
}
