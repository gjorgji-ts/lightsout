# Setup Guide

This guide covers two installation paths:

1. **Basic** - controller only, no webhooks
2. **With webhooks and cert-manager** - adds validation, defaulting, and overlap detection

## Prerequisites

- Kubernetes cluster (v1.28+)
- [Helm](https://helm.sh/) v3
- `kubectl` configured for your cluster
- A node autoscaler like [Karpenter](https://karpenter.sh/) or [Cluster Autoscaler](https://github.com/kubernetes/autoscaler/tree/master/cluster-autoscaler) (LightsOut scales workloads to zero, but you need a node autoscaler to deprovision the empty nodes and realize cost savings)

## Basic Install

This installs the LightsOut controller without admission webhooks. Schedules will not be validated on creation - the controller will still work, but invalid cron expressions or misconfigurations won't be caught until reconciliation.

### 1. Install

```bash
helm install lightsout oci://ghcr.io/gjorgji-ts/charts/lightsout \
  --set webhook.enabled=false \
  --set certManager.enabled=false
```

### 2. Verify

```bash
kubectl get pods -l app.kubernetes.io/name=lightsout
```

You should see the controller pod running:

```
NAME                        READY   STATUS    RESTARTS   AGE
lightsout-xxxxxxxxx-xxxxx   1/1     Running   0          30s
```

### 3. Create a Schedule

```bash
kubectl apply -f - <<EOF
apiVersion: lightsout.techsupport.mk/v1alpha1
kind: LightsOutSchedule
metadata:
  name: dev-weekday-hours
spec:
  upscale: "0 6 * * 1-5"
  downscale: "0 18 * * 1-5"
  timezone: "America/New_York"
  namespaceSelector:
    matchLabels:
      environment: dev
EOF
```

### 4. Check Status

```bash
kubectl get lightsoutschedules
```

```
NAME               STATE   UPSCALE       DOWNSCALE     SUSPENDED   AGE
dev-weekday-hours  Up      0 6 * * 1-5   0 18 * * 1-5  false       1m
```

## With Webhooks and cert-manager

This is the recommended production setup. Admission webhooks validate schedules on creation and update, catching errors before they're persisted. cert-manager handles TLS certificate provisioning for the webhook server.

### 1. Install cert-manager

If you don't already have cert-manager installed:

```bash
kubectl apply -f https://github.com/cert-manager/cert-manager/releases/latest/download/cert-manager.yaml
```

Wait for cert-manager pods to be ready:

```bash
kubectl wait --for=condition=Ready pods -l app.kubernetes.io/instance=cert-manager -n cert-manager --timeout=120s
```

See the [cert-manager documentation](https://cert-manager.io/docs/installation/) for alternative installation methods.

### 2. Install LightsOut

Webhooks and cert-manager integration are enabled by default:

```bash
helm install lightsout oci://ghcr.io/gjorgji-ts/charts/lightsout
```

### 3. Verify

Check the controller is running:

```bash
kubectl get pods -l app.kubernetes.io/name=lightsout
```

Check the webhook is registered:

```bash
kubectl get validatingwebhookconfigurations | grep lightsout
kubectl get mutatingwebhookconfigurations | grep lightsout
```

Check the certificate was issued:

```bash
kubectl get certificates -l app.kubernetes.io/name=lightsout
```

### What Webhooks Provide

With webhooks enabled, schedules are validated before being persisted:

- **Invalid cron expressions** are rejected immediately
- **Invalid timezones** are rejected
- **Missing namespace selection** on `LightsOutSchedule` (no `namespaceSelector` or `namespaces`) is rejected
- **Invalid rate limit config** (batch size < 1, negative delays) is rejected
- **Invalid ArgoCD namespace** (not a valid DNS label) is rejected
- **Overlapping schedules** produce a warning (not rejected, but you'll know)
- **Global schedule targeting a namespace** that already has a `LightsOutNamespaceSchedule` produces a warning
- **Default timezone** is set to `UTC` if not specified

Without webhooks, these errors are only surfaced during reconciliation via status conditions.

## Namespace-Scoped Schedules

Developers can define their own scaling schedules directly in their namespace without requiring cluster-level access. When a `LightsOutNamespaceSchedule` exists in a namespace, any `LightsOutSchedule` targeting that namespace is automatically skipped for that namespace.

```bash
kubectl apply -f - <<EOF
apiVersion: lightsout.techsupport.mk/v1alpha1
kind: LightsOutNamespaceSchedule
metadata:
  name: team-hours
  namespace: team-a
spec:
  upscale: "0 8 * * 1-5"
  downscale: "0 20 * * 1-5"
  timezone: "Europe/Berlin"
EOF
```

Check status the same way as a global schedule:

```bash
kubectl get lightsoutnamespaceschedules -n team-a
```

To allow developers to create these resources in their namespace, provision a `Role` and `RoleBinding` granting `create`, `update`, `delete` on `lightsoutnamespaceschedules` (API group `lightsout.techsupport.mk`). `get`, `list`, `watch` are typically granted to all namespace members for observability.

To disable the namespace schedule controller entirely, set `--set namespaceSchedules.enabled=false` during Helm install/upgrade. The CRD is still installed; only the controller registration and RBAC rules are skipped.

## Uninstall

```bash
helm uninstall lightsout
```

Helm does not remove CRDs on uninstall. To fully clean up:

```bash
kubectl delete crd lightsoutschedules.lightsout.techsupport.mk
kubectl delete crd lightsoutnamespaceschedules.lightsout.techsupport.mk
```

> **Warning**: Deleting the CRDs removes all corresponding schedule resources. If the controller is still running, its finalizer will restore workloads before deletion. If the controller is already gone, workloads will remain in their current state.
