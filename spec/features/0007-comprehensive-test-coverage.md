# Feature: Comprehensive Test Coverage and CI Pipeline

**Spec ID:** 0007
**Status:** Draft
**Author:**
**Created:** 2026-03-13
**Priority:** Critical

## Summary

The project has a single test file (`ingress_controller_test.go`) with 2 test functions covering only `isManaged()` and a basic reconcile smoke test. The Pangolin API client package has zero tests. The CI pipeline (`build-docker.yml`) builds Docker images but never runs `go test`. This feature establishes comprehensive test coverage, a mockable API client interface, and a CI pipeline that gates merges on passing tests.

## Motivation

- **Test coverage is near-zero:** Only `isManaged()` is meaningfully tested. No tests exist for `processIngressRules`, `createOrUpdatePangolinResource`, `deletePangolinResources`, `parseHost`, `pathTypeToMatch`, annotation parsing, domain resolution, site caching, stale target cleanup, or the entire Pangolin API client package.
- **CI doesn't run tests:** The existing test file is never executed in CI. Regressions can be merged without detection.
- **No API client interface:** The Pangolin client is a concrete struct, making it impossible to mock in controller tests. The reconcile test creates a real reconciler but can't test Pangolin interactions.
- **Pure functions are untested:** `parseHost`, `pathTypeToMatch`, `parseBoolAnnotation`, `parseIntAnnotation`, `parseHeadersAnnotation` are all pure functions that are trivially testable.

## Detailed Design

### Overview

1. Extract a Pangolin client interface for mockability.
2. Write unit tests for all pure functions and key controller logic.
3. Add a CI workflow step that runs tests and blocks on failure.

### API / Configuration Changes

No user-facing changes. Internal refactoring only.

### Implementation Details

#### Phase 1: API Client Interface

1. **Define an interface** in `internal/pangolin/`:
   ```go
   type PangolinClient interface {
       CreateResource(ctx context.Context, req CreateResourceRequest) (*Resource, error)
       GetResource(ctx context.Context, resourceID string) (*Resource, error)
       ListResources(ctx context.Context) ([]Resource, error)
       UpdateResource(ctx context.Context, resourceID string, req UpdateResourceRequest) error
       DeleteResource(ctx context.Context, resourceID string) error
       CreateTarget(ctx context.Context, resourceID string, req CreateTargetRequest) (*Target, error)
       ListTargets(ctx context.Context, resourceID string) ([]Target, error)
       UpdateTarget(ctx context.Context, targetID int, req CreateTargetRequest) error
       DeleteTarget(ctx context.Context, targetID int) error
       GetSiteByNiceID(ctx context.Context, niceID string) (*Site, error)
       ListDomains(ctx context.Context) ([]Domain, error)
   }
   ```

2. **Update `IngressReconciler`** to depend on the interface rather than the concrete `*Client`.

3. **Create `internal/pangolin/mock/`** with a test mock implementing `PangolinClient` using configurable function fields (or use `go generate` with mockgen).

#### Phase 2: Unit Tests

| Test File | Tests | Coverage Target |
|---|---|---|
| `internal/controller/parse_test.go` | `parseHost`, `pathTypeToMatch`, `parseBoolAnnotation`, `parseStringAnnotation`, `parseIntAnnotation`, `parseHeadersAnnotation` | 100% of pure functions |
| `internal/controller/reconcile_test.go` | `processIngressRules` (multi-rule, single-rule, empty host), `createOrUpdatePangolinResource` (create path, update path, conflict/adoption), `deletePangolinResources`, `updateIngressStatus` | Core reconciliation logic |
| `internal/controller/predicate_test.go` | `pangolinAnnotationChangedPredicate` (annotation added, removed, changed, resource-id ignored) | Event filtering |
| `internal/pangolin/client_test.go` | HTTP request construction, response parsing, `checkResponse` error types, `decodeData` | API client package |

#### Phase 3: CI Pipeline

Add to `.github/workflows/build-docker.yml` (or create a new `ci.yml`):

```yaml
jobs:
  test:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
      - uses: actions/setup-go@v5
        with:
          go-version-file: 'go.mod'
      - run: go vet ./...
      - run: go test -race -coverprofile=coverage.out ./...
      - name: Check coverage
        run: go tool cover -func=coverage.out
  
  lint:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
      - uses: golangci/golangci-lint-action@v4

  build:
    needs: [test, lint]
    # ... existing Docker build steps
```

#### Phase 4: Makefile Improvements

```makefile
lint:
	golangci-lint run ./...

test-unit:
	go test -race -short ./...

test-integration:
	go test -race -run Integration ./...

coverage:
	go test -race -coverprofile=cover.out ./...
	go tool cover -html=cover.out -o cover.html
```

### Error Handling

Not applicable -- this is a testing and infrastructure feature.

## Alternatives Considered

1. **Integration tests with envtest:** controller-runtime's envtest provides a real API server for testing. Should be added but as a follow-up -- unit tests with mocks are the priority.
2. **Testcontainers for Pangolin API:** Spin up a real Pangolin instance in tests. Too complex for initial coverage; mock-based tests are sufficient.
3. **Table-driven tests only:** The existing tests use table-driven style. Continue this convention but also add focused single-case tests for complex scenarios.

## Testing Strategy

This feature IS the testing strategy. Success criteria:
- All pure functions have 100% line coverage.
- Core reconciliation paths (create, update, delete, conflict) have test coverage.
- CI pipeline runs tests on every PR and push to main.
- CI blocks merges if tests fail.

## Rollout Plan

1. Phase 1 (interface extraction) can be merged independently.
2. Phase 2 (tests) can be merged incrementally per test file.
3. Phase 3 (CI) should be merged as soon as any tests exist.
4. Target: >80% line coverage for `internal/controller/` and `internal/pangolin/`.

## Open Questions

- Should we add a minimum coverage threshold that blocks CI?
- Which mock generation tool to use: `mockgen`, `moq`, or hand-written mocks?
- Should `golangci-lint` config (`.golangci.yml`) be added with specific linters enabled?
