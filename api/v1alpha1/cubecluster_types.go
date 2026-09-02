package v1alpha1

import (
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

type WorkloadSpec struct {
	// +kubebuilder:validation:Minimum=1
	Replicas  *int32                      `json:"replicas,omitempty"`
	Resources corev1.ResourceRequirements `json:"resources,omitempty"`
}

type PersistentVolumeStorage struct {
	ExistingClaim string `json:"existingClaim"`
}

type S3Storage struct {
	Bucket    string `json:"bucket"`
	Region    string `json:"region"`
	SecretRef string `json:"secretRef,omitempty"`
}

type RemoteStorageSpec struct {
	// +kubebuilder:validation:Enum=persistentVolume;s3
	Type             string                   `json:"type"`
	PersistentVolume *PersistentVolumeStorage `json:"persistentVolume,omitempty"`
	S3               *S3Storage               `json:"s3,omitempty"`
}

type ScratchStorageSpec struct {
	// Size limits the ephemeral scratch volume. It is not durable storage.
	Size string `json:"size,omitempty"`
}

type CubeStoreSpec struct {
	// +kubebuilder:validation:Enum=single;clustered
	Mode  string `json:"mode,omitempty"`
	Image string `json:"image,omitempty"`
	// +kubebuilder:validation:Minimum=1
	Workers       *int32                      `json:"workers,omitempty"`
	Resources     corev1.ResourceRequirements `json:"resources,omitempty"`
	RemoteStorage RemoteStorageSpec           `json:"remoteStorage"`
	Scratch       ScratchStorageSpec          `json:"scratch,omitempty"`
}

type ServiceSpec struct {
	// +kubebuilder:validation:Enum=ClusterIP;NodePort;LoadBalancer
	Type corev1.ServiceType `json:"type,omitempty"`
	// +kubebuilder:validation:Minimum=30000
	// +kubebuilder:validation:Maximum=32767
	NodePort    int32             `json:"nodePort,omitempty"`
	Annotations map[string]string `json:"annotations,omitempty"`
}

type CubeClusterSpec struct {
	Image string `json:"image,omitempty"`
	// +kubebuilder:validation:Enum=Always;Never;IfNotPresent
	ImagePullPolicy     corev1.PullPolicy `json:"imagePullPolicy,omitempty"`
	ModelConfigMap      string            `json:"modelConfigMap"`
	ConfigurationSecret string            `json:"configurationSecret"`
	API                 WorkloadSpec      `json:"api,omitempty"`
	RefreshWorker       WorkloadSpec      `json:"refreshWorker,omitempty"`
	CubeStore           CubeStoreSpec     `json:"cubeStore"`
	Service             ServiceSpec       `json:"service,omitempty"`
}

type CubeClusterStatus struct {
	ObservedGeneration int64              `json:"observedGeneration,omitempty"`
	ReadyAPIs          int32              `json:"readyAPIs,omitempty"`
	Endpoint           string             `json:"endpoint,omitempty"`
	Conditions         []metav1.Condition `json:"conditions,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:shortName=cube
// +kubebuilder:printcolumn:name="Ready",type="string",JSONPath=".status.conditions[?(@.type=='Ready')].status"
// +kubebuilder:printcolumn:name="API",type="integer",JSONPath=".status.readyAPIs"
// +kubebuilder:printcolumn:name="Endpoint",type="string",JSONPath=".status.endpoint"
type CubeCluster struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	Spec              CubeClusterSpec   `json:"spec,omitempty"`
	Status            CubeClusterStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true
type CubeClusterList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []CubeCluster `json:"items"`
}
