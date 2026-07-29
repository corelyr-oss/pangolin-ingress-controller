# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Overview

Kubernetes Ingress Controller that reconciles `Ingress` resources with `ingressClassName: pangolin` into resources/targets in a Pangolin proxy backend (https://pangolin.net) via its REST API. Built on `sigs.k8s.io/controller-runtime`.

## Common commands

```bash
make build           # go build -> bin/manager (runs fmt + vet first)
make test            # go test ./... -coverprofile cover.out (runs fmt + vet first)
make run             # run controller from host against current kubeconfig
make fmt             # go fmt ./...
make vet             # go vet ./...
make docker-build IMG=...
make docker-build-multiarch IMG=...   # linux/amd64,linux/arm64 via buildx
make deploy / undeploy                # kubectl apply/delete -f deploy/
```

Run a single test:

```bash
go test ./internal/controller/ -run TestMatchHostToDomains -v
```

Local run requires Pangolin credentials — `--pangolin-org-id` and `--pangolin-site-nice-id` are **required** flags; the controller exits if either is empty (see `cmd/main.go`). API key is loaded from a Kubernetes Secret (default `pangolin-api-key` in `pangolin-system`), so `make run` needs that secret to exist in the targeted cluster.

## Architecture

### Reconciliation flow (`internal/controller/ingress_controller.go`)

`IngressReconciler.Reconcile` is the single entry point. The controller is registered with two predicates OR'd together so it reconciles on `GenerationChanged` **and** on changes to any `pangolin.ingress.k8s.io/*` annotation _except_ the controller-managed `resource-id` (otherwise the controller would re-trigger itself on every write). See `pangolinAnnotationChangedPredicate` and `SetupWithManager`.

Per Ingress:

1. **Lazy client init**: `PangolinClient` is created on first reconcile by reading the API key from the configured Secret. There is no per-Secret watch — restart the controller (or rotate the secret + pod) to pick up a new key.
2. **Finalizer**: Adds `pangolin.ingress.k8s.io/finalizer`. On deletion, calls `DeleteResource` (Pangolin auto-deletes child targets), then removes the finalizer.
3. **Host → (subdomain, domainID) resolution** (`resolveHostDomain`): API-first. Fetches and caches the Pangolin domain list (`internal/controller/domain_cache.go`), sorts by `BaseDomain` length descending so the longest suffix wins, then suffix-matches. Falls back to `publicsuffix.EffectiveTLDPlusOne` only if no Pangolin domain matches by suffix.

   The cache is **refreshed on miss**: a stale list can only ever cause a spurious miss (never a spurious hit), so a host that matches nothing triggers one refetch and a retry before the resolution is declared failed. This lets a domain registered in Pangolin *after* controller startup resolve without a restart. Refetches are rate-limited by `--domain-cache-refresh-interval` (default `60s`, `0` disables), counted from the last *attempt* rather than the last success, so neither many unresolvable Ingresses nor a Pangolin outage can amplify into sustained API load. A failed refetch never discards the existing cache. Cache hits cost no API calls, so steady-state traffic is one fetch per process.

   A host that still matches nothing after a refresh yields `errDomainNotFound`. `Reconcile` treats this as an expected, operator-fixable condition: it emits a Warning `DomainNotFound` event on the Ingress and requeues at ~the refresh interval **instead of returning an error**, so it does not ride exponential backoff and does not increment `controller_runtime_reconcile_errors_total`. Callers must wrap the sentinel with `%w` or that behavior silently reverts to a hard error.
4. **Resource create/update**: If the Ingress already has `pangolin.ingress.k8s.io/resource-id`, it `UpdateResource`s. Otherwise it `CreateResource`s and stores the new ID in the annotation. On `409 Conflict` during create, it _adopts_ the existing Pangolin resource by listing and matching `(subdomain, domainID)` — this is how the controller recovers when its annotation was lost but the Pangolin-side resource still exists.
5. **Target reconciliation**: Lists existing targets, finds one matching `(siteID, ip, port)`, and **updates** it in-place; otherwise creates a new one. Any other targets on the resource are deleted as stale. The target IP is always `<service>.<namespace>.svc.cluster.local`. `pathTypeToMatch` maps `Exact`→`exact`, `ImplementationSpecific`→`regex`, default→`prefix`.
6. **Status**: `updateIngressStatus` writes `status.loadBalancer.ingress[0]` using the cached Site's `proxyIp` if present, otherwise the first rule's `host` as `hostname` (so ArgoCD and similar tools see the Ingress as healthy). Site info is cached in `r.siteCache`, which **is** still restart-to-invalidate — unlike the domain cache, it has no refresh-on-miss path, so a changed `proxyIp` is only picked up on restart.

### Annotations

All controller-recognized annotations are constants at the top of `internal/controller/ingress_controller.go` (prefix `pangolin.ingress.k8s.io/`). Parsing helpers `parseBoolAnnotation` / `parseStringAnnotation` / `parseIntAnnotation` / `parseHeadersAnnotation` return pointer types so "unset" is distinguishable from "false"/"empty" and only set fields are sent to Pangolin.

**Health-check defaults**: When `healthcheck-enabled: "true"`, the controller auto-fills `hcPath=/`, `hcHostname=<targetIP>`, `hcPort=<servicePort>`, `hcInterval=30`, `hcMethod=GET` if not set by the user, because Pangolin requires all five non-null before pushing the config to Newt. See the block at lines ~497–517.

### Pangolin API client (`internal/pangolin/`)

- `client.go` — thin HTTP client. Bearer auth, 30s timeout. `ConflictError` + `IsConflict(err)` are the canonical way to detect 409s (used by the adopt-on-conflict path).
- `resources.go` — typed request/response structs and CRUD for `Resource`, `Target`, `Site`, `Domain`. Pangolin's API uses `PUT` for create and `POST` for update on resources/targets, scoped by `/v1/org/{orgID}/...`. JSON request fields use omitempty pointers so partial updates only touch the fields the user has annotated.

### Deployment artifacts

Two parallel ways to ship:

- `deploy/` — raw manifests (`kubectl apply -f deploy/`)
- `chart/` — Helm chart (image defaults to `ghcr.io/corelyr-oss/pangolin-ingress-controller`)

When changing required flags or RBAC, update **both** `deploy/deployment.yaml`/`deploy/clusterrole.yaml` and `chart/templates/deployment.yaml`/`chart/templates/clusterrole.yaml`. The kubebuilder RBAC markers above `IngressReconciler` (`//+kubebuilder:rbac:...`) document what the controller needs but do not auto-generate the YAML in this repo.

## Spec workflow

This repo uses two parallel workflows; check which one a task belongs to before authoring docs:

- `openspec/` — OpenSpec change proposals (active changes in `openspec/changes/`, archived in `openspec/changes/archive/`). Driven by the `openspec-*` / `opsx:*` skills.
- `spec/features/` and `spec/bugs/` — older spec docs, numbered `NNNN-short-description.md`.
