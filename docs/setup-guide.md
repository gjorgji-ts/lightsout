# Setup guide

This guide covers two installation paths:

1. **Basic** - the controller alone, without webhooks
2. **With webhooks and cert-manager** - adds validation, defaulting and overlap detection

## Prerequisites

- A Kubernetes cluster, v1.28 or later
- [Helm](https://helm.sh/) v3
- `kubectl` configured for the cluster
- A node autoscaler, such as [Karpenter](https://karpenter.sh/) or [Cluster Autoscaler](https://github.com/kubernetes/autoscaler/tree/master/cluster-autoscaler)

The autoscaler is not optional if you want the saving. LightsOut empties the workloads, and the autoscaler removes the nodes that billing follows.

## Basic install

This installs the controller without admission webhooks. Nothing validates a schedule as you create it. The controller still runs, but an invalid cron expression surfaces on the status conditions at the first reconcile rather than at `kubectl apply`.

### 1. Install

```bash
helm install lightsout oci://ghcr.io/gjorgji-ts/charts/lightsout \
  --set webhook.enabled=false \
  --set certManager.enabled=false
```

### 2. Check the controller

```bash
kubectl get pods -l app.kubernetes.io/name=lightsout
```

The controller pod runs:

```text
NAME                        READY   STATUS    RESTARTS   AGE
lightsout-xxxxxxxxx-xxxxx   1/1     Running   0          30s
```

### 3. Create a schedule

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

### 4. Read the state

```bash
kubectl get lightsoutschedules
```

```text
NAME               STATE   UPSCALE       DOWNSCALE     SUSPENDED   AGE
dev-weekday-hours  Up      0 6 * * 1-5   0 18 * * 1-5  false       1m
```

## Install with webhooks and cert-manager

This is the recommended setup. The admission webhooks validate a schedule on create and on update, so an error surfaces at `kubectl apply` rather than at the first reconcile. cert-manager issues the TLS certificate the webhook server needs.

### 1. Install cert-manager

Skip this step if cert-manager already runs on the cluster.

```bash
kubectl apply -f https://github.com/cert-manager/cert-manager/releases/latest/download/cert-manager.yaml
```

Wait for it to come up:

```bash
kubectl wait --for=condition=Ready pods -l app.kubernetes.io/instance=cert-manager -n cert-manager --timeout=120s
```

For other installation methods, see the [cert-manager documentation](https://cert-manager.io/docs/installation/).

### 2. Install LightsOut

The chart enables webhooks and the cert-manager integration by default:

```bash
helm install lightsout oci://ghcr.io/gjorgji-ts/charts/lightsout
```

### 3. Check the install

Check that the controller runs:

```bash
kubectl get pods -l app.kubernetes.io/name=lightsout
```

Check that the webhooks are registered:

```bash
kubectl get validatingwebhookconfigurations | grep lightsout
kubectl get mutatingwebhookconfigurations | grep lightsout
```

Check that cert-manager issued the certificate:

```bash
kubectl get certificates -l app.kubernetes.io/name=lightsout
```

### What the webhooks catch

The webhooks reject these outright:

- An invalid cron expression
- An invalid timezone
- A `LightsOutSchedule` with neither `namespaceSelector` nor `namespaces`
- A rate limit with a batch size below 1, or a negative delay
- An ArgoCD namespace that is not a valid DNS label
- An `argoCD.warmupTimeout` of zero or less

These produce a warning and still apply:

- Two schedules whose windows overlap
- A cluster-wide schedule that targets a namespace already holding a `LightsOutNamespaceSchedule`

The mutating webhook also defaults `timezone` to `UTC` when you omit it.

Without the webhooks, each of these surfaces on the status conditions at the first reconcile instead.

## Namespace-scoped schedules

A developer can set their own hours without cluster-level access. While a `LightsOutNamespaceSchedule` exists in a namespace, every `LightsOutSchedule` skips that namespace.

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

Check the state the same way:

```bash
kubectl get lightsoutnamespaceschedules -n team-a
```

To let developers create these, grant a `Role` and `RoleBinding` with `create`, `update` and `delete` on `lightsoutnamespaceschedules` in the `lightsout.techsupport.mk` API group. Most clusters grant `get`, `list` and `watch` to every namespace member as well.

## Optional integrations

### ArgoCD

`spec.argoCD` needs permission to label ArgoCD Application CRDs, which you opt in to:

```bash
helm upgrade lightsout oci://ghcr.io/gjorgji-ts/charts/lightsout \
  --set rbac.argocd=true
```

The labels are one half of the work. ArgoCD also needs `ignoreDifferences` entries, or `selfHeal` reverts the downscale. For those, see [ArgoCD integration](argocd.md).

### FluxCD

`spec.fluxCD` needs permission to suspend Kustomization and HelmRelease resources:

```bash
helm upgrade lightsout oci://ghcr.io/gjorgji-ts/charts/lightsout \
  --set rbac.fluxcd=true
```

For details, see [FluxCD integration](fluxcd.md).

## Disabling namespace schedules

To turn the namespace schedule controller off, install or upgrade with `--set namespaceSchedules.enabled=false`. The chart still installs the CRD. It skips the controller registration and the RBAC rules.

## Uninstall

> [!IMPORTANT]
> Delete your schedules **before** you uninstall. Each one carries a
> `lightsout.techsupport.mk/cleanup` finalizer that only the controller can
> clear. If you uninstall first, the delete never finishes, because the resource
> waits for a finalizer that nothing is left to process.

```bash
kubectl delete lightsoutschedules --all
kubectl delete lightsoutnamespaceschedules --all -A
helm uninstall lightsout
```

Deleting a schedule while the controller runs restores its workloads first.

Helm leaves the CRDs behind. To remove them:

```bash
kubectl delete crd lightsoutschedules.lightsout.techsupport.mk
kubectl delete crd lightsoutnamespaceschedules.lightsout.techsupport.mk
```

> [!WARNING]
> Deleting the CRDs deletes every schedule resource with them. If a schedule
> still exists and the controller is already gone, `kubectl delete crd` hangs on
> the finalizer. Clear it by hand:
>
> ```bash
> kubectl patch lightsoutschedule <name> --type merge -p '{"metadata":{"finalizers":null}}'
> ```
>
> That skips the restore, so those workloads stay scaled down.
