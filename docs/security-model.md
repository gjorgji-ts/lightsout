# Security model

## RBAC permissions

The controller needs cluster-wide permissions to modify workloads. That is deliberate. A schedule targets namespaces by label, and the matching set grows as new namespaces appear.

### Required permissions

| Resource | API group | Verbs | Purpose |
|----------|-----------|-------|---------|
| Deployments | apps | get, list, watch, patch, update | Scale replicas to zero and restore them |
| StatefulSets | apps | get, list, watch, patch, update | Scale replicas to zero and restore them |
| CronJobs | batch | get, list, watch, patch, update | Suspend and resume scheduled jobs |
| HorizontalPodAutoscalers | autoscaling | get, list, watch, update, patch | Disable HPA scale-up for the window, then restore it |
| Namespaces | core | get, list, watch | Resolve namespace selectors |
| Pods | core | get, list, watch | Check readiness during warmup, and find pods still terminating after a downscale |
| Events | core, events.k8s.io | create, patch | Record scaling events. controller-runtime uses `events.k8s.io` on current clusters. |
| LightsOutSchedules | lightsout.techsupport.mk | get, list, watch, create, update, patch, delete | Manage cluster-scoped schedules |
| LightsOutNamespaceSchedules | lightsout.techsupport.mk | get, list, watch, create, update, patch, delete | Manage namespace-scoped schedules, and let the cluster-wide controller apply precedence |
| Applications | argoproj.io | get, list, watch, update, patch | Label ArgoCD Applications. Optional, needs `rbac.argocd: true`. |
| Kustomizations | kustomize.toolkit.fluxcd.io | get, list, watch, update, patch | Suspend and resume Flux Kustomizations. Optional, needs `rbac.fluxcd: true`. |
| HelmReleases | helm.toolkit.fluxcd.io | get, list, watch, update, patch | Suspend and resume Flux HelmReleases. Optional, needs `rbac.fluxcd: true`. |

The ClusterRole also grants `update` on the `finalizers` subresource of both schedule types, and `get`, `patch` and `update` on their `status` subresource. Both are needed for the cleanup finalizer and the status writes.

Custom resource permissions are not in this table. The API groups are unknown until you declare them, so you list them yourself under `rbac.customResources`. For details, see [Custom resource integration](custom-resources.md).

### Why cluster-wide access

One schedule targets many namespaces through a label selector, so the controller holds a `ClusterRole` rather than a `Role` per namespace. That is what lets a single schedule cover every `dev-*` namespace, including ones created after the install.

`LightsOutNamespaceScheduleReconciler` runs inside the same process and inherits the same permissions. It narrows itself at runtime and acts only on the namespace that holds its `LightsOutNamespaceSchedule`.

### Developer access to namespace schedules

The operator `ClusterRole` covers the controller alone. For a developer to create a `LightsOutNamespaceSchedule`, grant a `Role` and `RoleBinding` in their namespace with `create`, `update` and `delete` on `lightsoutnamespaceschedules` in the `lightsout.techsupport.mk` API group. Most clusters grant `get`, `list` and `watch` to every namespace member as well.

## Risks

- The controller can write to every Deployment, StatefulSet and CronJob on the cluster.
- With the ArgoCD integration on, it can modify labels on any ArgoCD Application.
- With the FluxCD integration on, it can set `spec.suspend` on any Kustomization and HelmRelease.
- A schedule with too wide a selector can scale down production workloads.
- Stolen controller credentials can take services down on a schedule that looks legitimate.

## Mitigations

- Target workloads precisely, through namespace selectors and label selectors.
- Exclude the namespaces that must never stop, such as `kube-system` and your monitoring stack.
- Read a schedule before you apply it, and check which namespaces the selector resolves to.
- Restrict who can create a cluster-scoped schedule. See `config/rbac/lightsoutschedule_editor_role.yaml`.
- Control per namespace who can create a `LightsOutNamespaceSchedule`, through a `Role` and `RoleBinding`.
- Watch the controller logs and the Kubernetes events for scaling you did not expect.
- Run the admission webhooks, so an invalid schedule never reaches the cluster.

## Practices worth following

1. Start narrow, with one namespace and one label, then widen.
2. Protect critical workloads with `excludeLabels`, and whole namespaces with `excludeNamespaces`.
3. Run a schedule somewhere harmless first, through one full down-and-up cycle.
4. Alert on `lightsout_scaling_errors_total` and on `lightsout_stuck_terminating_pods`.
