#!/bin/bash

# Get namespace from argument or use default
NAMESPACE=${1:-default}
OUTPUT_DIR="test-outputs"
mkdir -p "${OUTPUT_DIR}"

echo "Testing cleanup of all resources in namespace: ${NAMESPACE}"

# Get all resources from the namespace
kubectl get all -n "${NAMESPACE}" -o yaml > "${OUTPUT_DIR}/original_namespace_${NAMESPACE}.yaml"

# Clean all resources
kubectl get all -n "${NAMESPACE}" -o yaml | go run ../Klean.go > "${OUTPUT_DIR}/cleaned_namespace_${NAMESPACE}.yaml"

echo "Generated: ${OUTPUT_DIR}/original_namespace_${NAMESPACE}.yaml"
echo "Generated: ${OUTPUT_DIR}/cleaned_namespace_${NAMESPACE}.yaml"
echo "You can compare using:"
echo "diff -y ${OUTPUT_DIR}/original_namespace_${NAMESPACE}.yaml ${OUTPUT_DIR}/cleaned_namespace_${NAMESPACE}.yaml"
