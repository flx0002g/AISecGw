#!/usr/bin/env bash
# AISecGw (WntASG) - One-Click Deploy Script
# Usage: ./deploy.sh [command]
# Commands:
#   init      - Create kind cluster and deploy AISecGw (default)
#   console   - Build and deploy WntASG Console only
#   status    - Show deployment status
#   clean     - Remove kind cluster and all resources
#   port-forward - Start port-forward to access console

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
CLUSTER_NAME="${CLUSTER_NAME:-higress}"
KIND_NODE_TAG="${KIND_NODE_TAG:-v1.25.3}"
NAMESPACE="${NAMESPACE:-higress-system}"
CONSOLE_IMAGE="${CONSOLE_IMAGE:-wntasg-console}"
CONSOLE_TAG="${CONSOLE_TAG:-latest}"
# Path to AISecGw-console repo (sibling directory by default)
CONSOLE_REPO="${CONSOLE_REPO:-${SCRIPT_DIR}/../AISecGw-console}"

create_cluster() {
    echo "=== Creating Kind cluster: ${CLUSTER_NAME} ==="

    if kind get clusters 2>/dev/null | grep -q "^${CLUSTER_NAME}$"; then
        echo "Cluster '${CLUSTER_NAME}' already exists. Skipping creation."
        return 0
    fi

    local project_dir="${SCRIPT_DIR}"
    cat <<EOF | kind create cluster --image "kindest/node:${KIND_NODE_TAG}" --name "${CLUSTER_NAME}" --config=-
kind: Cluster
apiVersion: kind.x-k8s.io/v1alpha4
networking:
  ipFamily: dual
nodes:
- role: control-plane
  kubeadmConfigPatches:
  - |
    kind: InitConfiguration
    nodeRegistration:
      kubeletExtraArgs:
        node-labels: "ingress-ready=true"
  extraPortMappings:
  - containerPort: 80
    hostPort: 80
    protocol: TCP
  - containerPort: 443
    hostPort: 443
    protocol: TCP
  extraMounts:
    - hostPath: ${project_dir}/plugins
      containerPath: /opt/plugins
EOF

    echo "=== Kind cluster created ==="
}

deploy_higress() {
    echo "=== Deploying AISecGw via Helm ==="

    # Update helm dependencies
    helm dependency update "${SCRIPT_DIR}/helm/higress"

    if helm status higress -n "${NAMESPACE}" &>/dev/null; then
        echo "Helm release 'higress' already exists. Upgrading..."
        helm upgrade higress "${SCRIPT_DIR}/helm/higress" \
            -n "${NAMESPACE}" \
            --set global.local=true \
            --reuse-values
    else
        helm install higress "${SCRIPT_DIR}/helm/higress" \
            -n "${NAMESPACE}" \
            --create-namespace \
            --set global.local=true \
            --wait --timeout=5m
    fi

    echo "=== AISecGw deployed ==="
}

build_console() {
    echo "=== Building WntASG Console image ==="

    if [ ! -f "${CONSOLE_REPO}/Dockerfile" ]; then
        echo "Error: AISecGw-console repo not found at ${CONSOLE_REPO}"
        echo "Please clone AISecGw-console to ${CONSOLE_REPO} or set CONSOLE_REPO env var"
        exit 1
    fi

    docker build -t "${CONSOLE_IMAGE}:${CONSOLE_TAG}" "${CONSOLE_REPO}"

    # Load into kind cluster
    echo "=== Loading image into kind cluster ==="
    kind load docker-image "${CONSOLE_IMAGE}:${CONSOLE_TAG}" --name "${CLUSTER_NAME}"

    echo "=== Console image built and loaded ==="
}

deploy_console() {
    build_console

    echo "=== Deploying WntASG Console ==="
    helm upgrade higress "${SCRIPT_DIR}/helm/higress" \
        -n "${NAMESPACE}" \
        --set higress-console.image.repository="${CONSOLE_IMAGE}" \
        --set higress-console.image.tag="${CONSOLE_TAG}" \
        --set higress-console.image.pullPolicy=Never \
        --reuse-values

    kubectl rollout status deployment/higress-console -n "${NAMESPACE}" --timeout=120s
    echo "=== WntASG Console deployed ==="
}

init_system() {
    echo "=== Initializing WntASG system ==="

    # Wait for console to be ready
    kubectl wait --for=condition=available deployment/higress-console -n "${NAMESPACE}" --timeout=120s

    # Get console pod
    local pod
    pod=$(kubectl get pods -n "${NAMESPACE}" -l app.kubernetes.io/name=higress-console -o jsonpath='{.items[0].metadata.name}')

    # Check if already initialized
    local init_status
    init_status=$(kubectl exec -n "${NAMESPACE}" "${pod}" -- curl -s -o /dev/null -w '%{http_code}' http://localhost:8080/v1/users 2>/dev/null || echo "000")

    if [ "${init_status}" = "200" ]; then
        echo "System already initialized. Skipping."
        return 0
    fi

    # Initialize system with admin user
    echo "Initializing admin user..."
    kubectl exec -n "${NAMESPACE}" "${pod}" -- \
        curl -s -X POST http://localhost:8080/system/init \
        -H 'Content-Type: application/json' \
        -d '{"adminUser":{"name":"admin","displayName":"Admin","password":"admin123"}}'

    echo ""
    echo "=== System initialized ==="
    echo "Username: admin"
    echo "Password: admin123"
}

show_status() {
    echo "=== AISecGw Deployment Status ==="
    echo ""
    echo "Cluster: ${CLUSTER_NAME}"
    echo "Namespace: ${NAMESPACE}"
    echo ""
    kubectl get pods -n "${NAMESPACE}" -o wide 2>/dev/null || echo "No pods found"
    echo ""
    helm status higress -n "${NAMESPACE}" 2>/dev/null || echo "No helm release found"
}

start_port_forward() {
    echo "=== Starting port-forward to WntASG Console ==="
    echo "Access console at: http://localhost:8080"
    echo "Username: admin | Password: admin123"
    echo "Press Ctrl+C to stop"
    echo ""
    kubectl port-forward -n "${NAMESPACE}" svc/higress-console 8080:8080
}

clean_all() {
    echo "=== Removing Kind cluster: ${CLUSTER_NAME} ==="
    if kind get clusters 2>/dev/null | grep -q "^${CLUSTER_NAME}$"; then
        kind delete cluster --name "${CLUSTER_NAME}"
        echo "Cluster deleted."
    else
        echo "Cluster not found."
    fi
}

case "${1:-init}" in
    init)
        create_cluster
        deploy_higress
        init_system
        echo ""
        echo "=== AISecGw is ready! ==="
        echo "Run './deploy.sh port-forward' to access the console."
        ;;
    console)
        deploy_console
        ;;
    status)
        show_status
        ;;
    clean)
        clean_all
        ;;
    port-forward)
        start_port_forward
        ;;
    *)
        echo "Usage: $0 {init|console|status|clean|port-forward}"
        echo ""
        echo "Commands:"
        echo "  init          Create kind cluster, deploy AISecGw, and initialize system"
        echo "  console       Build and deploy WntASG Console image only"
        echo "  status        Show deployment status"
        echo "  clean         Remove kind cluster and all resources"
        echo "  port-forward  Start port-forward to access console"
        exit 1
        ;;
esac
