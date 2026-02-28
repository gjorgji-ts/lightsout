# Security Policy

## Reporting a Vulnerability

**Please do not file public GitHub issues for security vulnerabilities.**

Use GitHub's [private vulnerability reporting](https://github.com/gjorgji-ts/lightsout/security/advisories/new) to submit a report confidentially. The maintainers will acknowledge receipt within 5 business days and aim to release a fix within 30 days depending on severity.

Include as much detail as you can: affected versions, reproduction steps, and potential impact.

## Security Model

LightsOut requires cluster-wide RBAC permissions to scale workloads across namespaces. Before deploying, review [docs/security-model.md](docs/security-model.md) to understand the risks and recommended mitigations.

## Supported Versions

Only the latest release receives security fixes.
