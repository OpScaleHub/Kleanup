# Kleanup

[![License: MIT](https://img.shields.io/badge/License-MIT-yellow.svg)](https://opensource.org/licenses/MIT)
[![Go Version](https://img.shields.io/github/go-mod/go-version/OpScaleHub/Kleanup)](https://golang.org/)

A lightweight tool to clean Kubernetes YAML manifests by removing runtime-specific fields, making them portable across clusters and namespaces.

## Features

- Removes runtime-specific metadata (timestamps, UIDs, etc.)
- Cleans up controller-specific annotations
- Removes cluster-specific configuration
- Makes manifests portable across namespaces
- Supports multiple Kubernetes resource types
- Preserves essential configuration

## Installation

```bash
go install github.com/OpScaleHub/Kleanup@latest
```

## Usage

```bash
# Clean a deployment manifest
kubectl get deployment myapp -o yaml | klean > clean-deployment.yaml

# Clean multiple resources
kubectl get deployment,service,configmap -o yaml | klean > clean-manifests.yaml

# Clean all resources in a namespace
kubectl get all -o yaml | klean > backup.yaml

# Clean and apply to another namespace
kubectl get deployment myapp -o yaml | klean | kubectl apply -f - --namespace=staging
```

## Examples

Input:
```yaml
apiVersion: apps/v1
kind: Deployment
metadata:
  annotations:
    deployment.kubernetes.io/revision: "2"
  creationTimestamp: "2023-04-08T19:51:09Z"
  generation: 2
  name: myapp
  namespace: default
  resourceVersion: "4433"
  uid: a174f3d1-0b1d-4ec5-9da4-8b7c889362ca
spec:
  replicas: 1
  template:
    spec:
      containers:
      - image: nginx
        name: nginx
```

Output:
```yaml
apiVersion: apps/v1
kind: Deployment
metadata:
  name: myapp
  namespace: default
spec:
  template:
    spec:
      containers:
      - image: nginx
        name: nginx
```

## Contributing

Contributions are welcome! Please feel free to submit a Pull Request.

## License

MIT License - see LICENSE file for details
