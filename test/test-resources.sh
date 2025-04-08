#!/bin/bash

# Directory for test outputs
OUTPUT_DIR="test-outputs"
mkdir -p "${OUTPUT_DIR}"

# Function to test a resource
test_resource() {
    local resource=$1
    local name=$2
    local namespace=${3:-default}

    echo "Testing ${resource} ${name} in namespace ${namespace}"

    # Original resource
    kubectl get "${resource}" "${name}" -n "${namespace}" -o yaml > "${OUTPUT_DIR}/original_${resource}_${name}.yaml"

    # Cleaned resource
    kubectl get "${resource}" "${name}" -n "${namespace}" -o yaml | go run ../Klean.go > "${OUTPUT_DIR}/cleaned_${resource}_${name}.yaml"

    echo "Generated: ${OUTPUT_DIR}/original_${resource}_${name}.yaml"
    echo "Generated: ${OUTPUT_DIR}/cleaned_${resource}_${name}.yaml"
    echo "---"
}

# Test common resource types
resources=(
    "deployment"
    "service"
    "configmap"
    "secret"
    "statefulset"
    "daemonset"
    "ingress"
    "persistentvolumeclaim"
)

# Get a list of resources in default namespace
echo "Fetching resources from default namespace..."

for resource in "${resources[@]}"; do
    # Get resource names
    names=$(kubectl get "${resource}" -o custom-columns=":metadata.name" --no-headers 2>/dev/null)
    if [ ! -z "$names" ]; then
        while IFS= read -r name; do
            test_resource "${resource}" "${name}"
        done <<< "$names"
    fi
done

echo "All test files have been generated in ${OUTPUT_DIR}"
echo "You can compare original and cleaned versions using:"
echo "diff -y ${OUTPUT_DIR}/original_RESOURCE_NAME.yaml ${OUTPUT_DIR}/cleaned_RESOURCE_NAME.yaml"
