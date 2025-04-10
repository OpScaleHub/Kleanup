package main

import (
	"bufio"
	"fmt"
	"io"
	"log"
	"os"
	"reflect"
	"strings"

	"gopkg.in/yaml.v2"
)

// KubernetesObject represents the basic structure of Kubernetes objects.
type KubernetesObject struct {
	APIVersion string                 `yaml:"apiVersion"`
	Kind       string                 `yaml:"kind"`
	Metadata   map[string]interface{} `yaml:"metadata,omitempty"`
	Spec       map[string]interface{} `yaml:"spec,omitempty"`
	Status     map[string]interface{} `yaml:"status,omitempty"`
	Data       map[string]interface{} `yaml:"data,omitempty"`       // For ConfigMaps/Secrets
	StringData map[string]interface{} `yaml:"stringData,omitempty"` // For Secrets
	Type       string                 `yaml:"type,omitempty"`       // e.g., for Secrets
	// Add other common top-level fields if needed
}

// CleanupOptions defines options to customize the cleanup process.
type CleanupOptions struct {
	RemoveManagedFields   bool
	RemoveStatus          bool
	RemoveNamespace       bool
	RemoveClusterName     bool     // Remove cluster name (Placeholder - not implemented yet)
	RemoveLabels          []string // labels to remove
	RemoveAnnotations     []string // annotations to remove
	RemoveEmpty           bool     // Remove empty fields after cleaning
	CleanupFinalizers     bool     // Remove finalizers
	RevertToDeployment    bool     // Attempt to reconstruct Deployment from Pod
	PreserveResourceState bool     // Keep resource state related fields
	ResourceStateMode     string   // "Desired" or "Runtime" cleanup mode
}

// resourceStateFields tracks which fields represent desired vs runtime state using dot notation
var resourceStateFields = map[string]map[string]bool{
	"Deployment": {
		"metadata.generation": false, // runtime state
		"spec.replicas":       true,  // desired state
		"spec.strategy":       true,  // desired state
		"spec.template":       true,  // desired state
		"status":              false, // runtime state
	},
	"Service": {
		"spec.clusterIP":  false, // runtime state
		"spec.clusterIPs": false, // runtime state - Added
		"spec.ports":      true,  // desired state
		"spec.selector":   true,  // desired state
		"status":          false, // runtime state - Added
	},
	"Pod": {
		"metadata.generation": false, // runtime state - Added
		"spec.containers":     true,  // desired state
		"spec.initContainers": true,  // desired state - Added
		"spec.nodeName":       false, // runtime state
		"spec.nodeSelector":   true,  // desired state
		"spec.volumes":        true,  // desired state
		"status":              false, // runtime state
	},
	// Add more kinds and their fields as needed
}

// MetadataCleaner defines an interface for cleaning object metadata.
type MetadataCleaner interface {
	Clean(obj *KubernetesObject, options *CleanupOptions) // Pass the whole object for context
}

// ObjectCleaner defines an interface for cleaning Kubernetes objects.
type ObjectCleaner interface {
	Clean(obj *KubernetesObject, options *CleanupOptions)
}

// GenericMetadataCleaner cleans common metadata fields.
type GenericMetadataCleaner struct{}

func (c *GenericMetadataCleaner) Clean(obj *KubernetesObject, options *CleanupOptions) {
	if obj.Metadata == nil {
		return
	}
	metadata := obj.Metadata

	// Determine fields to remove based on options and state preservation
	fieldsToRemove := map[string]bool{
		"creationTimestamp": true,
		"resourceVersion":   true,
		"selfLink":          true,
		"uid":               true,
		"ownerReferences":   true,
	}

	// Handle generation based on state preservation first
	isGenerationRuntime := false
	if stateFields, ok := resourceStateFields[obj.Kind]; ok {
		if isDesired, exists := stateFields["metadata.generation"]; exists && !isDesired {
			isGenerationRuntime = true
		}
	}
	if !(options.PreserveResourceState && options.ResourceStateMode == "Runtime" && isGenerationRuntime) {
		fieldsToRemove["generation"] = true // Remove generation unless preserving runtime state
	}

	if options.RemoveManagedFields {
		fieldsToRemove["managedFields"] = true
	}
	if options.CleanupFinalizers {
		fieldsToRemove["finalizers"] = true
	}
	if options.RemoveNamespace {
		fieldsToRemove["namespace"] = true
	}

	for field := range fieldsToRemove {
		delete(metadata, field)
	}

	// Clean annotations
	if annotations, ok := metadata["annotations"].(map[string]interface{}); ok {
		cleanAnnotations(annotations, options.RemoveAnnotations)
		if len(annotations) == 0 {
			delete(metadata, "annotations") // Remove empty annotations map
		}
	}
	// Clean labels
	if labels, ok := metadata["labels"].(map[string]interface{}); ok {
		cleanLabels(labels, options.RemoveLabels)
		if len(labels) == 0 {
			delete(metadata, "labels") // Remove empty labels map
		}
	}

	// Note: Removal of the entire metadata map if empty happens in removeEmptyFields
}

func cleanLabels(labels map[string]interface{}, removeLabels []string) {
	if labels == nil {
		return
	}
	for key := range labels {
		for _, labelToRemove := range removeLabels {
			if key == labelToRemove {
				delete(labels, key)
				break // Move to next key once a match is found
			}
		}
	}
}

// cleanAnnotations removes annotations matching specific prefixes and user provided annotations
func cleanAnnotations(annotations map[string]interface{}, removeAnnotations []string) {
	if annotations == nil {
		return
	}
	// Combined list of prefixes and exact matches known to be runtime/operational
	annotationPrefixesToRemove := []string{
		"kubectl.kubernetes.io/",
		"deployment.kubernetes.io/",
		"apps.kubernetes.io/",
		"statefulset.kubernetes.io/",
		"service.kubernetes.io/",
		"batch.kubernetes.io/",
		"networking.k8s.io/",
		"rbac.authorization.k8s.io/",
		"argocd.argoproj.io/",
		"helm.sh/",
		"meta.helm.sh/",
		"fluxcd.io/",               // Added Flux
		"kustomize.config.k8s.io/", // Added Kustomize
		"reloader.stakater.com/",   // Added Reloader
		// Add more common operational tool prefixes
	}
	annotationExactToRemove := map[string]bool{
		"kubernetes.io/change-cause":               true, // Often added by kubectl apply
		"controller-revision-hash":                 true, // Used by StatefulSets/DaemonSets
		"deprecated.daemonset.template.generation": true, // Used by DaemonSets
		"pod-template-hash":                        true, // Used by ReplicaSets (Deployments) - debatable, but often runtime
	}

	keysToDelete := []string{} // Collect keys to delete to avoid modifying map during iteration issues

	for key := range annotations {
		shouldDelete := false

		// Check exact matches first
		if annotationExactToRemove[key] {
			shouldDelete = true
		}

		// Check prefixes
		if !shouldDelete {
			for _, prefix := range annotationPrefixesToRemove {
				if strings.HasPrefix(key, prefix) {
					shouldDelete = true
					break
				}
			}
		}

		// Check user-provided list
		if !shouldDelete {
			for _, annotationToRemove := range removeAnnotations {
				if key == annotationToRemove {
					shouldDelete = true
					break
				}
			}
		}

		if shouldDelete {
			keysToDelete = append(keysToDelete, key)
		}
	}

	for _, key := range keysToDelete {
		delete(annotations, key)
	}
}

// GenericObjectCleaner cleans common object fields.
type GenericObjectCleaner struct {
	metadataCleaner MetadataCleaner
}

func (c *GenericObjectCleaner) Clean(obj *KubernetesObject, options *CleanupOptions) {

	// --- State Preservation Handling (Run First) ---
	if options.PreserveResourceState {
		if stateFields, ok := resourceStateFields[obj.Kind]; ok {
			fieldsToRemoveForState := []string{}
			for fieldPath, isDesired := range stateFields {
				remove := false
				if options.ResourceStateMode == "Desired" && !isDesired {
					remove = true // Remove runtime fields when preserving desired state
				} else if options.ResourceStateMode == "Runtime" && isDesired {
					remove = true // Remove desired fields when preserving runtime state
				}

				if remove {
					fieldsToRemoveForState = append(fieldsToRemoveForState, fieldPath)
				}
			}
			// Remove the identified fields
			for _, fieldPath := range fieldsToRemoveForState {
				removeField(obj, fieldPath)
			}
		}
	}

	// --- Metadata Cleaning (Run After State Preservation) ---
	if obj.Metadata != nil {
		c.metadataCleaner.Clean(obj, options)
	}

	// --- General Status Removal (Run After State Preservation) ---
	// Only remove status generally if state preservation didn't already keep it.
	isStatusRuntime := false
	if stateFields, ok := resourceStateFields[obj.Kind]; ok {
		if isDesired, exists := stateFields["status"]; exists && !isDesired {
			isStatusRuntime = true
		}
	}
	if options.RemoveStatus && !(options.PreserveResourceState && options.ResourceStateMode == "Runtime" && isStatusRuntime) {
		obj.Status = nil
	}

	// --- ClusterName Removal (Placeholder) ---
	if options.RemoveClusterName {
		// TODO: Implement cluster name removal if it exists in a standard location
		// e.g., delete(obj.Metadata, "clusterName") // If it were in metadata
	}

	// --- Final Empty Field Cleanup ---
	if options.RemoveEmpty {
		removeEmptyFields(obj)
	}
}

// Helper to remove nested fields using dot notation
func removeField(obj *KubernetesObject, fieldPath string) {
	parts := strings.Split(fieldPath, ".")
	if len(parts) == 0 {
		return
	}

	// Handle top-level fields directly
	if len(parts) == 1 {
		switch parts[0] {
		case "metadata":
			obj.Metadata = nil
		case "spec":
			obj.Spec = nil
		case "status":
			obj.Status = nil
		case "data":
			obj.Data = nil
		case "stringData":
			obj.StringData = nil
		case "type":
			obj.Type = "" // Reset type for secrets/services if needed
		}
		return
	}

	// Handle nested fields
	var currentMap map[string]interface{}
	switch parts[0] {
	case "metadata":
		currentMap = obj.Metadata
	case "spec":
		currentMap = obj.Spec
	case "status":
		currentMap = obj.Status
	case "data":
		currentMap = obj.Data
	case "stringData":
		currentMap = obj.StringData
	default:
		return // Cannot navigate path
	}

	if currentMap == nil {
		return // Path doesn't exist
	}

	for i := 1; i < len(parts)-1; i++ {
		if next, ok := currentMap[parts[i]].(map[string]interface{}); ok {
			currentMap = next
		} else {
			return // Path doesn't exist or is not a map
		}
	}

	// Delete the final key
	delete(currentMap, parts[len(parts)-1])
}

// removeEmptyFields recursively removes empty maps/slices and nil values.
// It's called last to clean up anything left empty by previous steps.
func removeEmptyFields(data interface{}) interface{} {
	if data == nil {
		return nil
	}

	value := reflect.ValueOf(data)
	kind := value.Kind()

	switch kind {
	case reflect.Map:
		if value.IsNil() {
			return nil
		}

		cleanedMap := make(map[string]interface{}) // Always create the target type

		// Try asserting to the expected map[string]interface{} first
		if mapString, ok := value.Interface().(map[string]interface{}); ok {
			for k, v := range mapString {
				cleanedValue := removeEmptyFields(v)
				if cleanedValue != nil {
					cleanedMap[k] = cleanedValue
				} else if strVal, ok := v.(string); ok && strVal == "" {
					cleanedMap[k] = "" // Keep intentional empty strings
				}
			}
		} else if mapInterface, ok := value.Interface().(map[interface{}]interface{}); ok {
			// Handle the map[interface{}]interface{} case from yaml.v2 decoding
			for k, v := range mapInterface {
				// Attempt to convert key to string
				stringKey, keyIsString := k.(string)
				if !keyIsString {
					// Log or handle non-string keys if necessary. For K8s YAML, keys
					// should generally be strings. Skipping non-string keys is usually safe.
					log.Printf("Warning: removeEmptyFields encountered non-string key in map: %v (%T)", k, k)
					continue // Skip this key-value pair
				}

				cleanedValue := removeEmptyFields(v)
				if cleanedValue != nil {
					cleanedMap[stringKey] = cleanedValue
				} else if strVal, ok := v.(string); ok && strVal == "" {
					cleanedMap[stringKey] = "" // Keep intentional empty strings
				}
			}
		} else {
			// This case should ideally not be hit if input is valid YAML decoded by yaml.v2
			log.Printf("Warning: removeEmptyFields encountered unexpected map type: %T", data)
			return data // Return original data if type is unexpected
		}

		// Check if the cleaned map is empty
		if len(cleanedMap) == 0 {
			return nil // Return nil if the map becomes empty
		}
		return cleanedMap

	case reflect.Slice:
		// Handle nil slice explicitly
		if value.IsNil() {
			return nil
		}
		// Check if slice is empty first
		if value.Len() == 0 {
			return nil
		}

		// Try asserting to []interface{}
		if sliceValue, ok := value.Interface().([]interface{}); ok {
			cleanedSlice := make([]interface{}, 0, len(sliceValue))
			for _, item := range sliceValue {
				cleanedItem := removeEmptyFields(item)
				if cleanedItem != nil {
					cleanedSlice = append(cleanedSlice, cleanedItem)
				}
			}
			if len(cleanedSlice) == 0 {
				return nil // Return nil if the slice becomes empty
			}
			return cleanedSlice
		} else {
			// Handle slices of other types (e.g., []string) - return as is if not empty
			log.Printf("Warning: removeEmptyFields encountered non []interface{} slice: %T. Returning original.", data)
			return data // Return original non-empty slice of other types
		}

	case reflect.Ptr, reflect.Interface:
		if value.IsNil() {
			return nil
		}
		// Recurse on the element pointed to or contained within the interface
		// Check if the element itself is valid before getting Interface()
		elem := value.Elem()
		if !elem.IsValid() {
			return nil
		}
		return removeEmptyFields(elem.Interface())

	default:
		// Keep primitive types and non-empty strings
		return data
	}
}

// Helper function to apply removeEmptyFields to the top-level KubernetesObject fields
// Handles potential nil maps after cleaning.
func cleanupEmptyTopLevelFields(obj *KubernetesObject) {
	cleanedMetadata := removeEmptyFields(obj.Metadata)
	if cleanedMetadata == nil {
		obj.Metadata = nil
	} else if md, ok := cleanedMetadata.(map[string]interface{}); ok {
		obj.Metadata = md
	} // else: keep original if type assertion fails (shouldn't happen with correct input)

	cleanedSpec := removeEmptyFields(obj.Spec)
	if cleanedSpec == nil {
		obj.Spec = nil
	} else if sp, ok := cleanedSpec.(map[string]interface{}); ok {
		obj.Spec = sp
	}

	cleanedStatus := removeEmptyFields(obj.Status)
	if cleanedStatus == nil {
		obj.Status = nil
	} else if st, ok := cleanedStatus.(map[string]interface{}); ok {
		obj.Status = st
	}

	cleanedData := removeEmptyFields(obj.Data)
	if cleanedData == nil {
		obj.Data = nil
	} else if d, ok := cleanedData.(map[string]interface{}); ok {
		obj.Data = d
	}

	cleanedStringData := removeEmptyFields(obj.StringData)
	if cleanedStringData == nil {
		obj.StringData = nil
	} else if sd, ok := cleanedStringData.(map[string]interface{}); ok {
		obj.StringData = sd
	}
	// Type is a string, handled by default case in removeEmptyFields if needed elsewhere
}

// DeploymentCleaner cleans Deployment-specific fields.
type DeploymentCleaner struct {
	genericCleaner ObjectCleaner // Use interface type
}

func (c *DeploymentCleaner) Clean(obj *KubernetesObject, options *CleanupOptions) {
	c.genericCleaner.Clean(obj, options) // Clean generic fields first

	if obj.Spec != nil {
		// Remove fields not typically needed for desired state definition
		// State preservation logic in genericCleaner handles replicas/strategy/template if enabled
		if !(options.PreserveResourceState && options.ResourceStateMode == "Runtime") {
			// Only remove these if not preserving runtime state
			delete(obj.Spec, "revisionHistoryLimit")
			delete(obj.Spec, "progressDeadlineSeconds")
			// selector is often desired state, keep it unless preserving runtime
		}

		if template, ok := obj.Spec["template"].(map[string]interface{}); ok {
			// Clean metadata within the template
			if templateMeta, ok := template["metadata"].(map[string]interface{}); ok {
				// Remove runtime fields specifically from template metadata
				delete(templateMeta, "creationTimestamp")
				// Clean labels/annotations within template metadata if needed (optional)
				// cleanAnnotations(templateMeta["annotations"]...)
				// cleanLabels(templateMeta["labels"]...)

				// Remove template metadata only if it becomes completely empty after cleaning
				cleanedTemplateMeta := removeEmptyFields(templateMeta)
				if cleanedTemplateMeta == nil {
					delete(template, "metadata")
				} else if tm, ok := cleanedTemplateMeta.(map[string]interface{}); ok {
					template["metadata"] = tm // Update with cleaned map
				}
			}
			// Clean the pod spec within the template
			if spec, ok := template["spec"].(map[string]interface{}); ok {
				cleanPodSpec(spec, options)
				// Remove template spec only if it becomes completely empty
				cleanedSpec := removeEmptyFields(spec)
				if cleanedSpec == nil {
					delete(template, "spec") // Should not happen for valid template
				} else if sp, ok := cleanedSpec.(map[string]interface{}); ok {
					template["spec"] = sp // Update with cleaned map
				}
			}
		}
	}
	// Final cleanup of empty fields potentially left by specific cleaner
	if options.RemoveEmpty {
		cleanupEmptyTopLevelFields(obj)
	}
}

// ServiceCleaner cleans Service-specific fields.
type ServiceCleaner struct {
	genericCleaner ObjectCleaner // Use interface type
}

func (c *ServiceCleaner) Clean(obj *KubernetesObject, options *CleanupOptions) {
	c.genericCleaner.Clean(obj, options)

	if obj.Spec != nil {
		// Remove fields not typically needed for desired state definition
		// State preservation logic handles clusterIP/selector if enabled
		if !(options.PreserveResourceState && options.ResourceStateMode == "Runtime") {
			// Only remove selector if not preserving runtime state
			// delete(obj.Spec, "selector") // Selector is usually desired state
		}
		if !(options.PreserveResourceState && options.ResourceStateMode == "Desired") {
			// Only remove clusterIP(s) if not preserving desired state (they are runtime)
			delete(obj.Spec, "clusterIP")
			delete(obj.Spec, "clusterIPs")
			delete(obj.Spec, "ipFamilies")            // Runtime assigned
			delete(obj.Spec, "ipFamilyPolicy")        // Runtime assigned
			delete(obj.Spec, "internalTrafficPolicy") // Often defaulted/runtime
		}

		// Clean ports: Remove default protocol TCP
		if ports, ok := obj.Spec["ports"].([]interface{}); ok {
			cleanedPorts := make([]interface{}, 0, len(ports))
			for _, p := range ports {
				if portMap, ok := p.(map[string]interface{}); ok {
					if proto, exists := portMap["protocol"]; exists {
						if protoStr, ok := proto.(string); ok && strings.ToUpper(protoStr) == "TCP" {
							delete(portMap, "protocol") // Remove default protocol
						}
					}
					// Keep port even if protocol was removed, unless port itself is empty
					if len(portMap) > 0 {
						cleanedPorts = append(cleanedPorts, portMap)
					}
				} else {
					cleanedPorts = append(cleanedPorts, p) // Keep non-map items if any
				}
			}
			if len(cleanedPorts) > 0 {
				obj.Spec["ports"] = cleanedPorts
			} else {
				delete(obj.Spec, "ports") // Remove if ports list becomes empty
			}
		}
	}
	// Final cleanup of empty fields
	if options.RemoveEmpty {
		cleanupEmptyTopLevelFields(obj)
	}
}

// StatefulSetCleaner cleans StatefulSet-specific fields.
type StatefulSetCleaner struct {
	genericCleaner ObjectCleaner // Use interface type
}

func (c *StatefulSetCleaner) Clean(obj *KubernetesObject, options *CleanupOptions) {
	c.genericCleaner.Clean(obj, options)

	if obj.Spec != nil {
		// Remove fields not typically needed for desired state definition
		// State preservation handles replicas/updateStrategy/template
		if !(options.PreserveResourceState && options.ResourceStateMode == "Runtime") {
			delete(obj.Spec, "revisionHistoryLimit")
			// selector is desired state
		}

		if template, ok := obj.Spec["template"].(map[string]interface{}); ok {
			if templateMeta, ok := template["metadata"].(map[string]interface{}); ok {
				delete(templateMeta, "creationTimestamp")
				cleanedTemplateMeta := removeEmptyFields(templateMeta)
				if cleanedTemplateMeta == nil {
					delete(template, "metadata")
				} else if tm, ok := cleanedTemplateMeta.(map[string]interface{}); ok {
					template["metadata"] = tm
				}
			}
			if spec, ok := template["spec"].(map[string]interface{}); ok {
				cleanPodSpec(spec, options)
				cleanedSpec := removeEmptyFields(spec)
				if cleanedSpec == nil {
					delete(template, "spec")
				} else if sp, ok := cleanedSpec.(map[string]interface{}); ok {
					template["spec"] = sp
				}
			}
		}
	}
	// Final cleanup of empty fields
	if options.RemoveEmpty {
		cleanupEmptyTopLevelFields(obj)
	}
}

// DaemonSetCleaner cleans DaemonSet-specific fields.
type DaemonSetCleaner struct {
	genericCleaner ObjectCleaner // Use interface type
}

func (c *DaemonSetCleaner) Clean(obj *KubernetesObject, options *CleanupOptions) {
	c.genericCleaner.Clean(obj, options)
	if obj.Spec != nil {
		// Remove fields not typically needed for desired state definition
		// State preservation handles updateStrategy/template
		if !(options.PreserveResourceState && options.ResourceStateMode == "Runtime") {
			delete(obj.Spec, "revisionHistoryLimit")
			// selector is desired state
		}

		if template, ok := obj.Spec["template"].(map[string]interface{}); ok {
			if templateMeta, ok := template["metadata"].(map[string]interface{}); ok {
				delete(templateMeta, "creationTimestamp")
				cleanedTemplateMeta := removeEmptyFields(templateMeta)
				if cleanedTemplateMeta == nil {
					delete(template, "metadata")
				} else if tm, ok := cleanedTemplateMeta.(map[string]interface{}); ok {
					template["metadata"] = tm
				}
			}
			if spec, ok := template["spec"].(map[string]interface{}); ok {
				cleanPodSpec(spec, options)
				cleanedSpec := removeEmptyFields(spec)
				if cleanedSpec == nil {
					delete(template, "spec")
				} else if sp, ok := cleanedSpec.(map[string]interface{}); ok {
					template["spec"] = sp
				}
			}
		}
	}
	// Final cleanup of empty fields
	if options.RemoveEmpty {
		cleanupEmptyTopLevelFields(obj)
	}
}

// PodCleaner cleans Pod-specific fields.
type PodCleaner struct {
	genericCleaner ObjectCleaner // Use interface type
}

func (c *PodCleaner) Clean(obj *KubernetesObject, options *CleanupOptions) {
	// Attempt revert *before* generic cleaning, as generic cleaning might remove labels needed for revert
	if options.RevertToDeployment {
		reverted := revertPodToDeployment(obj) // revertPodToDeployment now returns bool
		if reverted {
			// If reverted, get the Deployment cleaner and clean *that* object instead
			// This assumes the factory is accessible or passed down. For simplicity here,
			// we'll just re-apply generic cleaning. A better approach might involve
			// the factory pattern more deeply.
			log.Println("Reverted Pod to Deployment, re-applying generic cleaning")
			// Re-apply generic cleaning to the *new* Deployment object structure
			c.genericCleaner.Clean(obj, options)

			// Specifically clean the pod spec *within* the new template
			if obj.Spec != nil {
				if template, ok := obj.Spec["template"].(map[string]interface{}); ok {
					if spec, ok := template["spec"].(map[string]interface{}); ok {
						cleanPodSpec(spec, options) // Clean the spec moved into the template
						cleanedSpec := removeEmptyFields(spec)
						if cleanedSpec == nil {
							delete(template, "spec")
						} else if sp, ok := cleanedSpec.(map[string]interface{}); ok {
							template["spec"] = sp
						}
					}
				}
			}
			// Final cleanup of empty fields for the Deployment
			if options.RemoveEmpty {
				cleanupEmptyTopLevelFields(obj)
			}
			return // Stop processing as a Pod
		}
	}

	// If not reverted, proceed with standard Pod cleaning
	c.genericCleaner.Clean(obj, options)
	if obj.Spec != nil {
		cleanPodSpec(obj.Spec, options)
		// Clean the top-level spec itself if it becomes empty
		cleanedSpec := removeEmptyFields(obj.Spec)
		if cleanedSpec == nil {
			obj.Spec = nil
		} else if sp, ok := cleanedSpec.(map[string]interface{}); ok {
			obj.Spec = sp
		}
	}
	// Final cleanup of empty fields for the Pod
	if options.RemoveEmpty {
		cleanupEmptyTopLevelFields(obj)
	}
}

// ConfigMapCleaner cleans ConfigMap-specific fields
type ConfigMapCleaner struct {
	genericCleaner ObjectCleaner // Use interface type
}

func (c *ConfigMapCleaner) Clean(obj *KubernetesObject, options *CleanupOptions) {
	c.genericCleaner.Clean(obj, options)
	if obj.Data != nil {
		cleanConfigMapData(obj.Data)
		if len(obj.Data) == 0 {
			obj.Data = nil // Remove data field if empty
		}
	}
	// Final cleanup of empty fields
	if options.RemoveEmpty {
		cleanupEmptyTopLevelFields(obj)
	}
}

// cleanConfigMapData removes specific noisy keys often found in ConfigMaps
func cleanConfigMapData(data map[string]interface{}) {
	keysToDelete := []string{}
	for key := range data {
		// Remove keys commonly holding last applied configuration or similar metadata
		if key == "kubectl.kubernetes.io/last-applied-configuration" {
			keysToDelete = append(keysToDelete, key)
			continue
		}
		// Example: Remove ca.crt if it's the only key and likely from service account? (Maybe too specific)
		// if key == "ca.crt" && len(data) == 1 { ... }
	}
	for _, key := range keysToDelete {
		delete(data, key)
	}
}

// SecretCleaner cleans Secret-specific fields.
type SecretCleaner struct {
	genericCleaner ObjectCleaner // Use interface type
}

func (c *SecretCleaner) Clean(obj *KubernetesObject, options *CleanupOptions) {
	c.genericCleaner.Clean(obj, options)
	// Secrets often contain service account tokens or docker config generated at runtime.
	// We might want to remove specific types or data keys.

	// Get name safely
	var secretName string
	if name, ok := obj.Metadata["name"].(string); ok {
		secretName = name
	}

	// Remove common runtime-generated secrets entirely? (Potentially dangerous, make optional?)
	// Example: Remove default service account tokens
	if obj.Type == "kubernetes.io/service-account-token" && strings.HasPrefix(secretName, "default-token-") {
		log.Printf("Note: Secret '%s' looks like a default service account token. Consider removing manually if not needed.", secretName)
		// To actually remove: obj.Data = nil; obj.StringData = nil; obj.Type = ""
		// Or maybe set a flag to skip encoding this object entirely?
	}

	// Example: Clean docker config secrets?
	if obj.Type == "kubernetes.io/dockerconfigjson" {
		// Maybe remove specific keys from .dockerconfigjson if needed?
	}

	// Clean potentially empty data/stringData after generic cleaning
	if obj.Data != nil && len(obj.Data) == 0 {
		obj.Data = nil
	}
	if obj.StringData != nil && len(obj.StringData) == 0 {
		obj.StringData = nil
	}
	// Final cleanup of empty fields
	if options.RemoveEmpty {
		cleanupEmptyTopLevelFields(obj)
	}
}

// cleanPodSpec removes fields from Pod specs (used for Pods and templates).
func cleanPodSpec(spec map[string]interface{}, options *CleanupOptions) {
	if spec == nil {
		return
	}
	// Fields typically representing runtime state or scheduler decisions
	fieldsToRemove := []string{
		"nodeName",
		// "serviceAccountName", // Often desired state
		// "serviceAccount", // Older field, less common
		// "automountServiceAccountToken", // Can be desired state
		"dnsPolicy", // Often defaulted
		// "nodeSelector", // Often desired state
		// "tolerations", // Often desired state
		// "affinity", // Often desired state
		"schedulerName",
		// "priorityClassName", // Often desired state
		// "priority", // Often desired state
		"enableServiceLinks", // Often defaulted
		"preemptionPolicy",
		// "restartPolicy", // Usually implied by controller
		"terminationGracePeriodSeconds", // Often defaulted
		"hostIP",                        // Runtime
		"podIP",                         // Runtime
		"podIPs",                        // Runtime
		"hostPID",                       // Runtime/Security Context related
		"hostNetwork",                   // Runtime/Security Context related
		"hostIPC",                       // Runtime/Security Context related
		"hostname",                      // Runtime/Set by system
		"subdomain",                     // Runtime/Set by system
		"shareProcessNamespace",         // Runtime/Security Context related
		"runtimeClassName",              // Runtime/Node specific
		"readinessGates",                // Often status related
		// "topologySpreadConstraints", // Often desired state
		"setHostnameAsFQDN", // Often defaulted
	}

	// Conditionally remove based on state preservation
	if options.PreserveResourceState {
		podStateFields, podStateOk := resourceStateFields["Pod"]
		tempRemoveList := []string{}
		for _, field := range fieldsToRemove {
			fieldPath := "spec." + field // Construct path for lookup

			remove := true // Default to removing these runtime/defaulted fields

			if podStateOk {
				isDesired, exists := podStateFields[fieldPath]
				if exists { // If defined in state map
					if options.ResourceStateMode == "Desired" && isDesired {
						remove = false // Keep desired field when preserving desired
					} else if options.ResourceStateMode == "Runtime" && !isDesired {
						remove = false // Keep runtime field when preserving runtime
					}
					// If field exists in map but doesn't match preservation mode, 'remove' remains true
				} else {
					// If not in state map, assume runtime/defaulted.
					// Keep it only if preserving runtime state.
					if options.ResourceStateMode == "Runtime" {
						remove = false
					}
				}
			} else {
				// If "Pod" kind is missing from state map entirely,
				// fall back to default behavior: remove if preserving desired, keep if preserving runtime.
				if options.ResourceStateMode == "Runtime" {
					remove = false
				}
			}

			if remove {
				tempRemoveList = append(tempRemoveList, field)
			}
		}
		fieldsToRemove = tempRemoveList
	}

	for _, field := range fieldsToRemove {
		delete(spec, field)
	}

	// Clean containers and initContainers
	for _, containerType := range []string{"containers", "initContainers"} {
		if containers, ok := spec[containerType].([]interface{}); ok {
			cleanedContainers := make([]interface{}, 0, len(containers))
			for _, container := range containers {
				if containerMap, ok := container.(map[string]interface{}); ok {
					cleanContainerSpec(containerMap, options)
					// Keep container even if empty after cleaning? Usually name/image remain.
					// Only discard if the map becomes truly empty (unlikely for valid container)
					if len(containerMap) > 0 {
						cleanedContainers = append(cleanedContainers, containerMap)
					}
				} else {
					cleanedContainers = append(cleanedContainers, container) // Keep non-map items
				}
			}
			if len(cleanedContainers) > 0 {
				spec[containerType] = cleanedContainers
			} else {
				delete(spec, containerType) // Remove if list becomes empty
			}
		}
	}

	// Clean volumes and associated volumeMounts (modifies spec in place)
	cleanPodVolumes(spec)

	// Remove empty volumes list if necessary (after cleanPodVolumes)
	if volumes, ok := spec["volumes"].([]interface{}); ok && len(volumes) == 0 {
		delete(spec, "volumes")
	}
}

// cleanContainerSpec removes fields from container specs.
func cleanContainerSpec(container map[string]interface{}, options *CleanupOptions) {
	if container == nil {
		return
	}
	// Fields typically representing runtime state, defaults, or status probes
	fieldsToRemove := []string{
		"terminationMessagePath",   // Defaulted
		"terminationMessagePolicy", // Defaulted
		"imagePullPolicy",          // Defaulted or runtime decision
		// "securityContext",       // Often desired state
		// "livenessProbe",         // Often desired state
		// "readinessProbe",        // Often desired state
		// "startupProbe",          // Often desired state
		// "resources",             // Often desired state (requests/limits)
		"tty",       // Runtime interaction hint
		"stdin",     // Runtime interaction hint
		"stdinOnce", // Runtime interaction hint
	}
	// Note: We generally KEEP 'name', 'image', 'command', 'args', 'ports', 'env', 'envFrom', 'volumeMounts' as core desired state.

	for _, field := range fieldsToRemove {
		delete(container, field)
	}

	// Clean ports: Remove default protocol TCP
	if ports, ok := container["ports"].([]interface{}); ok {
		cleanedPorts := make([]interface{}, 0, len(ports))
		for _, p := range ports {
			if portMap, ok := p.(map[string]interface{}); ok {
				if proto, exists := portMap["protocol"]; exists {
					if protoStr, ok := proto.(string); ok && strings.ToUpper(protoStr) == "TCP" {
						delete(portMap, "protocol") // Remove default protocol
					}
				}
				// Keep port even if protocol was removed, unless port itself is empty
				if len(portMap) > 0 {
					cleanedPorts = append(cleanedPorts, portMap)
				}
			} else {
				cleanedPorts = append(cleanedPorts, p) // Keep non-map items
			}
		}
		if len(cleanedPorts) > 0 {
			container["ports"] = cleanedPorts
		} else {
			delete(container, "ports") // Remove if ports list becomes empty
		}
	}

	// Clean volumeMounts (handled by cleanPodVolumes called from cleanPodSpec)
}

// cleanPodVolumes removes kube-api-access volumes and related volumeMounts
func cleanPodVolumes(spec map[string]interface{}) {
	if spec == nil {
		return
	}
	volumesToRemove := map[string]bool{}

	// Identify volumes to remove (e.g., kube-api-access, projected service account tokens)
	if volumes, ok := spec["volumes"].([]interface{}); ok {
		cleanedVolumes := make([]interface{}, 0, len(volumes))
		for _, volume := range volumes {
			shouldKeep := true
			if volumeMap, ok := volume.(map[string]interface{}); ok {
				// Check name for kube-api-access prefix
				if name, exists := volumeMap["name"].(string); exists && strings.HasPrefix(name, "kube-api-access-") {
					volumesToRemove[name] = true // Mark for removal
					shouldKeep = false
				}
				// Check for projected service account token volumes (often runtime)
				if projected, projOk := volumeMap["projected"].(map[string]interface{}); projOk {
					if sources, sourcesOk := projected["sources"].([]interface{}); sourcesOk {
						isServiceAccountToken := false
						for _, source := range sources {
							if sourceMap, sourceMapOk := source.(map[string]interface{}); sourceMapOk {
								if _, satOk := sourceMap["serviceAccountToken"]; satOk {
									isServiceAccountToken = true
									break
								}
							}
						}
						if isServiceAccountToken {
							// Also mark projected service account token volumes for removal
							if name, exists := volumeMap["name"].(string); exists {
								volumesToRemove[name] = true
								shouldKeep = false
							}
						}
					}
				}
			}
			// Keep the volume if it wasn't marked for removal
			if shouldKeep {
				cleanedVolumes = append(cleanedVolumes, volume)
			}
		}
		// Update spec with the cleaned list or remove if empty
		if len(cleanedVolumes) > 0 {
			spec["volumes"] = cleanedVolumes
		} else {
			delete(spec, "volumes") // Remove if list becomes empty
		}
	}

	// If no volumes are left to remove, no need to check volumeMounts
	if len(volumesToRemove) == 0 {
		return
	}

	// Clean volumeMounts in containers and initContainers referencing removed volumes
	for _, containerType := range []string{"containers", "initContainers"} {
		if containers, ok := spec[containerType].([]interface{}); ok {
			for _, container := range containers {
				if containerMap, ok := container.(map[string]interface{}); ok {
					if volumeMounts, exists := containerMap["volumeMounts"].([]interface{}); exists {
						cleanedVolumeMounts := make([]interface{}, 0, len(volumeMounts))
						for _, vm := range volumeMounts {
							shouldKeepMount := true
							if vmMap, ok := vm.(map[string]interface{}); ok {
								if name, nameExists := vmMap["name"].(string); nameExists {
									if volumesToRemove[name] { // Check if this mount references a removed volume
										shouldKeepMount = false
									}
								}
							}
							if shouldKeepMount {
								cleanedVolumeMounts = append(cleanedVolumeMounts, vm)
							}
						}
						// Update or remove volumeMounts list in the container
						if len(cleanedVolumeMounts) > 0 {
							containerMap["volumeMounts"] = cleanedVolumeMounts
						} else {
							delete(containerMap, "volumeMounts")
						}
					}
				}
			}
		}
	}
}

// revertPodToDeployment attempts to reconstruct a Deployment from a Pod. Returns true if successful.
func revertPodToDeployment(obj *KubernetesObject) bool {
	if obj == nil || obj.Kind != "Pod" || obj.Metadata == nil {
		return false // Only process valid Pods
	}

	// Check for the presence of a pod-template-hash label.
	podLabels, labelsOk := obj.Metadata["labels"].(map[string]interface{})
	if !labelsOk {
		log.Printf("Skipping Pod revert for '%s': No labels found.", obj.Metadata["name"])
		return false // No labels found
	}

	hashValue, hasHash := podLabels["pod-template-hash"]
	if !hasHash {
		log.Printf("Skipping Pod revert for '%s': Missing 'pod-template-hash' label.", obj.Metadata["name"])
		return false // Not controlled by a standard controller using this label
	}
	hashStr, hashOk := hashValue.(string)
	if !hashOk || hashStr == "" {
		log.Printf("Skipping Pod revert for '%s': Invalid 'pod-template-hash' label value.", obj.Metadata["name"])
		return false // Invalid hash label value
	}

	// --- Construct Deployment ---
	log.Printf("Attempting to revert Pod '%s' to Deployment based on pod-template-hash '%s'", obj.Metadata["name"], hashStr)

	// Preserve original metadata fields selectively
	originalName := obj.Metadata["name"] // Might need adjustment (e.g., remove hash suffix)
	originalNamespace := obj.Metadata["namespace"]

	// Attempt to derive a base name for the Deployment
	deploymentName := fmt.Sprintf("%s-reverted", originalName) // Default name
	if baseName, ok := deriveBaseName(originalName.(string), hashStr); ok {
		deploymentName = baseName
	} else {
		log.Printf("Warning: Could not derive base name for Deployment from Pod name '%s'. Using default.", originalName)
	}

	// Copy all original labels for the deployment itself, EXCLUDING pod-template-hash
	deploymentLabels := make(map[string]interface{})
	for k, v := range podLabels {
		if k != "pod-template-hash" {
			deploymentLabels[k] = v
		}
	}
	// If no labels remain, maybe add a default one?
	if len(deploymentLabels) == 0 {
		deploymentLabels["app"] = deploymentName // Example default label
	}

	// Template labels should match deployment labels (or be derived appropriately)
	templateLabels := make(map[string]interface{})
	for k, v := range deploymentLabels {
		templateLabels[k] = v
	}
	// Add the pod-template-hash back to the *template* labels if desired?
	// Usually selector matches template labels, so maybe not needed here.

	// Create the Deployment structure
	obj.APIVersion = "apps/v1"
	obj.Kind = "Deployment"

	// Reset Metadata, keeping essential parts
	obj.Metadata = map[string]interface{}{
		"name":   deploymentName,
		"labels": deploymentLabels,
	}
	if originalNamespace != nil {
		obj.Metadata["namespace"] = originalNamespace
	}
	// Remove pod-specific metadata fields that don't apply to Deployments
	delete(obj.Metadata, "generateName")
	// Keep annotations? Maybe clean them separately.

	// Preserve original Pod Spec
	originalPodSpec := obj.Spec // Keep a reference before overwriting obj.Spec

	// Create Deployment Spec
	obj.Spec = map[string]interface{}{
		"replicas": 1, // Default to 1 replica
		"selector": map[string]interface{}{
			// Selector should match the labels applied to the *template*
			"matchLabels": templateLabels,
		},
		"template": map[string]interface{}{
			"metadata": map[string]interface{}{
				"labels": templateLabels, // Apply derived labels to template
			},
			"spec": originalPodSpec, // Move the original Pod's spec here
		},
		// Add default strategy?
		// "strategy": map[string]interface{}{"type": "RollingUpdate", ...},
	}

	// Clear Status and other Pod-specific top-level fields
	obj.Status = nil
	obj.Data = nil
	obj.StringData = nil
	obj.Type = ""

	log.Printf("Successfully reverted Pod '%s' to Deployment structure named '%s'", originalName, deploymentName)
	return true
}

// deriveBaseName attempts to remove common controller hash suffixes from a pod name.
func deriveBaseName(podName, hash string) (string, bool) {
	// Common pattern: deployment-name-<pod-template-hash>-<random-suffix>
	// Simpler pattern: statefulset-name-<ordinal>
	// Simpler pattern: replicaset-name-<random-suffix> (hash is on RS, not pod name directly)

	// Try removing -<hash>-<suffix>
	hashSuffixPattern := "-" + hash + "-"
	if index := strings.LastIndex(podName, hashSuffixPattern); index != -1 {
		return podName[:index], true
	}

	// Try removing -<hash> (less common for pods, maybe ReplicaSet name?)
	hashSuffix := "-" + hash
	if strings.HasSuffix(podName, hashSuffix) {
		return strings.TrimSuffix(podName, hashSuffix), true
	}

	// Add more sophisticated logic if needed, e.g., checking ownerReferences if available before cleaning
	return "", false // Could not determine base name reliably
}

// ObjectCleanerFactory maps kinds to cleaners.
type ObjectCleanerFactory struct {
	cleaners map[string]ObjectCleaner
}

// GetCleaner returns the appropriate cleaner for the given kind.
func (f *ObjectCleanerFactory) GetCleaner(kind string) ObjectCleaner {
	if kind == "" {
		log.Println("Warning: GetCleaner called with empty kind, returning Generic cleaner")
		return f.cleaners["Generic"]
	}
	cleaner, ok := f.cleaners[kind]
	if !ok {
		log.Printf("Warning: No specific cleaner found for kind '%s', using Generic cleaner", kind)
		return f.cleaners["Generic"] // Default to the generic cleaner.
	}
	return cleaner
}

// NewObjectCleanerFactory creates a new ObjectCleanerFactory.
func NewObjectCleanerFactory() *ObjectCleanerFactory {
	// Create the generic cleaner first
	genericMetaCleaner := &GenericMetadataCleaner{}
	genericObjCleaner := &GenericObjectCleaner{metadataCleaner: genericMetaCleaner}

	// Create specific cleaners, injecting the generic one
	factory := &ObjectCleanerFactory{
		cleaners: map[string]ObjectCleaner{
			"Generic":     genericObjCleaner, // Register the generic cleaner itself
			"Deployment":  &DeploymentCleaner{genericCleaner: genericObjCleaner},
			"Service":     &ServiceCleaner{genericCleaner: genericObjCleaner},
			"StatefulSet": &StatefulSetCleaner{genericCleaner: genericObjCleaner},
			"DaemonSet":   &DaemonSetCleaner{genericCleaner: genericObjCleaner},
			"Pod":         &PodCleaner{genericCleaner: genericObjCleaner},
			"ConfigMap":   &ConfigMapCleaner{genericCleaner: genericObjCleaner},
			"Secret":      &SecretCleaner{genericCleaner: genericObjCleaner},
			// Add more cleaners for other kinds as needed.
			// Example: "ReplicaSet": &ReplicaSetCleaner{genericCleaner: genericObjCleaner},
		},
	}
	return factory
}

// cleanupKubernetesObject cleans a Kubernetes object based on its kind.
func cleanupKubernetesObject(obj *KubernetesObject, options *CleanupOptions, cleanerFactory *ObjectCleanerFactory) {
	if obj == nil || obj.Kind == "" {
		log.Println("Skipping cleanup for object with missing Kind")
		return // Cannot determine cleaner without Kind
	}

	cleaner := cleanerFactory.GetCleaner(obj.Kind)
	// Cleaner factory now guarantees a non-nil cleaner (returns Generic if specific not found)
	cleaner.Clean(obj, options)

	// The removeEmptyFields logic is now integrated into the cleaners or called at the end.
}

// cleanupManifest processes the input YAML, cleans each object, and writes the cleaned YAML to the output.
func cleanupManifest(input io.Reader, output io.Writer, options *CleanupOptions) error {
	reader := bufio.NewReader(input)
	decoder := yaml.NewDecoder(reader)
	encoder := yaml.NewEncoder(output)
	// encoder.SetIndent(2) // <-- REMOVED: SetIndent is not available in yaml.v2
	defer encoder.Close()

	documentCount := 0
	cleanerFactory := NewObjectCleanerFactory()

	for {
		var obj KubernetesObject
		// Use Decode directly into the struct
		err := decoder.Decode(&obj)

		if err == io.EOF {
			if documentCount == 0 {
				// Allow empty input without error, just produce no output
				log.Println("Input contained no YAML documents.")
				return nil // Changed from error to nil for empty input case
			}
			break // End of input stream
		}
		if err != nil {
			// var genericDoc interface{} // <-- REMOVED: Variable declared but not used
			// Attempt to provide more context on the decoding error.
			// Reading the raw segment that failed might be complex with bufio.Reader.
			// For now, just report the error.
			return fmt.Errorf("error decoding YAML document %d: %w. Check YAML syntax near this document", documentCount+1, err)
		}

		documentCount++

		// Basic validation: Check if it looks like a K8s object
		if obj.Kind == "" && obj.APIVersion == "" {
			// It might be a comment block, an empty document (---), or non-K8s YAML.
			// We choose to skip these silently for now.
			log.Printf("Skipping document %d: Missing Kind and APIVersion.", documentCount)
			continue
		}
		if obj.Kind == "" {
			log.Printf("Skipping document %d: Missing Kind (APIVersion: %s).", documentCount, obj.APIVersion)
			continue
		}
		if obj.APIVersion == "" {
			log.Printf("Skipping document %d: Missing APIVersion (Kind: %s).", documentCount, obj.Kind)
			continue
		}

		// Attempt to get name for logging, handle potential nil metadata or missing name gracefully
		var objName interface{} = "<unknown>" // Default name
		if obj.Metadata != nil {
			if name, ok := obj.Metadata["name"]; ok {
				objName = name
			}
		}
		log.Printf("Processing document %d: %s/%s (%v)", documentCount, obj.APIVersion, obj.Kind, objName)

		cleanupKubernetesObject(&obj, options, cleanerFactory)

		// Check if the object became "empty" after cleaning (e.g., only apiVersion/kind left)
		// This might happen if a runtime object was aggressively cleaned.
		// We still encode it, as apiVersion/kind might be useful context.
		// If obj.Metadata == nil && obj.Spec == nil && obj.Status == nil && obj.Data == nil && obj.StringData == nil {
		//  log.Printf("Note: Document %d (%s/%s %v) is effectively empty after cleaning.", documentCount, obj.APIVersion, obj.Kind, objName)
		// }

		// Encode the cleaned object
		err = encoder.Encode(obj)
		if err != nil {
			// This error is less likely but possible (e.g., IO error on output)
			return fmt.Errorf("error encoding cleaned YAML document %d (%s/%s %v): %w", documentCount, obj.APIVersion, obj.Kind, objName, err)
		}
	}

	log.Printf("Successfully processed %d YAML documents.", documentCount)
	return nil
}

func main() {
	// Default options (can be overridden by flags later)
	options := &CleanupOptions{
		RemoveManagedFields:   true,       // Remove kubectl internal annotations, etc.
		RemoveStatus:          true,       // Remove runtime status block
		RemoveNamespace:       true,       // Make objects namespace-agnostic
		RemoveClusterName:     false,      // Placeholder, not implemented
		RemoveLabels:          []string{}, // No specific labels to remove by default
		RemoveAnnotations:     []string{}, // No specific annotations to remove by default
		RemoveEmpty:           true,       // Clean up empty maps/slices at the end
		CleanupFinalizers:     true,       // Remove finalizers
		RevertToDeployment:    true,       // Try to revert ownerless Pods to Deployments
		PreserveResourceState: false,      // Default: Don't preserve specific state, clean generally
		ResourceStateMode:     "Desired",  // Default mode if PreserveResourceState is true
	}

	// Setup logging
	log.SetOutput(os.Stderr) // Log to stderr
	log.SetPrefix("[Kleanup] ")
	// log.SetFlags(log.Ldate | log.Ltime | log.Lshortfile) // Keep it simple for CLI tool
	log.SetFlags(log.Ltime)

	// TODO: Add flag parsing here to override default options
	// Example using 'flag' package:
	// flag.BoolVar(&options.RemoveManagedFields, "remove-managed-fields", true, "Remove metadata.managedFields")
	// flag.BoolVar(&options.RemoveStatus, "remove-status", true, "Remove status block")
	// flag.BoolVar(&options.RemoveNamespace, "remove-namespace", true, "Remove metadata.namespace")
	// flag.BoolVar(&options.RemoveEmpty, "remove-empty", true, "Remove empty fields/maps/slices after cleaning")
	// flag.BoolVar(&options.CleanupFinalizers, "cleanup-finalizers", true, "Remove metadata.finalizers")
	// flag.BoolVar(&options.RevertToDeployment, "revert-pod-to-deployment", true, "Attempt to revert standalone Pods to Deployments")
	// flag.BoolVar(&options.PreserveResourceState, "preserve-state", false, "Preserve specific desired or runtime state fields")
	// flag.StringVar(&options.ResourceStateMode, "state-mode", "Desired", "Mode for state preservation ('Desired' or 'Runtime')")
	// // Add flags for RemoveLabels and RemoveAnnotations (e.g., using a custom flag type for slices)
	// flag.Parse()

	// --- Input/Output Handling ---
	var input io.Reader = os.Stdin
	var output io.Writer = os.Stdout
	var err error

	// Basic argument handling (replace with flag package later)
	// Example: kleanup input.yaml > output.yaml
	// Example: cat input.yaml | kleanup > output.yaml
	// if len(os.Args) > 1 {
	// 	inputFile := os.Args[1]
	// 	if inputFile != "-" { // Allow "-" for stdin explicitly
	// 		file, err := os.Open(inputFile)
	// 		if err != nil {
	// 			fmt.Fprintf(os.Stderr, "Error opening input file '%s': %v\n", inputFile, err)
	// 			os.Exit(1)
	// 		}
	// 		defer file.Close()
	// 		input = file
	// 		log.Printf("Reading from file: %s", inputFile)
	// 	} else {
	//      log.Println("Reading from stdin...")
	//  }
	// } else {
	// 	log.Println("Reading from stdin...")
	// }
	// Add similar logic for output file if needed

	log.Println("Starting cleanup...")
	if err = cleanupManifest(input, output, options); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
	log.Println("Cleanup finished successfully.")
}
