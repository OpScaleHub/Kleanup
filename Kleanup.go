// Copyright (c) 2024 OpScaleHub
// SPDX-License-Identifier: MIT

package main

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"strings"

	"gopkg.in/yaml.v2"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/kubernetes/scheme"
)

// Struct to represent any Kubernetes object (only metadata for simplicity)
type KubernetesObject struct {
	APIVersion string                 `yaml:"apiVersion"`
	Kind       string                 `yaml:"kind"`
	Metadata   map[string]interface{} `yaml:"metadata"`
	Spec       map[string]interface{} `yaml:"spec,omitempty"`
	Status     map[string]interface{} `yaml:"status,omitempty"`
	Data       map[string]interface{} `yaml:"data,omitempty"`
	Type       string                 `yaml:"type,omitempty"`
}

// Fields to remove from metadata
var metadataFieldsToRemove = []string{
	"creationTimestamp",
	"generation",
	"resourceVersion",
	"selfLink",
	"uid",
	"managedFields",
	"ownerReferences",
	"finalizers",
	"generateName",
	"labels",    // Add optional label cleanup
	"namespace", // Will be handled separately
}

// Additional runtime fields to remove
var runtimeFieldsToRemove = []string{
	"status",
	"template.generation",
	"metadata.generation",
	"metadata.resourceVersion",
	"metadata.selfLink",
	"metadata.uid",
	"metadata.creationTimestamp",
	"metadata.deletionTimestamp",
	"metadata.deletionGracePeriodSeconds",
	"metadata.managedFields",
	"metadata.labels",
	"metadata.annotations.kubectl.kubernetes.io/last-applied-configuration",
	"status.conditions",
	"status.observedGeneration",
	"status.loadBalancer",
}

// Annotations to remove
var annotationPrefixesToRemove = []string{
	"kubectl.kubernetes.io/",
	"deployment.kubernetes.io/",
	"kubernetes.io/",
	"k8s.io/",
	"control-plane.alpha.kubernetes.io/",
	"app.kubernetes.io/",
	"autoscaling.alpha.kubernetes.io/",
	"batch.kubernetes.io/",
	"helm.sh/",
	"meta.helm.sh/",
}

// Fields to remove from spec
var specFieldsToRemove = []string{
	"progressDeadlineSeconds",
	"revisionHistoryLimit",
	"terminationMessagePath",
	"terminationMessagePolicy",
	"dnsPolicy",
	"schedulerName",
	"securityContext",
	"terminationGracePeriodSeconds",
	"serviceAccount",
	"nodeName",
	"hostname",
	"subdomain",
	"clusterIP",
	"clusterIPs",
	"volumeName",
	"volumeClaimTemplate",
	"serviceAccountName",
	"automountServiceAccountToken",
	"nodeSelector",
	"tolerations",
	"hostNetwork",
	"hostPID",
	"hostIPC",
}

// Custom error types
type EmptyDocumentError struct{}

func (e *EmptyDocumentError) Error() string {
	return "empty YAML document"
}

// cleanAnnotations removes annotations matching specific prefixes
func cleanAnnotations(annotations map[string]interface{}) {
	for key := range annotations {
		shouldDelete := false
		for _, prefix := range annotationPrefixesToRemove {
			if strings.HasPrefix(key, prefix) {
				shouldDelete = true
				break
			}
		}
		if shouldDelete {
			delete(annotations, key)
		}
	}
}

// cleanMetadata removes unwanted fields from the metadata section
func cleanMetadata(metadata map[string]interface{}) {
	// First clean annotations
	if annotations, ok := metadata["annotations"].(map[string]interface{}); ok {
		cleanAnnotations(annotations)
		if len(annotations) == 0 {
			delete(metadata, "annotations")
		}
	}

	// Remove all unwanted fields
	for _, field := range metadataFieldsToRemove {
		delete(metadata, field)
	}

	// Clean labels if they match certain patterns
	if labels, ok := metadata["labels"].(map[string]interface{}); ok {
		for k := range labels {
			if strings.HasPrefix(k, "kubernetes.io/") ||
				strings.HasPrefix(k, "k8s.io/") ||
				strings.HasPrefix(k, "control-plane.alpha.kubernetes.io/") {
				delete(labels, k)
			}
		}
		if len(labels) == 0 {
			delete(metadata, "labels")
		}
	}

	// Remove metadata if empty
	if len(metadata) == 0 {
		delete(metadata, "metadata")
	}
}

// cleanSpec removes unwanted fields from the spec section
func cleanSpec(spec map[string]interface{}) {
	// Remove specific fields
	fieldsToRemove := append(specFieldsToRemove,
		"progressDeadlineSeconds",
		"revisionHistoryLimit",
		"strategy",
		"template.spec.imagePullPolicy",
		"template.spec.terminationMessagePath",
		"template.spec.terminationMessagePolicy",
		"template.spec.dnsPolicy",
		"template.spec.schedulerName",
		"template.spec.securityContext",
		"template.spec.terminationGracePeriodSeconds",
	)

	for _, field := range fieldsToRemove {
		parts := strings.Split(field, ".")
		current := spec
		for i, part := range parts {
			if i == len(parts)-1 {
				delete(current, part)
				continue
			}
			if next, ok := current[part].(map[string]interface{}); ok {
				current = next
			} else {
				break
			}
		}
	}

	// Clean template metadata and spec
	if template, ok := spec["template"].(map[string]interface{}); ok {
		if templateMetadata, ok := template["metadata"].(map[string]interface{}); ok {
			cleanMetadata(templateMetadata)
			if len(templateMetadata) == 0 {
				delete(template, "metadata")
			}
		}
		if templateSpec, ok := template["spec"].(map[string]interface{}); ok {
			cleanPodTemplateSpec(templateSpec)
			if len(templateSpec) == 0 {
				delete(template, "spec")
			}
		}
	}
}

// cleanDeploymentSpec handles deployment-specific cleanup
func cleanDeploymentSpec(spec map[string]interface{}) {
	// Remove deployment-specific fields
	fieldsToRemove := []string{
		"replicas",
		"paused",
		"progressDeadlineSeconds",
		"revisionHistoryLimit",
		"strategy",
		"selector", // Often auto-generated
	}

	for _, field := range fieldsToRemove {
		delete(spec, field)
	}

	// Clean pod template
	if template, ok := spec["template"].(map[string]interface{}); ok {
		cleanPodTemplateSpec(template)
		// Remove template if empty
		if len(template) == 0 {
			delete(spec, "template")
		}
	}
}

// cleanPodTemplateSpec cleans pod template specific fields
func cleanPodTemplateSpec(template map[string]interface{}) {
	if spec, ok := template["spec"].(map[string]interface{}); ok {
		// Remove runtime specific pod fields
		for _, field := range []string{
			"nodeName",
			"serviceAccountName",
			"automountServiceAccountToken",
			"dnsPolicy",
			"nodeSelector",
			"tolerations",
			"schedulerName",
			"priorityClassName",
			"enableServiceLinks",
			"preemptionPolicy",
		} {
			delete(spec, field)
		}

		// Clean container specs
		if containers, ok := spec["containers"].([]interface{}); ok {
			for _, c := range containers {
				if container, ok := c.(map[string]interface{}); ok {
					cleanContainerSpec(container)
				}
			}
		}
	}
}

// cleanContainerSpec cleans container specific fields
func cleanContainerSpec(container map[string]interface{}) {
	fieldsToRemove := []string{
		"terminationMessagePath",
		"terminationMessagePolicy",
		"imagePullPolicy",
		"securityContext",
		"livenessProbe",
		"readinessProbe",
		"startupProbe",
		"resources", // Often has default values
		"ports",     // Clean if using default values
	}

	for _, field := range fieldsToRemove {
		delete(container, field)
	}

	// Clean port definitions if they're using defaults
	if ports, ok := container["ports"].([]interface{}); ok {
		cleanPorts := make([]interface{}, 0)
		for _, p := range ports {
			if port, ok := p.(map[string]interface{}); ok {
				// Remove default protocol
				if proto, exists := port["protocol"].(string); exists && proto == "TCP" {
					delete(port, "protocol")
				}
				if len(port) > 0 {
					cleanPorts = append(cleanPorts, port)
				}
			}
		}
		if len(cleanPorts) > 0 {
			container["ports"] = cleanPorts
		} else {
			delete(container, "ports")
		}
	}
}

// cleanConfigMapData cleans ConfigMap specific data
func cleanConfigMapData(data map[string]interface{}) {
	// Keep the data but clean any runtime-specific content
	for key, value := range data {
		if strVal, ok := value.(string); ok {
			// Clean any runtime configuration from data values
			if strings.Contains(strVal, "kubectl.kubernetes.io") ||
				strings.Contains(strVal, "kubernetes.io/") {
				delete(data, key)
			}
		}
	}
}

// Recursive cleanup function
func cleanupMap(m map[string]interface{}) {
	// Skip data cleanup for Secret objects
	isSecret := false
	if kind, ok := m["kind"].(string); ok && kind == "Secret" {
		isSecret = true
	}

	// Clean metadata
	if metadata, ok := m["metadata"].(map[string]interface{}); ok {
		cleanMetadata(metadata)
	}

	// Clean spec if not a Secret
	if !isSecret {
		if spec, ok := m["spec"].(map[string]interface{}); ok {
			cleanSpec(spec)
		}
	}

	// Apply resource-specific cleanup
	if kind, ok := m["kind"].(string); ok {
		switch kind {
		case "Deployment":
			if spec, ok := m["spec"].(map[string]interface{}); ok {
				cleanDeploymentSpec(spec)
			}
		case "StatefulSet", "DaemonSet":
			if spec, ok := m["spec"].(map[string]interface{}); ok {
				cleanDeploymentSpec(spec) // Similar cleanup as Deployment
			}
		case "Service":
			if spec, ok := m["spec"].(map[string]interface{}); ok {
				delete(spec, "clusterIP")
				delete(spec, "clusterIPs")
			}
		case "ConfigMap":
			if data, ok := m["data"].(map[string]interface{}); ok {
				cleanConfigMapData(data)
			}
		}
	}

	// Remove all runtime fields
	removeRuntimeFields(m)

	// Recursively process nested maps and arrays
	for _, value := range m {
		switch v := value.(type) {
		case map[string]interface{}:
			cleanupMap(v)
		case []interface{}:
			for _, item := range v {
				if itemMap, ok := item.(map[string]interface{}); ok {
					cleanupMap(itemMap)
				}
			}
		}
	}

	// Remove empty maps
	for key, value := range m {
		if mapVal, ok := value.(map[string]interface{}); ok && len(mapVal) == 0 {
			delete(m, key)
		}
	}
}

// removeRuntimeFields removes all runtime-specific fields
func removeRuntimeFields(m map[string]interface{}) {
	for _, path := range runtimeFieldsToRemove {
		parts := strings.Split(path, ".")
		current := m
		for _, part := range parts[:len(parts)-1] {
			if next, ok := current[part].(map[string]interface{}); ok {
				current = next
			} else {
				break
			}
		}
		if len(parts) > 0 {
			delete(current, parts[len(parts)-1])
		}
	}
}

// cleanKubernetesObject ensures the object is stripped of cluster-specific data
func cleanKubernetesObject(objMap map[string]interface{}) {
	// Remove namespace-specific fields
	if metadata, ok := objMap["metadata"].(map[string]interface{}); ok {
		delete(metadata, "namespace")
	}

	// Remove controller-specific fields
	if spec, ok := objMap["spec"].(map[string]interface{}); ok {
		delete(spec, "replicas")
	}

	// Validate and clean fields using Kubernetes schema
	apiVersion, _ := objMap["apiVersion"].(string)
	kind, _ := objMap["kind"].(string)
	gvk := schema.FromAPIVersionAndKind(apiVersion, kind)

	// Create a placeholder object for schema validation
	obj, err := scheme.Scheme.New(gvk)
	if err != nil {
		// If the object type is unknown, skip schema-based cleanup
		fmt.Fprintf(os.Stderr, "Warning: Unknown GVK %s, skipping schema-based cleanup\n", gvk)
		return
	}

	// Perform schema-based cleanup (if applicable)
	// Placeholder logic: Add schema-based cleanup here if needed
	_ = obj
}

func cleanupManifest(input io.Reader, output io.Writer) error {
	reader := bufio.NewReader(input)
	decoder := yaml.NewDecoder(reader)
	encoder := yaml.NewEncoder(output)
	defer encoder.Close()

	documentCount := 0
	for {
		var obj KubernetesObject
		err := decoder.Decode(&obj)
		if err == io.EOF {
			if documentCount == 0 {
				return &EmptyDocumentError{}
			}
			break
		}
		if err != nil {
			return fmt.Errorf("error decoding YAML: %w", err)
		}
		documentCount++

		// Skip empty documents
		if obj.Kind == "" && obj.APIVersion == "" {
			continue
		}

		// Convert the object to a map for recursive cleanup
		objMap := make(map[string]interface{})
		data, err := yaml.Marshal(obj)
		if err != nil {
			return fmt.Errorf("error marshaling object: %w", err)
		}
		if err := yaml.Unmarshal(data, &objMap); err != nil {
			return fmt.Errorf("error unmarshaling to map: %w", err)
		}

		// Perform recursive cleanup
		cleanupMap(objMap)

		// Perform additional cleanup using Kubernetes-specific logic
		cleanKubernetesObject(objMap)

		// Encode the cleaned object
		err = encoder.Encode(objMap)
		if err != nil {
			return fmt.Errorf("error encoding cleaned YAML: %w", err)
		}
	}

	return nil
}

func main() {
	if err := cleanupManifest(os.Stdin, os.Stdout); err != nil {
		if _, ok := err.(*EmptyDocumentError); ok {
			fmt.Fprintln(os.Stderr, "Warning: No valid YAML documents found")
			return
		}
		fmt.Fprintf(os.Stderr, "Error cleaning manifest: %v\n", err)
		os.Exit(1)
	}
}
