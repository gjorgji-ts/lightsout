# Contributing to LightsOut

Thanks for your interest. This guide covers a local development environment, the test suites, and how to open a pull request.

## Prerequisites

- Go 1.27 or later
- `kubectl` configured against a cluster, for manual testing
- [Kind](https://kind.sigs.k8s.io/), for the e2e tests
- Docker, for image builds and the e2e tests

`make` downloads every other tool into `./bin/`, including controller-gen, kustomize, golangci-lint and setup-envtest.

## Setting up

Clone the repository and check that it builds:

```bash
git clone https://github.com/gjorgji-ts/lightsout.git
cd lightsout
make build
```

## Running the tests

### Unit and integration

```bash
make test
```

This runs `go test` over every package except the e2e suite, against [envtest](https://book.kubebuilder.io/reference/envtest). It needs no cluster. The coverage report lands in `cover.out`.

### End to end

The e2e suite needs Kind. It creates a cluster named `lightsout-test-e2e` if one is not already there:

```bash
make test-e2e
```

The run tears the cluster down afterwards. To keep it for debugging, set `SKIP_CLEANUP=true`, or call the targets yourself:

```bash
make setup-test-e2e
KIND_CLUSTER=lightsout-test-e2e go test -tags=e2e ./test/e2e/ -v -ginkgo.v -timeout 20m
```

The operator integration cases are separate, because installing every operator at once exhausts most machines. For those, see [test/e2e/README.md](test/e2e/README.md).

## Linting

```bash
make lint
```

This runs `golangci-lint`, and checks that the Helm chart RBAC rules still match `config/rbac/role.yaml`. To apply the fixes it can make on its own:

```bash
make lint-fix
```

## Code generation

After you change the API types in `api/v1alpha1/`, regenerate the DeepCopy methods and the CRD manifests:

```bash
make manifests generate
```

Then sync the result into the Helm chart:

```bash
make helm-sync
```

> [!IMPORTANT]
> Always run `make helm-sync` after `make manifests`. The chart ships its own
> copy of the CRDs in `charts/lightsout/crds/`. If that copy goes stale,
> Kubernetes silently prunes any field that exists in the Go types but not in
> the installed CRD schema, and the failure is invisible until something does
> not work.

## Project layout

```text
api/v1alpha1/          # CRD types, defaulting and validation webhooks
cmd/                   # Operator entry point
config/                # Kustomize manifests: CRDs, RBAC, deployment
charts/lightsout/      # Helm chart. crds/ must match config/crd/bases/
internal/controller/   # Reconcilers, scaler, and the optional integrations
internal/constants/    # Annotation and label keys
internal/webhook/      # Webhook handlers
test/e2e/              # End-to-end suites, behind build tags
docs/                  # User-facing documentation
```

## Opening a pull request

1. Fork the repository and branch from `main`.
2. Make the change, and add or update the tests that cover it.
3. Run `make lint test` and check that both pass.
4. If you changed API types, run `make manifests generate helm-sync` and commit the generated files.
5. Open the pull request against `main`, fill in the template, and link any related issue.

A pull request needs one approving review from a maintainer, and green CI.

## Writing style

Documentation and code comments follow two rules: say what the code does, and stop. Prefer a short active sentence to a long one. Explain why a piece of code is shaped the way it is, rather than restating what the next line already says.

## Reporting issues

Use [GitHub Issues](https://github.com/gjorgji-ts/lightsout/issues) for bugs and feature requests. Search the existing issues first.

## License

Contributions are licensed under the [Apache License 2.0](LICENSE).
