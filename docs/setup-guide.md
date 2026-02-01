# Setup Guide

This guide covers two installation paths:

1. **Basic** — controller only, no webhooks
2. **With webhooks and cert-manager** — adds validation, defaulting, and overlap detection

## Prerequisites

- Kubernetes cluster (v1.28+)
- [Helm](https://helm.sh/) v3
- `kubectl` configured for your cluster

## Basic Install

This installs the LightsOut controller without admission webhooks. Schedules will not be validated on creation — the controller will still work, but invalid cron expressions or misconfigurations won't be caught until reconciliation.

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
- **Missing namespace selection** (no `namespaceSelector` or `namespaces`) is rejected
- **Invalid rate limit config** (batch size < 1, negative delays) is rejected
- **Overlapping schedules** produce a warning (not rejected, but you'll know)
- **Default timezone** is set to `UTC` if not specified

Without webhooks, these errors are only surfaced during reconciliation via status conditions.

## Uninstall

```bash
helm uninstall lightsout
```

Helm does not remove CRDs on uninstall. To fully clean up:

```bash
kubectl delete crd lightsoutschedules.lightsout.techsupport.mk
```

> **Warning**: Deleting the CRD removes all `LightsOutSchedule` resources. If the controller is still running, its finalizer will restore workloads before deletion. If the controller is already gone, workloads will remain in their current state.
