## Security Model

### RBAC Permissions

The LightsOut controller requires **cluster-wide permissions** to modify workloads. This is by design, as the operator needs to scale workloads across namespaces based on configured schedules.

#### Required Permissions

| Resource | API Group | Verbs | Purpose |
|----------|-----------|-------|---------|
| Deployments | apps | get, list, watch, patch, update | Scale replicas to 0 during off-hours, restore during business hours |
| StatefulSets | apps | get, list, watch, patch, update | Scale replicas to 0 during off-hours, restore during business hours |
| CronJobs | batch | get, list, watch, patch, update | Suspend/unsuspend scheduled jobs |
| Namespaces | core | get, list, watch | Discover namespaces for namespace selectors |
| Events | core | create, patch | Record scaling events for observability |

#### Why Cluster-Wide Access?

LightsOut schedules can target workloads across multiple namespaces using label selectors and namespace selectors. To support this use case, the controller requires a `ClusterRole` rather than namespace-scoped `Roles`. This enables:

- A single schedule to manage workloads in `dev-*`, `staging-*`, or other namespace patterns
- Organization-wide cost savings policies
- Centralized schedule management

### Security Considerations

**Risks:**
- The controller has write access to all Deployments, StatefulSets, and CronJobs cluster-wide
- A misconfigured schedule could inadvertently scale down production workloads
- Compromised controller credentials could be used to disrupt services

**Mitigations:**
- Use namespace selectors and label selectors to precisely target workloads
- Exclude critical namespaces (e.g., `kube-system`, `monitoring`) from schedules
- Review `LightsOutSchedule` resources carefully before applying
- Use Kubernetes RBAC to restrict who can create/modify schedules (see `lightsoutschedule_editor_role.yaml`)
- Monitor controller logs and events for unexpected scaling operations
- Use the admission webhook (when enabled) to validate schedules before creation

**Best Practices:**
1. Start with a narrow scope (specific namespace, specific labels) before expanding
2. Use the `exclusions` field to protect critical workloads by date or permanently
3. Test schedules in non-production environments first
4. Set up alerts for scaling events in your monitoring stack
