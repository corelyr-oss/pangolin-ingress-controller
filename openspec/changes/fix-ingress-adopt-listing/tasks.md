## 1. Client

- [x] 1.1 `ListResources` paginates to completion (fold in `ListAllResources`, remove that name)
- [x] 1.2 Update the public endpoint branch to call `ListResources`

## 2. Adopt path

- [x] 2.1 `findExistingResource(host, domainID)` matches `(fullDomain, domainId)` case-insensitively over HTTP resources
- [x] 2.2 More than one match returns an error naming the candidates

## 3. Tests

- [x] 3.1 Ingress fake: listing omits `subdomain` and paginates by `page`/`pageSize` with an optional cap
- [x] 3.2 Subdomain host adopted after 409
- [x] 3.3 Apex host adopts the apex resource, not a sibling
- [x] 3.4 Match beyond page 1 is adopted
- [x] 3.5 Ambiguous match is refused without writes
- [x] 3.6 Mutation check: the old `(subdomain, domainId)` match fails 3.2/3.3

## 4. Docs

- [x] 4.1 Remove the "found in passing" notes that point at this bug, and tick 5.4 in `add-public-raw-endpoint`
