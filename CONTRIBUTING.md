# Contributing to LightsOut

Thank you for your interest in contributing! This guide covers everything you need to get a local development environment running, make changes, and open a pull request.

## Prerequisites

- Go 1.26+
- `kubectl` configured against a cluster (for manual testing)
- [Kind](https://kind.sigs.k8s.io/) (for e2e tests)
- Docker (for building images and e2e tests)

All other development tools (controller-gen, kustomize, golangci-lint, setup-envtest) are downloaded automatically into `./bin/` by `make`.

## Setting Up

Clone the repository and verify everything builds:

```bash
git clone https://github.com/gjorgji-ts/lightsout.git
cd lightsout
make build
```

## Running Tests

### Unit and integration tests

```bash
make test
```

This runs `go test` against all non-e2e packages using [envtest](https://book.kubebuilder.io/reference/envtest) (no real cluster required). A coverage report is written to `cover.out`.

### End-to-end tests

E2e tests require Kind. A cluster named `lightsout-test-e2e` is created automatically if it does not already exist:

```bash
make test-e2e
```

The cluster is torn down after the run. To keep it for debugging, set `SKIP_CLEANUP=true` or call the individual targets:

```bash
make setup-test-e2e
KIND_CLUSTER=lightsout-test-e2e go test -tags=e2e ./test/e2e/ -v -ginkgo.v -timeout 20m
```

## Linting

```bash
make lint
```

This runs `golangci-lint` and verifies that the Helm chart RBAC rules are in sync with `config/rbac/role.yaml`. Auto-fixable issues can be resolved with:

```bash
make lint-fix
```

## Code Generation

After modifying API types in `api/v1alpha1/`, regenerate DeepCopy methods and CRD manifests:

```bash
make manifests generate
```

Then sync the generated CRDs into the Helm chart and verify RBAC rules:

```bash
make helm-sync
```

> **Important:** Always run `make helm-sync` after `make manifests`. The Helm chart ships its own copy of the CRDs in `charts/lightsout/crds/`. If that copy is stale, Kubernetes will silently prune fields that exist in the Go types but not in the installed CRD schema.

## Project Layout

```
api/v1alpha1/          # CRD types and webhook logic
cmd/                   # Operator entry point
config/                # Kustomize manifests (CRDs, RBAC, deployment)
charts/lightsout/      # Helm chart (crds/ must stay in sync with config/crd/bases/)
internal/controller/   # Reconciler and ArgoCD integration
test/                  # Unit helpers and e2e suite
docs/                  # User-facing documentation
```

## Opening a Pull Request

1. Fork the repository and create a branch from `main`.
2. Make your changes, adding or updating tests as appropriate.
3. Run `make lint test` locally and ensure both pass.
4. If you changed API types, run `make manifests generate helm-sync` and commit the generated files.
5. Open a pull request against `main`. Fill in the PR template and link any related issues.

Pull requests require at least one approving review from a maintainer before merging. CI must be green.

## Reporting Issues

Use [GitHub Issues](https://github.com/gjorgji-ts/lightsout/issues) to report bugs or request features. Please search for existing issues before opening a new one.

## License

By contributing you agree that your contributions will be licensed under the [Apache License 2.0](LICENSE).
