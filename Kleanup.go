package main

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"strings"

	"log"

	"gopkg.in/yaml.v2"
)

// KubernetesObject represents the basic structure of Kubernetes objects.
type KubernetesObject struct {
	APIVersion string                 `yaml:"apiVersion"`
	Kind       string                 `yaml:"kind"`
	Metadata   map[string]interface{} `yaml:"metadata,omitempty"`
	Spec       map[string]interface{} `yaml:"spec,omitempty"`
	Status     map[string]interface{} `yaml:"status,omitempty"`
	Data       map[string]interface{} `yaml:"data,omitempty"` // For ConfigMaps
	Type       string                 `yaml:"type,omitempty"` // e.g., for Secrets
}

// CleanupOptions defines options to customize the cleanup process.
type CleanupOptions struct {
	RemoveManagedFields   bool
	RemoveStatus          bool
	RemoveNamespace       bool
	RemoveClusterName     bool     // Remove cluster name
	RemoveLabels          []string // labels to remove
	RemoveAnnotations     []string // annotations to remove
	RemoveEmpty           bool     // Remove empty fields
	CleanupFinalizers     bool     // Remove finalizers
	RevertToDeployment    bool     // Attempt to reconstruct Deployment
	PreserveResourceState bool     // Keep resource state related fields
	ResourceStateMode     string   // "Desired" or "Runtime" cleanup mode
}

// ResourceState tracks which fields represent desired vs runtime state
var resourceStateFields = map[string]map[string]bool{
	"Deployment": {
		"spec.replicas":       true,  // desired state
		"spec.strategy":       true,  // desired state
		"spec.template":       true,  // desired state
		"status":              false, // runtime state
		"metadata.generation": false, // runtime state
	},
	"Service": {
		"spec.ports":     true,  // desired state
		"spec.selector":  true,  // desired state
		"spec.clusterIP": false, // runtime state
	},
	"Pod": {
		"spec.containers":   true,  // desired state
		"spec.volumes":      true,  // desired state
		"spec.nodeSelector": true,  // desired state
		"status":            false, // runtime state
		"spec.nodeName":     false, // runtime state
	},
}

// MetadataCleaner defines an interface for cleaning object metadata.
type MetadataCleaner interface {
	Clean(metadata map[string]interface{}, options *CleanupOptions)
}

// ObjectCleaner defines an interface for cleaning Kubernetes objects.
type ObjectCleaner interface {
	Clean(obj *KubernetesObject, options *CleanupOptions)
}

// GenericMetadataCleaner cleans common metadata fields.
type GenericMetadataCleaner struct{}

func (c *GenericMetadataCleaner) Clean(metadata map[string]interface{}, options *CleanupOptions) {
	fieldsToRemove := []string{
		"creationTimestamp",
		"generation",
		"resourceVersion",
		"selfLink",
		"uid",
		"ownerReferences",
		"managedFields",
	}
	if !options.RemoveManagedFields {
		fieldsToRemove = []string{
			"creationTimestamp",
			"generation",
			"resourceVersion",
			"selfLink",
			"uid",
			"ownerReferences",
		}
	}
	if !options.CleanupFinalizers {
		fieldsToRemove = append(fieldsToRemove, "finalizers")
	}

	for _, field := range fieldsToRemove {
		delete(metadata, field)
	}

	// Clean annotations
	if annotations, ok := metadata["annotations"].(map[string]interface{}); ok {
		cleanAnnotations(annotations, options.RemoveAnnotations)
	}
	if labels, ok := metadata["labels"].(map[string]interface{}); ok {
		cleanLabels(labels, options.RemoveLabels)
	}

	if options.RemoveNamespace {
		delete(metadata, "namespace")
	}
	if len(metadata) == 0 && options.RemoveEmpty {
		delete(metadata, "metadata")
	}
}

func cleanLabels(labels map[string]interface{}, removeLabels []string) {
	if len(removeLabels) == 0 {
		return
	}
	for key := range labels {
		for _, labelToRemove := range removeLabels {
			if key == labelToRemove {
				delete(labels, key)
				break
			}
		}
	}
}

// cleanAnnotations removes annotations matching specific prefixes and user provided annotations
func cleanAnnotations(annotations map[string]interface{}, removeAnnotations []string) {
	annotationPrefixesToRemove := []string{
		"kubectl.kubernetes.io/",
		"deployment.kubernetes.io/", // This is already here but ensuring it's effective
		"apps.kubernetes.io/",
		"pod-template-hash",
		"statefulset.kubernetes.io/",
		"controller-revision-hash",
		"service.kubernetes.io/",
		"batch.kubernetes.io/",
		"networking.k8s.io/",
		"rbac.authorization.k8s.io/",
		"argocd.argoproj.io/",
		"helm.sh/",
		"meta.helm.sh/",
	}

	for key := range annotations {
		shouldDelete := false
		for _, prefix := range annotationPrefixesToRemove {
			if strings.HasPrefix(key, prefix) {
				shouldDelete = true
				break
			}
		}
		if !shouldDelete { // check user provided annotations to remove
			for _, annotationToRemove := range removeAnnotations {
				if key == annotationToRemove {
					shouldDelete = true
					break
				}
			}
		}

		if shouldDelete {
			delete(annotations, key)
		}
	}
}

// GenericObjectCleaner cleans common object fields.
type GenericObjectCleaner struct {
	metadataCleaner MetadataCleaner
}

func (c *GenericObjectCleaner) Clean(obj *KubernetesObject, options *CleanupOptions) {
	if obj.Metadata != nil {
		c.metadataCleaner.Clean(obj.Metadata, options)
	}

	// Handle state based cleaning
	if options.PreserveResourceState {
		if stateFields, ok := resourceStateFields[obj.Kind]; ok {
			for field, isDesired := range stateFields {
				if options.ResourceStateMode == "Desired" && !isDesired {
					removeField(obj, field)
				} else if options.ResourceStateMode == "Runtime" && isDesired {
					removeField(obj, field)
				}
			}
		}
	}

	if options.RemoveStatus {
		obj.Status = nil
	}
	if options.RemoveClusterName {
		//delete cluster name
	}
	if options.RemoveEmpty {
		removeEmptyFields(obj)
	}
}

// Helper to remove nested fields using dot notation
func removeField(obj *KubernetesObject, field string) {
	parts := strings.Split(field, ".")
	current := make(map[string]interface{})

	switch parts[0] {
	case "spec":
		current = obj.Spec
	case "status":
		current = obj.Status
	case "metadata":
		current = obj.Metadata
	default:
		return
	}

	for i := 1; i < len(parts)-1; i++ {
		if next, ok := current[parts[i]].(map[string]interface{}); ok {
			current = next
		} else {
			return
		}
	}

	delete(current, parts[len(parts)-1])
}

func removeEmptyFields(obj *KubernetesObject) {
	if obj.Metadata != nil {
		if len(obj.Metadata) == 0 {
			obj.Metadata = nil
		}
	}
	if obj.Spec != nil {
		if len(obj.Spec) == 0 {
			obj.Spec = nil
		}
	}
	if obj.Status != nil {
		if len(obj.Status) == 0 {
			obj.Status = nil
		}
	}
	if obj.Data != nil {
		if len(obj.Data) == 0 {
			obj.Data = nil
		}
	}
}

// DeploymentCleaner cleans Deployment-specific fields.
type DeploymentCleaner struct {
	genericCleaner *GenericObjectCleaner
}

func (c *DeploymentCleaner) Clean(obj *KubernetesObject, options *CleanupOptions) {
	c.genericCleaner.Clean(obj, options) // Clean generic fields

	if obj.Spec != nil {
		delete(obj.Spec, "replicas")
		delete(obj.Spec, "revisionHistoryLimit")
		delete(obj.Spec, "strategy")
		delete(obj.Spec, "progressDeadlineSeconds")

		if template, ok := obj.Spec["template"].(map[string]interface{}); ok {
			if spec, ok := template["spec"].(map[string]interface{}); ok {
				cleanPodSpec(spec, options)
				if templateMeta, ok := template["metadata"].(map[string]interface{}); ok {
					// Remove null/empty fields from template metadata
					for k, v := range templateMeta {
						if v == nil {
							delete(templateMeta, k)
						}
					}
					// Remove template metadata if empty
					if len(templateMeta) == 0 {
						delete(template, "metadata")
					}
				}
			}
		}
	}
}

// ServiceCleaner cleans Service-specific fields.
type ServiceCleaner struct {
	genericCleaner *GenericObjectCleaner
}

func (c *ServiceCleaner) Clean(obj *KubernetesObject, options *CleanupOptions) {
	c.genericCleaner.Clean(obj, options)

	if obj.Spec != nil {
		delete(obj.Spec, "clusterIP")
		delete(obj.Spec, "clusterIPs")
		delete(obj.Spec, "selector")
	}
}

// StatefulSetCleaner cleans StatefulSet-specific fields.
type StatefulSetCleaner struct {
	genericCleaner *GenericObjectCleaner
}

func (c *StatefulSetCleaner) Clean(obj *KubernetesObject, options *CleanupOptions) {
	c.genericCleaner.Clean(obj, options)

	if obj.Spec != nil {
		delete(obj.Spec, "replicas")
		delete(obj.Spec, "revisionHistoryLimit")
		delete(obj.Spec, "updateStrategy")
		if template, ok := obj.Spec["template"].(map[string]interface{}); ok {
			if spec, ok := template["spec"].(map[string]interface{}); ok {
				cleanPodSpec(spec, options)
			}
		}
	}
}

// DaemonSetCleaner cleans DaemonSet-specific fields.
type DaemonSetCleaner struct {
	genericCleaner *GenericObjectCleaner
}

func (c *DaemonSetCleaner) Clean(obj *KubernetesObject, options *CleanupOptions) {
	c.genericCleaner.Clean(obj, options)
	if obj.Spec != nil {
		if template, ok := obj.Spec["template"].(map[string]interface{}); ok {
			if spec, ok := template["spec"].(map[string]interface{}); ok {
				cleanPodSpec(spec, options)
			}
		}
	}
}

// PodCleaner cleans Pod-specific fields.
type PodCleaner struct {
	genericCleaner *GenericObjectCleaner
}

func (c *PodCleaner) Clean(obj *KubernetesObject, options *CleanupOptions) {
	c.genericCleaner.Clean(obj, options)
	if obj.Spec != nil {
		cleanPodSpec(obj.Spec, options)
	}
	if options.RevertToDeployment {
		revertPodToDeployment(obj)
	}
}

// ConfigMapCleaner cleans ConfigMap-specific fields
type ConfigMapCleaner struct {
	genericCleaner *GenericObjectCleaner
}

func (c *ConfigMapCleaner) Clean(obj *KubernetesObject, options *CleanupOptions) {
	c.genericCleaner.Clean(obj, options)
	if obj.Data != nil {
		cleanConfigMapData(obj.Data)
	}
}

// clean config map
func cleanConfigMapData(data map[string]interface{}) {
	for key, val := range data {
		strVal, ok := val.(string)
		if ok {
			if strings.Contains(strVal, "kubectl.kubernetes.io/") || strings.Contains(strVal, "kubernetes.io/") {
				delete(data, key)
			}
		}
	}
}

// SecretCleaner cleans Secret-specific fields.
type SecretCleaner struct {
	genericCleaner *GenericObjectCleaner
}

func (c *SecretCleaner) Clean(obj *KubernetesObject, options *CleanupOptions) {
	c.genericCleaner.Clean(obj, options)
	//clean secret data
}

// cleanPodSpec removes fields from Pod specs.
func cleanPodSpec(spec map[string]interface{}, options *CleanupOptions) {
	fieldsToRemove := []string{
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
		"restartPolicy",
		"terminationGracePeriodSeconds",
		"hostIP",                // Remove hostIP
		"hostPID",               // Remove hostPID
		"hostname",              // Remove hostname
		"subdomain",             // Remove subdomain
		"shareProcessNamespace", //Remove
	}
	for _, field := range fieldsToRemove {
		delete(spec, field)
	}

	// Clean containers
	if containers, ok := spec["containers"].([]interface{}); ok {
		for _, container := range containers {
			if containerMap, ok := container.(map[string]interface{}); ok {
				cleanContainerSpec(containerMap, options) // Clean the container spec
			}
		}
	}

	//clean template metadata
	if template, ok := spec["template"].(map[string]interface{}); ok {
		// Clean template metadata thoroughly
		if templateMeta, ok := template["metadata"].(map[string]interface{}); ok {
			// Remove all runtime metadata from template
			fieldsToRemove := []string{
				"creationTimestamp",
				"generation",
				"resourceVersion",
				"selfLink",
				"uid",
			}
			for _, field := range fieldsToRemove {
				delete(templateMeta, field)
			}

			// If template metadata is empty except for labels, keep only labels
			hasOnlyLabels := true
			for k := range templateMeta {
				if k != "labels" {
					hasOnlyLabels = false
					break
				}
			}

			if len(templateMeta) == 0 || (hasOnlyLabels && templateMeta["labels"] != nil) {
				// Keep template metadata if it has labels, otherwise remove it
				if templateMeta["labels"] == nil {
					delete(template, "metadata")
				}
			}
		}
	}

	// Clean volumes and volumeMounts
	cleanPodVolumes(spec)
}

// cleanContainerSpec removes fields from container specs.
func cleanContainerSpec(container map[string]interface{}, options *CleanupOptions) {
	fieldsToRemove := []string{
		"terminationMessagePath",
		"terminationMessagePolicy",
		"imagePullPolicy",
		"securityContext",
		"livenessProbe",
		"readinessProbe",
		"startupProbe",
		"resources",
		"tty",       //remove
		"stdin",     //remove
		"stdinOnce", //remove
	}
	for _, field := range fieldsToRemove {
		delete(container, field)
	}

	// Clean default ports and recursively clean
	if ports, ok := container["ports"].([]interface{}); ok {
		cleanedPorts := make([]interface{}, 0)
		for _, p := range ports {
			if port, ok := p.(map[string]interface{}); ok {
				if proto, exists := port["protocol"].(string); exists && proto == "TCP" {
					delete(port, "protocol")
				}
				if len(port) > 0 {
					cleanedPorts = append(cleanedPorts, port)
				}
			}
		}
		if len(cleanedPorts) > 0 {
			container["ports"] = cleanedPorts
		} else {
			delete(container, "ports")
		}
	}

	// Recursively clean the rest of the container spec
	for _, value := range container {
		switch v := value.(type) {
		case map[string]interface{}:
			cleanContainerSpec(v, options)
		case []interface{}:
			for _, item := range v {
				if itemMap, ok := item.(map[string]interface{}); ok {
					cleanContainerSpec(itemMap, options)
				}
			}
		}
	}
}

// cleanPodVolumes removes kube-api-access volumes and related volumeMounts
func cleanPodVolumes(spec map[string]interface{}) {
	if volumes, ok := spec["volumes"].([]interface{}); ok {
		cleanedVolumes := make([]interface{}, 0)
		for _, volume := range volumes {
			if volumeMap, ok := volume.(map[string]interface{}); ok {
				if name, exists := volumeMap["name"].(string); exists && !strings.HasPrefix(name, "kube-api-access") {
					cleanedVolumes = append(cleanedVolumes, volume)
				}
			}
		}
		spec["volumes"] = cleanedVolumes
	}

	// Clean volumeMounts in containers
	if containers, ok := spec["containers"].([]interface{}); ok {
		for _, container := range containers {
			if containerMap, ok := container.(map[string]interface{}); ok {
				if volumeMounts, exists := containerMap["volumeMounts"].([]interface{}); exists {
					cleanedVolumeMounts := make([]interface{}, 0)
					for _, vm := range volumeMounts {
						if vmMap, ok := vm.(map[string]interface{}); ok {
							if name, exists := vmMap["name"].(string); exists && !strings.HasPrefix(name, "kube-api-access") {
								cleanedVolumeMounts = append(cleanedVolumeMounts, vm)
							}
						}
					}
					containerMap["volumeMounts"] = cleanedVolumeMounts
				}
			}
		}
	}
}

// revertPodToDeployment attempts to reconstruct a Deployment from a Pod.
func revertPodToDeployment(obj *KubernetesObject) {
	if obj.Kind != "Pod" {
		return // Only process Pods
	}

	// Check for the presence of a pod-template-hash.  This is a strong indicator
	// that the Pod was created by a Deployment.
	if labels, ok := obj.Metadata["labels"].(map[string]interface{}); ok {
		if _, hasHash := labels["pod-template-hash"]; hasHash {
			// Create a basic Deployment structure.
			deployment := map[string]interface{}{
				"apiVersion": "apps/v1", //  Hardcoded,
				"kind":       "Deployment",
				"metadata": map[string]interface{}{
					"name":      obj.Metadata["name"],      // Try to keep original name
					"labels":    obj.Metadata["labels"],    // Copy labels
					"namespace": obj.Metadata["namespace"], //copy namespace
				},
				"spec": map[string]interface{}{
					"selector": map[string]interface{}{
						"matchLabels": map[string]interface{}{
							"pod-template-hash": labels["pod-template-hash"],
						},
					},
					"template": map[string]interface{}{
						"metadata": map[string]interface{}{
							"labels": labels, //copy labels
						},
						"spec": obj.Spec, // Move the Pod's spec to the template
					},
				},
			}
			obj.Kind = "Deployment"
			obj.Spec = deployment["spec"].(map[string]interface{})
			obj.APIVersion = deployment["apiVersion"].(string)
			obj.Metadata = deployment["metadata"].(map[string]interface{})
			//clean the obj.metadata
			delete(obj.Metadata, "generateName")

			log.Println("Reverted Pod to Deployment")
		}
	}
}

// ObjectCleanerFactory maps kinds to cleaners.
type ObjectCleanerFactory struct {
	cleaners map[string]ObjectCleaner
}

// GetCleaner returns the appropriate cleaner for the given kind.
func (f *ObjectCleanerFactory) GetCleaner(kind string) ObjectCleaner {
	cleaner, ok := f.cleaners[kind]
	if !ok {
		// Default to the generic cleaner.
		return f.cleaners["Generic"]
	}
	return cleaner
}

// NewObjectCleanerFactory creates a new ObjectCleanerFactory.
func NewObjectCleanerFactory() *ObjectCleanerFactory {
	genericCleaner := &GenericObjectCleaner{metadataCleaner: &GenericMetadataCleaner{}}
	return &ObjectCleanerFactory{
		cleaners: map[string]ObjectCleaner{
			"Generic":     genericCleaner,
			"Deployment":  &DeploymentCleaner{genericCleaner: genericCleaner},
			"Service":     &ServiceCleaner{genericCleaner: genericCleaner},
			"StatefulSet": &StatefulSetCleaner{genericCleaner: genericCleaner},
			"DaemonSet":   &DaemonSetCleaner{genericCleaner: genericCleaner},
			"Pod":         &PodCleaner{genericCleaner: genericCleaner},
			"ConfigMap":   &ConfigMapCleaner{genericCleaner: genericCleaner},
			"Secret":      &SecretCleaner{genericCleaner: genericCleaner},
			// Add more cleaners for other kinds as needed.
		},
	}
}

// cleanupKubernetesObject cleans a Kubernetes object based on its kind.
func cleanupKubernetesObject(obj *KubernetesObject, options *CleanupOptions, cleanerFactory *ObjectCleanerFactory) {
	cleaner := cleanerFactory.GetCleaner(obj.Kind)
	cleanupErr := false
	if cleaner == nil {
		log.Printf("ERROR: Cleaner for %s is nil", obj.Kind)
		cleanupErr = true
	} else {
		cleaner.Clean(obj, options)
	}

	if cleanupErr {
		log.Printf("ERROR: Object was not cleaned %v", obj)
	}

}

// cleanupManifest processes the input YAML, cleans each object, and writes the cleaned YAML to the output.
func cleanupManifest(input io.Reader, output io.Writer, options *CleanupOptions) error {
	reader := bufio.NewReader(input)
	decoder := yaml.NewDecoder(reader)
	encoder := yaml.NewEncoder(output)
	defer encoder.Close()

	documentCount := 0
	cleanerFactory := NewObjectCleanerFactory()

	for {
		var obj KubernetesObject
		err := decoder.Decode(&obj)
		if err == io.EOF {
			if documentCount == 0 {
				return fmt.Errorf("no valid YAML documents found")
			}
			break
		}
		if err != nil {
			return fmt.Errorf("error decoding YAML: %w", err)
		}
		documentCount++

		if obj.Kind == "" && obj.APIVersion == "" {
			log.Println("Skipping empty document")
			continue // Skip empty documents
		}

		cleanupKubernetesObject(&obj, options, cleanerFactory)

		err = encoder.Encode(obj)
		if err != nil {
			return fmt.Errorf("error encoding cleaned YAML: %w", err)
		}
	}
	return nil
}

func main() {
	options := &CleanupOptions{
		RemoveManagedFields:   true,
		RemoveStatus:          true,
		RemoveNamespace:       true,
		RemoveClusterName:     true,
		RemoveLabels:          []string{},
		RemoveAnnotations:     []string{},
		RemoveEmpty:           true,
		CleanupFinalizers:     false,
		RevertToDeployment:    true, // Enable reverting to Deployment
		PreserveResourceState: true,
		ResourceStateMode:     "Desired", // Default to desired state cleanup
	}
	log.SetOutput(os.Stderr)
	log.SetPrefix("[Kleanup] ")
	log.SetFlags(log.Ldate | log.Ltime | log.Lshortfile)

	if err := cleanupManifest(os.Stdin, os.Stdout, options); err != nil {
		fmt.Fprintf(os.Stderr, "Error cleaning manifest: %v\n", err)
		os.Exit(1)
	}
}
