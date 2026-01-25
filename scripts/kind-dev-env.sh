#!/bin/bash

# This shell script deploys a kind cluster with an Istio-based Gateway API
# implementation fully configured. It deploys the vllm simulator, which it
# exposes with an InferencePool.

set -eo pipefail

# ------------------------------------------------------------------------------
# Variables
# ------------------------------------------------------------------------------

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

# Set a default CLUSTER_NAME if not provided
: "${CLUSTER_NAME:=llm-d-inference-proxy}"

# Set the host port to map to the Gateway's inbound port (30080)
: "${PROXY_HOST_PORT:=30080}"

# Set the default IMAGE_REGISTRY if not provided
: "${IMAGE_REGISTRY:=ghcr.io/llm-d}"

# Set a default VLLM_SIMULATOR_TAG if not provided
export VLLM_SIMULATOR_TAG="${VLLM_SIMULATOR_TAG:-dev}"

# Set a default VLLM_SIMULATOR_IMAGE if not provided
VLLM_SIMULATOR_IMAGE="${VLLM_SIMULATOR_IMAGE:-${IMAGE_REGISTRY}/llm-d-inference-sim:${VLLM_SIMULATOR_TAG}}"
export VLLM_SIMULATOR_IMAGE

# Set a default PROXY_TAG if not provided
export PROXY_TAG="${PROXY_TAG:-dev}"

# Set a default PROXY_IMAGE if not provided
PROXY_IMAGE="${PROXY_IMAGE:-${IMAGE_REGISTRY}/llm-d-inference-proxy:${PROXY_TAG}}"
export PROXY_IMAGE

# Set the model name to deploy
export MODEL_NAME="${MODEL_NAME:-food-review}"
# Extract model family (e.g., "meta-llama" from "meta-llama/Llama-3.1-8B-Instruct")
export MODEL_FAMILY="${MODEL_NAME%%/*}"
# Extract model ID (e.g., "Llama-3.1-8B-Instruct")
export MODEL_ID="${MODEL_NAME##*/}"
# Safe model name for Kubernetes resources (lowercase, hyphenated)
export MODEL_NAME_SAFE=$(echo "${MODEL_ID}" | tr '[:upper:]' '[:lower:]' | tr ' /_.' '-')

# Set the proxy to deploy
export PROXY_NAME="${PROXY_NAME:-${MODEL_NAME_SAFE}-proxy}"

# Set the default routing side car image tag
export SIDECAR_TAG="${SIDECAR_TAG:-dev}"

# Set a default SIDECAR_IMAGE if not provided
SIDECAR_IMAGE="${SIDECAR_IMAGE:-${IMAGE_REGISTRY}/llm-d-routing-sidecar:${SIDECAR_TAG}}"
export SIDECAR_IMAGE

# Set the inference pool name for the deployment
export POOL_NAME="${POOL_NAME:-${MODEL_NAME_SAFE}-inference-pool}"

# vLLM replica count (without PD)
export VLLM_REPLICA_COUNT="${VLLM_REPLICA_COUNT:-1}"

# By default we are not setting up for PD
export PD_ENABLED="\"${PD_ENABLED:-false}\""

# By default we are not setting up for PD with a Sidecar
export WITH_SIDECAR="\"${WITH_SIDECAR:-false}\""

# By default we are not setting up for KV cache
export KV_CACHE_ENABLED="${KV_CACHE_ENABLED:-false}"

# Replica counts for P and D
export VLLM_REPLICA_COUNT_P="${VLLM_REPLICA_COUNT_P:-1}"
export VLLM_REPLICA_COUNT_D="${VLLM_REPLICA_COUNT_D:-2}"

# Data Parallel size
export VLLM_DATA_PARALLEL_SIZE="${VLLM_DATA_PARALLEL_SIZE:-1}"

PRIMARY_PORT="0"
if [ "${PD_ENABLED}" != "\"true\"" ]; then
  if [ "${KV_CACHE_ENABLED}" != "true" ]; then
    DEFAULT_PROXY_CONFIG="deploy/config/proxy-config.yaml"
  else
    DEFAULT_PROXY_CONFIG="deploy/config/proxy-kvcache-config.yaml"
  fi
else
  if [ "${KV_CACHE_ENABLED}" != "true" ]; then
    if [ "${WITH_SIDECAR}" == "\"true\"" ]; then
      DEFAULT_PROXY_CONFIG="deploy/config/pd-sidecar-proxy-config.yaml"
      if [ ${VLLM_DATA_PARALLEL_SIZE} -ne 1 ]; then
        PRIMARY_PORT="8000"
      fi
    else 
      DEFAULT_PROXY_CONFIG="deploy/config/pd-proxy-config.yaml"
    fi
  else
    echo "Invalid configuration: PD_ENABLED=true and KV_CACHE_ENABLED=true is not supported"
    exit 1
  fi
fi

export PROXY_CONFIG="${PROXY_CONFIG:-${DEFAULT_PROXY_CONFIG}}"
export PRIMARY_PORT

# ------------------------------------------------------------------------------
# Setup & Requirement Checks
# ------------------------------------------------------------------------------

# Check for a supported container runtime if an explicit one was not set
if [ -z "${CONTAINER_RUNTIME}" ]; then
  if command -v docker &> /dev/null; then
    CONTAINER_RUNTIME="docker"
  elif command -v podman &> /dev/null; then
    CONTAINER_RUNTIME="podman"
  else
    echo "Neither docker nor podman could be found in PATH" >&2
    exit 1
  fi
fi

set -u

# Check for required programs
for cmd in kind kubectl kustomize ${CONTAINER_RUNTIME}; do
    if ! command -v "$cmd" &> /dev/null; then
        echo "Error: $cmd is not installed or not in the PATH."
        exit 1
    fi
done

TARGET_PORTS="8000"

NEW_LINE=$'\n'
for ((i = 1; i < VLLM_DATA_PARALLEL_SIZE; ++i)); do
    EXTRA_PORT=$((8000 + i))
    TARGET_PORTS="${TARGET_PORTS}${NEW_LINE}  - number: ${EXTRA_PORT}"
done

export TARGET_PORTS

# ------------------------------------------------------------------------------
# Cluster Deployment
# ------------------------------------------------------------------------------

# Check if the cluster already exists
if kind get clusters 2>/dev/null | grep -q "^${CLUSTER_NAME}$"; then
    echo "Cluster '${CLUSTER_NAME}' already exists, re-using"
else
    kind create cluster --name "${CLUSTER_NAME}" --config - << EOF
kind: Cluster
apiVersion: kind.x-k8s.io/v1alpha4
nodes:
- role: control-plane
  extraPortMappings:
  - containerPort: 30080
    hostPort: ${PROXY_HOST_PORT}
    protocol: TCP
EOF
fi

# Set the kubectl context to the kind cluster
KUBE_CONTEXT="kind-${CLUSTER_NAME}"
kubectl config set-context ${KUBE_CONTEXT} --namespace=default

set -x

# Hotfix for https://github.com/kubernetes-sigs/kind/issues/3880
CONTAINER_NAME="${CLUSTER_NAME}-control-plane"
${CONTAINER_RUNTIME} exec -it ${CONTAINER_NAME} /bin/bash -c "sysctl net.ipv4.conf.all.arp_ignore=0"

# Wait for all pods to be ready
kubectl --context ${KUBE_CONTEXT} -n kube-system wait --for=condition=Ready --all pods --timeout=300s

echo "Waiting for local-path-storage pods to be created..."
until kubectl --context ${KUBE_CONTEXT} -n local-path-storage get pods -o name | grep -q pod/; do
  sleep 2
done
kubectl --context ${KUBE_CONTEXT} -n local-path-storage wait --for=condition=Ready --all pods --timeout=300s

# ------------------------------------------------------------------------------
# Load Container Images
# ------------------------------------------------------------------------------

# Load the vllm simulator image into the cluster (only if it's a locally built image)
if [ -n "$(${CONTAINER_RUNTIME} images -q "${VLLM_SIMULATOR_IMAGE}")" ]; then
    if [ "${CONTAINER_RUNTIME}" == "podman" ]; then
        podman save ${VLLM_SIMULATOR_IMAGE} -o /dev/stdout | kind --name ${CLUSTER_NAME} load image-archive /dev/stdin
    else
        if docker image inspect "${VLLM_SIMULATOR_IMAGE}" > /dev/null 2>&1; then
            echo "INFO: Loading image into KIND cluster..."
            kind --name ${CLUSTER_NAME} load docker-image ${VLLM_SIMULATOR_IMAGE}
        fi
    fi
fi

# Load the proxy image into the cluster
if [ "${CONTAINER_RUNTIME}" == "podman" ]; then
	podman save ${PROXY_IMAGE} -o /dev/stdout | kind --name ${CLUSTER_NAME} load image-archive /dev/stdin
else
	kind --name ${CLUSTER_NAME} load docker-image ${PROXY_IMAGE}
fi

# Load the sidecar image into the cluster
if [ "${CONTAINER_RUNTIME}" == "podman" ]; then
	podman save ${SIDECAR_IMAGE} -o /dev/stdout | kind --name ${CLUSTER_NAME} load image-archive /dev/stdin
else
	kind --name ${CLUSTER_NAME} load docker-image ${SIDECAR_IMAGE}
fi


# ------------------------------------------------------------------------------
# CRD Deployment (GIE)
# ------------------------------------------------------------------------------

kustomize build deploy/components/crds-gie |
	kubectl --context ${KUBE_CONTEXT} apply --server-side --force-conflicts -f -

# ------------------------------------------------------------------------------
# Development Environment
# ------------------------------------------------------------------------------

# Deploy the environment to the "default" namespace
if [ "${PD_ENABLED}" != "\"true\"" ]; then
  KUSTOMIZE_DIR="deploy/environments/dev/kind"
else
  if [ "${WITH_SIDECAR}" != "\"true\"" ]; then
    KUSTOMIZE_DIR="deploy/environments/dev/kind-pd"
  else
    KUSTOMIZE_DIR="deploy/environments/dev/kind-pd-sizecar"
  fi
fi

TEMP_FILE=$(mktemp)
# Ensure that the temporary file is deleted now matter what happens in the script
trap "rm -f \"${TEMP_FILE}\"" EXIT

kubectl --context ${KUBE_CONTEXT} delete configmap proxy-config --ignore-not-found
envsubst '$PRIMARY_PORT' < ${PROXY_CONFIG} > ${TEMP_FILE}
kubectl --context ${KUBE_CONTEXT} create configmap proxy-config --from-file=proxy-config.yaml=${TEMP_FILE}

kustomize build --enable-helm  ${KUSTOMIZE_DIR} \
	| envsubst '${POOL_NAME} ${MODEL_NAME} ${MODEL_NAME_SAFE} ${PROXY_NAME} ${PROXY_IMAGE} ${VLLM_SIMULATOR_IMAGE} \
  ${PD_ENABLED} ${KV_CACHE_ENABLED} ${SIDECAR_IMAGE} ${TARGET_PORTS} \
  ${VLLM_REPLICA_COUNT} ${VLLM_REPLICA_COUNT_P} ${VLLM_REPLICA_COUNT_D} ${VLLM_DATA_PARALLEL_SIZE}' \
  | kubectl --context ${KUBE_CONTEXT} apply -f -

# ------------------------------------------------------------------------------
# Check & Verify
# ------------------------------------------------------------------------------

# Wait for all deployments to be ready
kubectl --context ${KUBE_CONTEXT} -n default wait --for=condition=available --timeout=300s deployment --all

cat <<EOF
-----------------------------------------
Deployment completed!

* Kind Cluster Name: ${CLUSTER_NAME}
* Kubectl Context: ${KUBE_CONTEXT}

Status:

* The vllm simulator is running and exposed via InferencePool
* The Proxy is exposed via the NodePort
* The Endpoint Picker is loaded into the Gateway via ext_proc

You can watch the Proxy logs with:

  $ kubectl --context ${KUBE_CONTEXT} logs -f deployments/${PROXY_NAME}

With that running in the background, you can make requests:

  $ curl -s -w '\n' http://localhost:${PROXY_HOST_PORT}/v1/completions -H 'Content-Type: application/json' -d '{"model":"${MODEL_NAME}","prompt":"hi","max_tokens":10,"temperature":0}' | jq

See DEVELOPMENT.md for additional access methods if the above fails.

-----------------------------------------
EOF
