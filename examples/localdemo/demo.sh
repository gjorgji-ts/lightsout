#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
CLUSTER_NAME="lightsout-demo"
CERT_MANAGER_VERSION="v1.19.1"
SCRIPT_DIR_ROOT="$(cd "$SCRIPT_DIR/../.." && pwd)"
LIGHTSOUT_CHART_OCI="oci://ghcr.io/gjorgji-ts/charts/lightsout"
LIGHTSOUT_CHART_LOCAL="$SCRIPT_DIR_ROOT/charts/lightsout"
LIGHTSOUT_CHART="${LIGHTSOUT_CHART:-}"
GRAFANA_PASSWORD="$(openssl rand -base64 12)"

# Colors
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
CYAN='\033[0;36m'
NC='\033[0m'

log()  { echo -e "${GREEN}[+]${NC} $1"; }
warn() { echo -e "${YELLOW}[!]${NC} $1"; }
err()  { echo -e "${RED}[x]${NC} $1"; }

check_prerequisites() {
    local missing=()
    for cmd in kind kubectl helm; do
        if ! command -v "$cmd" &>/dev/null; then
            missing+=("$cmd")
        fi
    done
    if [[ ${#missing[@]} -gt 0 ]]; then
        err "Missing required tools: ${missing[*]}"
        exit 1
    fi
}

cmd_up() {
    check_prerequisites

    # Create Kind cluster
    if kind get clusters 2>/dev/null | grep -q "^${CLUSTER_NAME}$"; then
        warn "Kind cluster '${CLUSTER_NAME}' already exists, skipping creation."
    else
        log "Creating Kind cluster '${CLUSTER_NAME}'..."
        kind create cluster --name "$CLUSTER_NAME" --config "$SCRIPT_DIR/kind-config.yaml"
    fi

    # Install cert-manager
    log "Installing cert-manager ${CERT_MANAGER_VERSION}..."
    kubectl apply -f "https://github.com/cert-manager/cert-manager/releases/download/${CERT_MANAGER_VERSION}/cert-manager.yaml"
    log "Waiting for cert-manager webhook..."
    kubectl wait deployment.apps/cert-manager-webhook \
        --for=condition=Available \
        --namespace cert-manager \
        --timeout=120s

    # Install kube-prometheus-stack
    log "Installing kube-prometheus-stack..."
    helm repo add prometheus-community https://prometheus-community.github.io/helm-charts 2>/dev/null || true
    helm repo update prometheus-community
    kubectl create namespace monitoring --dry-run=client -o yaml | kubectl apply -f -
    helm upgrade --install kube-prometheus-stack prometheus-community/kube-prometheus-stack \
        --namespace monitoring \
        --values "$SCRIPT_DIR/values/prometheus-stack.yaml" \
        --set grafana.adminPassword="$GRAFANA_PASSWORD" \
        --wait --timeout 5m

    # Deploy Grafana dashboards
    log "Deploying Grafana dashboards..."
    kubectl apply -f "$SCRIPT_DIR/manifests/grafana-dashboards.yaml"

    # Resolve LightsOut Helm chart (OCI registry → local fallback)
    if [[ -n "$LIGHTSOUT_CHART" ]]; then
        log "Using chart from LIGHTSOUT_CHART=$LIGHTSOUT_CHART"
    elif helm show chart "$LIGHTSOUT_CHART_OCI" &>/dev/null; then
        LIGHTSOUT_CHART="$LIGHTSOUT_CHART_OCI"
        log "Using chart from OCI registry: $LIGHTSOUT_CHART"
    else
        LIGHTSOUT_CHART="$LIGHTSOUT_CHART_LOCAL"
        warn "OCI chart not available, using local chart: $LIGHTSOUT_CHART"
    fi

    # Prepare LightsOut operator image (registry → local build fallback)
    local img="ghcr.io/gjorgji-ts/lightsout:latest"
    if docker pull "$img" 2>/dev/null; then
        log "Pulled image from registry: $img"
    else
        warn "Remote image not available, building locally..."
        make -C "$SCRIPT_DIR_ROOT" docker-build IMG="$img"
    fi
    log "Loading image into Kind cluster..."
    kind load docker-image "$img" --name "$CLUSTER_NAME"

    # Install LightsOut operator
    log "Installing LightsOut operator..."
    kubectl create namespace lightsout-system --dry-run=client -o yaml | kubectl apply -f -
    helm upgrade --install lightsout "$LIGHTSOUT_CHART" \
        --namespace lightsout-system \
        --values "$SCRIPT_DIR/values/lightsout.yaml" \
        --wait --timeout 5m

    # Deploy demo apps
    log "Deploying demo namespaces and apps..."
    kubectl apply -f "$SCRIPT_DIR/manifests/namespaces.yaml"
    kubectl apply -f "$SCRIPT_DIR/manifests/apps-frontend.yaml"
    kubectl apply -f "$SCRIPT_DIR/manifests/apps-backend.yaml"

    log "Waiting for demo apps to be ready..."
    kubectl wait deployment --all \
        --for=condition=Available \
        --namespace team-frontend \
        --timeout=120s
    kubectl wait deployment --all \
        --for=condition=Available \
        --namespace team-backend \
        --timeout=120s

    # Wait for LightsOutSchedule CRD and webhook to be ready
    log "Waiting for LightsOutSchedule CRD..."
    kubectl wait crd/lightsoutschedules.lightsout.techsupport.mk --for=condition=Established --timeout=60s
    log "Waiting for LightsOut operator to be ready..."
    kubectl wait deployment --all \
        --for=condition=Available \
        --namespace lightsout-system \
        --timeout=120s
    log "Waiting for webhook to become responsive..."
    until kubectl apply --dry-run=server -f "$SCRIPT_DIR/manifests/schedules.yaml" &>/dev/null; do
        sleep 2
    done

    # Deploy schedule
    log "Deploying demo schedule..."
    kubectl apply -f "$SCRIPT_DIR/manifests/schedules.yaml"

    echo ""
    echo -e "${CYAN}========================================${NC}"
    echo -e "${CYAN}  LightsOut Demo Environment Ready${NC}"
    echo -e "${CYAN}========================================${NC}"
    echo ""
    echo -e "  Grafana:   ${GREEN}http://localhost:30080${NC}"
    echo -e "  Username:  ${GREEN}admin${NC}"
    echo -e "  Password:  ${GREEN}${GRAFANA_PASSWORD}${NC}"
    echo ""
    echo -e "  The demo schedule cycles every ~3 minutes."
    echo -e "  Open the ${CYAN}LightsOut - Overview${NC} dashboard in Grafana to watch."
    echo ""
    echo -e "  Run ${YELLOW}./demo.sh status${NC} to check workload state."
    echo -e "  Run ${YELLOW}./demo.sh down${NC} to tear everything down."
    echo ""
}

cmd_down() {
    log "Deleting Kind cluster '${CLUSTER_NAME}'..."
    kind delete cluster --name "$CLUSTER_NAME"
    log "Done."
}

cmd_status() {
    echo -e "${CYAN}=== LightsOut Schedules ===${NC}"
    kubectl get lightsoutschedules.lightsout.techsupport.mk 2>/dev/null || warn "No schedules found."
    echo ""
    echo -e "${CYAN}=== team-frontend ===${NC}"
    kubectl get deployments -n team-frontend 2>/dev/null || true
    echo ""
    echo -e "${CYAN}=== team-backend ===${NC}"
    kubectl get deployments,statefulsets,cronjobs -n team-backend 2>/dev/null || true
    echo ""
    echo -e "Grafana: ${GREEN}http://localhost:30080${NC}  (admin / <password from initial setup>)"
}

case "${1:-help}" in
    up)     cmd_up ;;
    down)   cmd_down ;;
    status) cmd_status ;;
    *)
        echo "Usage: $0 {up|down|status}"
        echo ""
        echo "  up      Create Kind cluster and deploy full demo environment"
        echo "  down    Tear down the Kind cluster"
        echo "  status  Show schedule and workload state"
        exit 1
        ;;
esac
