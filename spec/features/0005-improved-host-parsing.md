# Feature: Improved Host Parsing for Complex Domain Structures

**Spec ID:** 0005
**Status:** Implemented
**Author:**
**Created:** 2026-03-13
**Priority:** High

## Summary

The `parseHost()` function naively splits hostnames on dots and assumes the last two labels form the domain (e.g., `example.com`). This breaks for country-code TLDs like `.co.uk`, `.com.au`, `.org.uk`, and many others, where `app.example.co.uk` would incorrectly parse as subdomain=`app.example`, domain=`co.uk`. This feature replaces the naive parser with a robust implementation using the Public Suffix List.

## Motivation

The existing code contains an acknowledged limitation (comment at line 339): _"For simplicity, assume host format is subdomain.domain.tld. In production, you'd want more sophisticated parsing."_

This breaks for any domain with a multi-label TLD:
- `app.example.co.uk` -> subdomain=`app.example`, domain=`co.uk` (wrong)
- `api.service.com.au` -> subdomain=`api.service`, domain=`com.au` (wrong)
- `blog.example.org.uk` -> subdomain=`blog.example`, domain=`org.uk` (wrong)

The correct parsing should be:
- `app.example.co.uk` -> subdomain=`app`, domain=`example.co.uk`
- `api.service.com.au` -> subdomain=`api`, domain=`service.com.au`

Additionally, the domain resolution (`resolveDomainID`) compares the parsed domain against `BaseDomain` from the Pangolin API. If parsing is wrong, the domain lookup fails silently (returns empty string), and resource creation fails.

## Detailed Design

### Overview

Replace the naive `parseHost()` with a PSL-aware parser, using domain data from the Pangolin API's own domain list as the primary matching source, with the Public Suffix List as a fallback.

### API / Configuration Changes

No new annotations or CLI flags.

### Implementation Details

**Approach: API-first domain matching (preferred)**

Rather than parsing the host independently and then matching against the API, reverse the logic: match the host against known Pangolin domains first.

1. **In `resolveDomainID()`**, which already fetches `ListDomains()`, iterate over all known `BaseDomain` values and find which one is a suffix of the Ingress host:
   ```go
   func (r *IngressReconciler) parseHostWithDomains(host string, domains []pangolin.Domain) (subdomain, domainID string, err error) {
       for _, d := range domains {
           if strings.HasSuffix(host, "."+d.BaseDomain) {
               subdomain := strings.TrimSuffix(host, "."+d.BaseDomain)
               return subdomain, d.ID, nil
           }
           if host == d.BaseDomain {
               return "", d.ID, nil
           }
       }
       return "", "", fmt.Errorf("no matching Pangolin domain found for host %q", host)
   }
   ```

2. **Merge `parseHost()` and `resolveDomainID()`** into a single function since they are always used together and the domain matching depends on API data.

3. **Sort domains by length (longest first)** to correctly handle nested domains (e.g., if both `example.com` and `co.uk` are registered, `app.example.co.uk` should match `example.co.uk` not `co.uk`).

4. **Fallback:** If no Pangolin domain matches (possible if the domain cache is stale), fall back to PSL-based parsing using `golang.org/x/net/publicsuffix`:
   ```go
   import "golang.org/x/net/publicsuffix"
   
   domain, err := publicsuffix.EffectiveTLDPlusOne(host)
   subdomain := strings.TrimSuffix(host, "."+domain)
   ```

### Error Handling

- If no Pangolin domain matches the host and PSL fallback also fails, emit a Warning event (see spec 0003) and return an error that causes requeue.
- If the host has no subdomain (e.g., bare domain `example.com`), return empty subdomain -- this is valid for Pangolin resources.

## Alternatives Considered

1. **Only use Public Suffix List:** Works for parsing but still requires a separate step to match against Pangolin domains. The API-first approach eliminates parsing ambiguity entirely.
2. **Configurable domain suffix:** A CLI flag like `--domain-suffix=example.com` would work for single-domain setups but not for orgs with multiple domains.
3. **Keep naive parsing, document the limitation:** Rejected because ccTLD support is a basic expectation.

## Testing Strategy

- Unit test: `parseHostWithDomains` with various domain structures:
  - `app.example.com` with domain `example.com` -> subdomain=`app`
  - `app.example.co.uk` with domain `example.co.uk` -> subdomain=`app`
  - `deep.sub.example.com` with domain `example.com` -> subdomain=`deep.sub`
  - `example.com` with domain `example.com` -> subdomain=``
  - Ambiguous: host matches multiple domains (longest wins)
- Unit test: Fallback to PSL when no Pangolin domain matches.
- Unit test: Error when host matches no known domain.

## Rollout Plan

- This changes internal parsing logic only; no user-facing API changes.
- The `golang.org/x/net` dependency may already be present transitively; if not, add it.
- No migration needed -- existing resources continue to work because their IDs are already stored in annotations.

## Open Questions

- Should the domain list be refreshed on cache invalidation (ties into spec 0004)?
- Is there a performance concern with iterating all domains on every reconciliation? (Likely negligible for typical org sizes.)
