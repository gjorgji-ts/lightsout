# Security policy

## Reporting a vulnerability

**Do not open a public GitHub issue for a security vulnerability.**

Use GitHub [private vulnerability reporting](https://github.com/gjorgji-ts/lightsout/security/advisories/new) to send the report confidentially. The maintainers acknowledge a report within 5 business days, and aim to ship a fix within 30 days, depending on severity.

Include the affected versions, the steps to reproduce, and the impact you see.

## Security model

LightsOut holds cluster-wide RBAC permissions, because it scales workloads across namespaces. Read [docs/security-model.md](docs/security-model.md) before you deploy it. That document lists what the controller can reach, and how to narrow it.

## Supported versions

Only the latest release receives security fixes.
