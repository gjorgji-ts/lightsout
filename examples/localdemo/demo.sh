#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
CLUSTER_NAME="lightsout-demo"
CERT_MANAGER_VERSION="v1.19.1"
CNPG_VERSION="1.30.0"
RABBITMQ_OPERATOR_VERSION="v2.23.0"
# The demo tracks the newest release rather than a pinned version, so it cannot drift
# behind the dashboards. The image has a "latest" tag. The chart has no equivalent:
# helm reads --version as a semver constraint, so omitting it is what takes the newest
# chart. A locally built image gets its own tag, so a remote run after a local one
# cannot pick up the source build by mistake.
LIGHTSOUT_IMAGE_TAG="latest"
LIGHTSOUT_DEV_TAG="dev"
SCRIPT_DIR_ROOT="$(cd "$SCRIPT_DIR/../.." && pwd)"
DASHBOARD_DIR="$SCRIPT_DIR_ROOT/examples/grafana"
LIGHTSOUT_CHART_OCI="oci://ghcr.io/gjorgji-ts/charts/lightsout"
LIGHTSOUT_CHART_LOCAL="$SCRIPT_DIR_ROOT/charts/lightsout"
CNPG_MANIFEST="https://github.com/cloudnative-pg/cloudnative-pg/releases/download/v${CNPG_VERSION}/cnpg-${CNPG_VERSION}.yaml"
RABBITMQ_MANIFEST="https://github.com/rabbitmq/cluster-operator/releases/download/${RABBITMQ_OPERATOR_VERSION}/cluster-operator.yml"
GRAFANA_PASSWORD="$(openssl rand -base64 12)"

# Namespaces with plain workloads, in the order the dashboard lists them.
DEMO_NAMESPACES=(
    neon-arcade
    midnight-diner
    jellyfish-cdn
    moonshot-labs
    sleepy-hollow
    popcorn-ci
    lava-lamp-analytics
    yak-shavers
    hammock-district
    taco-truck-api
    pixel-forge
    robot-petting-zoo
    library-of-sand
    goldfish-memory
    conveyor-belt
    cinema-paradiso
    greenhouse-grid
    ledger-lighthouse
    abacus-annex
    dress-rehearsal
    understudy-stage
)

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
    local source="remote"
    while [[ $# -gt 0 ]]; do
        case "$1" in
            --source)
                source="$2"
                shift 2
                ;;
            *)
                err "Unknown option: $1"
                exit 1
                ;;
        esac
    done
    if [[ "$source" != "local" && "$source" != "remote" ]]; then
        err "Invalid --source value: $source (must be 'local' or 'remote')"
        exit 1
    fi
    log "Source mode: $source"

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

    # Install metrics-server (Kind requires --kubelet-insecure-tls due to self-signed certs)
    log "Installing metrics-server..."
    helm repo add metrics-server https://kubernetes-sigs.github.io/metrics-server/ 2>/dev/null || true
    helm repo update metrics-server
    helm upgrade --install metrics-server metrics-server/metrics-server \
        --namespace kube-system \
        --set 'args={--kubelet-insecure-tls}' \
        --wait --timeout 2m

    # Install the two operators behind the custom resource demo
    log "Installing CloudNativePG ${CNPG_VERSION}..."
    kubectl apply --server-side -f "$CNPG_MANIFEST"
    log "Installing the RabbitMQ cluster operator ${RABBITMQ_OPERATOR_VERSION}..."
    kubectl apply --server-side -f "$RABBITMQ_MANIFEST"
    log "Waiting for the operators..."
    kubectl wait deployment/cnpg-controller-manager \
        --for=condition=Available --namespace cnpg-system --timeout=5m
    kubectl wait deployment/rabbitmq-cluster-operator \
        --for=condition=Available --namespace rabbitmq-system --timeout=5m

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

    # Deploy the Grafana dashboards straight from examples/grafana, so there is
    # only one copy of each dashboard in the repository.
    log "Deploying Grafana dashboards..."
    kubectl create configmap lightsout-grafana-dashboards \
        --namespace monitoring \
        --from-file "$DASHBOARD_DIR/lightsout-overview.json" \
        --from-file "$DASHBOARD_DIR/lightsout-schedule-detail.json" \
        --dry-run=client -o yaml | kubectl apply -f -
    kubectl label configmap lightsout-grafana-dashboards \
        --namespace monitoring grafana_dashboard=1 --overwrite

    # Resolve chart and image based on source mode
    local helm_args=()
    if [[ "$source" == "remote" ]]; then
        log "Using the newest chart from the OCI registry: $LIGHTSOUT_CHART_OCI"
        helm_args=("$LIGHTSOUT_CHART_OCI")

        local image="ghcr.io/gjorgji-ts/lightsout:${LIGHTSOUT_IMAGE_TAG}"
        log "Pulling image directly into Kind node: $image"
        local node="${CLUSTER_NAME}-control-plane"
        local arch
        arch="$(uname -m | sed 's/x86_64/amd64/;s/aarch64/arm64/')"
        local platform="linux/${arch}"
        docker exec "$node" ctr --namespace=k8s.io images pull --platform "$platform" "$image"
    else
        log "Using local chart: $LIGHTSOUT_CHART_LOCAL"
        helm_args=("$LIGHTSOUT_CHART_LOCAL" --set "image.tag=${LIGHTSOUT_DEV_TAG}")

        local image="ghcr.io/gjorgji-ts/lightsout:${LIGHTSOUT_DEV_TAG}"
        log "Building image locally: $image"
        make -C "$SCRIPT_DIR_ROOT" docker-build IMG="$image"
        log "Loading image into Kind cluster..."
        kind load docker-image "$image" --name "$CLUSTER_NAME"
    fi

    # Install LightsOut operator
    log "Installing LightsOut operator..."
    kubectl create namespace lightsout-system --dry-run=client -o yaml | kubectl apply -f -
    helm upgrade --install lightsout "${helm_args[@]}" \
        --namespace lightsout-system \
        --values "$SCRIPT_DIR/values/lightsout.yaml" \
        --wait --timeout 5m

    # Deploy demo apps
    log "Deploying demo namespaces and apps..."
    kubectl apply -f "$SCRIPT_DIR/manifests/apps/"

    log "Waiting for demo apps to be ready..."
    for ns in "${DEMO_NAMESPACES[@]}"; do
        kubectl wait deployment --all \
            --for=condition=Available \
            --namespace "$ns" \
            --timeout=180s
    done

    log "Waiting for the operator-managed resources to be ready..."
    kubectl wait cluster.postgresql.cnpg.io/launch-log \
        --for=condition=Ready --namespace moonshot-labs --timeout=5m
    kubectl wait cluster.postgresql.cnpg.io/ledger-db \
        --for=condition=Ready --namespace ledger-lighthouse --timeout=5m
    kubectl wait rabbitmqcluster.rabbitmq.com/bubble-bus \
        --for=condition=AllReplicasReady --namespace lava-lamp-analytics --timeout=5m

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

    # Deploy schedules
    log "Deploying demo schedules..."
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
    echo -e "  14 schedules, 21 namespaces, 3 operator-managed resources."
    echo -e "  The ones worth watching:"
    echo -e "    ${CYAN}platform-offhours${NC}   cluster-scoped, 6 min cycle, hibernates a Postgres cluster"
    echo -e "    ${CYAN}batch-offhours${NC}      cluster-scoped, 6 min cycle, half a cycle apart"
    echo -e "    ${CYAN}reporting-offhours${NC}  cluster-scoped, 20 min cycle, a second Postgres cluster"
    echo -e "    ${CYAN}analytics-offhours${NC}  lava-lamp-analytics, 10 min cycle, stops a RabbitMQ cluster"
    echo -e "    ${CYAN}cache-offhours${NC}      goldfish-memory, 3 min cycle, the fastest one"
    echo ""
    echo -e "  Open the ${CYAN}LightsOut - Overview${NC} dashboard in Grafana to watch."
    echo -e "  Give it two or three cycles to fill in."
    echo ""
    echo -e "  ${YELLOW}./demo.sh adopt${NC}   hand hammock-district from after-hours to its own schedule"
    echo -e "  ${YELLOW}./demo.sh status${NC}  show schedule and workload state"
    echo -e "  ${YELLOW}./demo.sh down${NC}    tear down the Kind cluster"
    echo ""
}

cmd_down() {
    log "Deleting Kind cluster '${CLUSTER_NAME}'..."
    kind delete cluster --name "$CLUSTER_NAME"
    log "Done."
}

cmd_adopt() {
    log "Giving hammock-district its own namespace-scoped schedule..."
    kubectl apply -f "$SCRIPT_DIR/manifests/sandbox-ns-schedule.yaml"
    echo ""
    echo -e "  platform-offhours releases the namespace on its next reconcile and"
    echo -e "  stops counting its workloads. ${YELLOW}sandbox-offhours${NC} takes over on an"
    echo -e "  8 minute cycle."
}

cmd_status() {
    echo -e "${CYAN}=== Schedules ===${NC}"
    kubectl get lightsoutschedules.lightsout.techsupport.mk 2>/dev/null || warn "No cluster schedules found."
    echo ""
    kubectl get lightsoutnamespaceschedules.lightsout.techsupport.mk \
        --all-namespaces 2>/dev/null || true
    echo ""
    for ns in "${DEMO_NAMESPACES[@]}"; do
        echo -e "${CYAN}=== ${ns} ===${NC}"
        kubectl get deployments,statefulsets,cronjobs --namespace "$ns" 2>/dev/null || true
        echo ""
    done
    echo -e "${CYAN}=== Operator-managed resources ===${NC}"
    kubectl get cluster.postgresql.cnpg.io --all-namespaces \
        -o 'custom-columns=NS:.metadata.namespace,NAME:.metadata.name,INSTANCES:.spec.instances,HIBERNATION:.metadata.annotations.cnpg\.io/hibernation,STATUS:.status.phase' 2>/dev/null || true
    kubectl get rabbitmqcluster.rabbitmq.com/bubble-bus --namespace lava-lamp-analytics \
        -o 'custom-columns=NAME:.metadata.name,REPLICAS:.spec.replicas,READY:.status.conditions[?(@.type=="AllReplicasReady")].status' 2>/dev/null || true
    echo ""
    echo -e "${CYAN}=== Terminating pods ===${NC}"
    kubectl get pods --all-namespaces 2>/dev/null | awk 'NR==1 || /Terminating/' || true
    echo ""
    echo -e "Grafana: ${GREEN}http://localhost:30080${NC}  (admin / <password from initial setup>)"
}

case "${1:-help}" in
    up)      shift; cmd_up "$@" ;;
    down)    cmd_down ;;
    status)  cmd_status ;;
    adopt)   cmd_adopt ;;
    *)
        echo "Usage: $0 {up|down|status|adopt} [options]"
        echo ""
        echo "  up [--source local|remote]"
        echo "          Create Kind cluster and deploy full demo environment"
        echo "          --source local   Build image and use chart from source"
        echo "          --source remote  Pull the newest released image and chart from ghcr.io (default)"
        echo "  down    Tear down the Kind cluster"
        echo "  status  Show schedule and workload state"
        echo "  adopt   Hand hammock-district to its own namespace-scoped schedule"
        exit 1
        ;;
esac
