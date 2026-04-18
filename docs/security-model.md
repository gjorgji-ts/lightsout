## Security Model

### RBAC Permissions

The LightsOut controller requires **cluster-wide permissions** to modify workloads. This is by design, as the operator needs to scale workloads across namespaces based on configured schedules.

#### Required Permissions

| Resource | API Group | Verbs | Purpose |
|----------|-----------|-------|---------|
| Deployments | apps | get, list, watch, patch, update | Scale replicas to 0 during off-hours, restore during business hours |
| StatefulSets | apps | get, list, watch, patch, update | Scale replicas to 0 during off-hours, restore during business hours |
| CronJobs | batch | get, list, watch, patch, update | Suspend/unsuspend scheduled jobs |
| HorizontalPodAutoscalers | autoscaling | get, list, watch, update, patch | Disable scale-up during downscale to prevent fight-back, restore on upscale |
| Namespaces | core | get, list, watch | Discover namespaces for namespace selectors |
| Events | core, events.k8s.io | create, patch | Record scaling events for observability (controller-runtime uses the `events.k8s.io` API group on modern clusters) |
| LightsOutSchedules | lightsout.techsupport.mk | get, list, watch, create, update, patch, delete | Manage cluster-scoped schedules |
| LightsOutNamespaceSchedules | lightsout.techsupport.mk | get, list, watch, create, update, patch, delete | Manage namespace-scoped schedules; global controller lists these to implement precedence |
| Applications | argoproj.io | get, list, watch, update, patch | Label ArgoCD Application CRDs during scaling (optional, requires `rbac.argocd: true`) |
| Kustomizations | kustomize.toolkit.fluxcd.io | get, list, watch, update, patch | Suspend/resume FluxCD Kustomization resources during scaling (optional, requires `rbac.fluxcd: true`) |
| HelmReleases | helm.toolkit.fluxcd.io | get, list, watch, update, patch | Suspend/resume FluxCD HelmRelease resources during scaling (optional, requires `rbac.fluxcd: true`) |

#### Why Cluster-Wide Access?

LightsOut schedules can target workloads across multiple namespaces using label selectors and namespace selectors. To support this use case, the controller requires a `ClusterRole` rather than namespace-scoped `Roles`. This enables:

- A single schedule to manage workloads in `dev-*`, `staging-*`, or other namespace patterns
- Organization-wide cost savings policies
- Centralized schedule management

The `LightsOutNamespaceScheduleReconciler` also runs with these cluster-wide permissions (it is part of the same operator process), but it constrains itself at runtime to only act on workloads in the namespace where the `LightsOutNamespaceSchedule` resource lives.

#### Developer access for namespace-scoped schedules

The operator's `ClusterRole` covers what the controller itself needs. For developers to create `LightsOutNamespaceSchedule` resources in their own namespaces, a separate `Role` and `RoleBinding` granting `create`, `update`, `delete` on `lightsoutnamespaceschedules` (in the `lightsout.techsupport.mk` API group) must be provisioned in each namespace. The `get`, `list`, `watch` verbs are typically granted to any namespace member for observability.

### Security Considerations

**Risks:**
- The controller has write access to all Deployments, StatefulSets, and CronJobs cluster-wide
- When ArgoCD integration is enabled, the controller can modify labels on ArgoCD Application CRDs
- When FluxCD integration is enabled, the controller can set `spec.suspend` on FluxCD Kustomization and HelmRelease resources cluster-wide
- A misconfigured schedule could inadvertently scale down production workloads
- Compromised controller credentials could be used to disrupt services

**Mitigations:**
- Use namespace selectors and label selectors to precisely target workloads
- Exclude critical namespaces (e.g., `kube-system`, `monitoring`) from schedules
- Review `LightsOutSchedule` and `LightsOutNamespaceSchedule` resources carefully before applying
- Use Kubernetes RBAC to restrict who can create/modify cluster-scoped schedules (see `lightsoutschedule_editor_role.yaml`)
- Use namespace-scoped `Role`/`RoleBinding` to control which developers can create `LightsOutNamespaceSchedule` in each namespace
- Monitor controller logs and events for unexpected scaling operations
- Use the admission webhooks (when enabled) to validate schedules before creation

**Best Practices:**
1. Start with a narrow scope (specific namespace, specific labels) before expanding
2. Use `excludeLabels` to protect critical workloads and `excludeNamespaces` to protect entire namespaces
3. Test schedules in non-production environments first
4. Set up alerts for scaling events in your monitoring stack
