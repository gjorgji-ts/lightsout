#!/usr/bin/env -S uv run
# /// script
# requires-python = ">=3.11"
# dependencies = ["pyyaml"]
# ///
"""
Verify that the Helm chart ClusterRole rules
(charts/lightsout/templates/clusterrole.yaml) are in sync with the
kubebuilder-generated RBAC (config/rbac/role.yaml).

apiGroups listed in CONDITIONAL_GROUPS are excluded from the comparison
because they are gated behind optional Helm values and intentionally
maintained by hand.

Exit 0 if in sync, 1 if drift detected, 2 on an internal error.
"""
import re
import sys
import yaml

GENERATED_ROLE = "config/rbac/role.yaml"
HELM_CLUSTERROLE = "charts/lightsout/templates/clusterrole.yaml"

# These groups are conditionally included in the Helm chart (behind a Helm
# value flag) and are excluded from the automated drift check.
CONDITIONAL_GROUPS = {"argoproj.io"}


def load_generated_rules():
    with open(GENERATED_ROLE) as f:
        return yaml.safe_load(f).get("rules", [])


def load_helm_rules():
    with open(HELM_CLUSTERROLE) as f:
        raw = f.read()
    # Strip Helm template directives ({{ ... }}); leaves empty strings/lines
    # which are valid YAML and ignored by the parser.
    clean = re.sub(r"\{\{[^}]*\}\}", "", raw)
    docs = [d for d in yaml.safe_load_all(clean) if d]
    role = next((d for d in docs if d.get("kind") == "ClusterRole"), None)
    if role is None:
        print("ERROR: ClusterRole not found in Helm template", file=sys.stderr)
        sys.exit(2)
    return role.get("rules", [])


def normalize(rules):
    """Return a set of (apiGroup, resource, frozenset(verbs)) tuples."""
    result = set()
    for rule in rules:
        for group in rule.get("apiGroups", [""]):
            if group in CONDITIONAL_GROUPS:
                continue
            for resource in rule.get("resources", []):
                result.add((group, resource, frozenset(rule.get("verbs", []))))
    return result


def main():
    gen = normalize(load_generated_rules())
    chart = normalize(load_helm_rules())

    missing = gen - chart
    extra = chart - gen

    if not missing and not extra:
        print("OK: Helm ClusterRole RBAC rules are in sync with config/rbac/role.yaml")
        sys.exit(0)

    if missing:
        print("FAIL: rules in config/rbac/role.yaml missing from Helm ClusterRole:")
        for group, resource, verbs in sorted(missing):
            print(f"  apiGroups: [{group!r}]  resources: [{resource!r}]  verbs: {sorted(verbs)}")
        print(f"  → Add the missing rules to {HELM_CLUSTERROLE}")

    if extra:
        print("FAIL: rules in Helm ClusterRole absent from config/rbac/role.yaml:")
        for group, resource, verbs in sorted(extra):
            print(f"  apiGroups: [{group!r}]  resources: [{resource!r}]  verbs: {sorted(verbs)}")
        print(f"  → Remove extra rules from {HELM_CLUSTERROLE} or add +kubebuilder:rbac markers")

    sys.exit(1)


if __name__ == "__main__":
    main()
