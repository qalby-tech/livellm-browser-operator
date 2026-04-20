package v1alpha1

import (
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// ControllerSpec defines the desired state of a Controller deployment.
type ControllerSpec struct {
	// Image overrides the controller container image.
	// If empty, the operator uses its configured default (DEFAULT_CONTROLLER_IMAGE env var).
	// +optional
	Image string `json:"image,omitempty"`

	// Replicas is the number of controller pod replicas.
	// +kubebuilder:default=1
	// +optional
	Replicas *int32 `json:"replicas,omitempty"`

	// Resources defines CPU/memory requests and limits for the controller pod.
	// +optional
	Resources *ResourcesSpec `json:"resources,omitempty"`

	// BrowserSelector filters which Browser CRs to auto-register.
	// Uses label matching on Browser CRs.
	// If empty, all Running Browser CRs in the same namespace are registered.
	// +optional
	BrowserSelector map[string]string `json:"browserSelector,omitempty"`

	// NodeOptions sets the V8 --max-old-space-size for the Patchright Node.js driver.
	// Given in MB (e.g. "3072"). Set to "0" to disable.
	// Defaults to "3072" (3 GB) when empty.
	// +optional
	NodeOptions string `json:"nodeOptions,omitempty"`

	// ExtraEnv is a list of additional environment variables injected into the controller container.
	// +optional
	ExtraEnv []corev1.EnvVar `json:"extraEnv,omitempty"`
}

// ControllerPhase describes the lifecycle phase of a Controller.
// +kubebuilder:validation:Enum=Pending;Creating;Running;Failed
type ControllerPhase string

const (
	ControllerPhasePending  ControllerPhase = "Pending"
	ControllerPhaseCreating ControllerPhase = "Creating"
	ControllerPhaseRunning  ControllerPhase = "Running"
	ControllerPhaseFailed   ControllerPhase = "Failed"
)

// RegisteredBrowser records a browser that has been registered with the controller.
type RegisteredBrowser struct {
	// Name is the Browser CR name.
	Name string `json:"name"`
	// ProfileUID is the profile identifier used as browser_id.
	ProfileUID string `json:"profileUid"`
	// WsURL is the CDP WebSocket URL registered with the controller.
	WsURL string `json:"wsUrl"`
}

// ControllerStatus defines the observed state of a Controller.
type ControllerStatus struct {
	// Phase is the current lifecycle phase.
	// +optional
	Phase ControllerPhase `json:"phase,omitempty"`

	// PodName is the name of a running controller pod.
	// +optional
	PodName string `json:"podName,omitempty"`

	// URL is the in-cluster base URL for the controller API (e.g. http://name:8000/parser).
	// +optional
	URL string `json:"url,omitempty"`

	// RegisteredBrowsers lists browsers currently registered with the controller.
	// +optional
	RegisteredBrowsers []RegisteredBrowser `json:"registeredBrowsers,omitempty"`

	// RegisteredBrowserCount is the number of registered browsers (for printer column).
	// +optional
	RegisteredBrowserCount int `json:"registeredBrowserCount,omitempty"`

	// Message is a human-readable status message.
	// +optional
	Message string `json:"message,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:shortName=bc
// +kubebuilder:printcolumn:name="Phase",type=string,JSONPath=`.status.phase`
// +kubebuilder:printcolumn:name="URL",type=string,JSONPath=`.status.url`
// +kubebuilder:printcolumn:name="Browsers",type=integer,JSONPath=`.status.registeredBrowserCount`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// Controller is the Schema for the controllers API.
// Each Controller CR deploys a livellm-controller instance and auto-registers Browser CRs.
type Controller struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   ControllerSpec   `json:"spec,omitempty"`
	Status ControllerStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// ControllerList contains a list of Controller resources.
type ControllerList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []Controller `json:"items"`
}
