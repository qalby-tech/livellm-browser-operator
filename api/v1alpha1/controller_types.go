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

	// Env is a list of environment variables injected into the controller container.
	// +optional
	Env []corev1.EnvVar `json:"env,omitempty"`

	// MaxPagesPerBrowser sets the maximum number of concurrent pages (sessions)
	// a single browser may hold.  When the limit is reached, new sessions are
	// routed to a different browser or — if autoscaleBrowser is true — a new
	// Browser CR is created automatically.
	// Defaults to 50 when autoscaleBrowser is true.
	// +optional
	MaxPagesPerBrowser *int32 `json:"maxPagesPerBrowser,omitempty"`

	// AutoscaleBrowser enables automatic creation of new Browser CRs when
	// existing browsers reach maxPagesPerBrowser.
	// The operator creates Browser CRs named <controller>-autoscale-<N>.
	// +optional
	AutoscaleBrowser *bool `json:"autoscaleBrowser,omitempty"`

	// AutoscaleBrowserTemplate specifies the Browser spec used when creating
	// autoscaled browsers.  If empty, the operator copies the spec from the
	// first manually-defined Browser CR in the same namespace (matched by
	// browserSelector).  At a minimum, profileUid is generated automatically.
	// +optional
	AutoscaleBrowserTemplate *AutoscaleBrowserTemplateSpec `json:"autoscaleBrowserTemplate,omitempty"`
}

// AutoscaleBrowserTemplateSpec is the template for browser CRs created by autoscaling.
type AutoscaleBrowserTemplateSpec struct {
	// Resources for the autoscaled browser pod.
	// +optional
	Resources *ResourcesSpec `json:"resources,omitempty"`

	// Storage is the PVC size (e.g. "1Gi").
	// +optional
	Storage string `json:"storage,omitempty"`

	// ShmSize is the /dev/shm size (e.g. "4Gi").
	// +optional
	ShmSize string `json:"shmSize,omitempty"`

	// Extensions to install in autoscaled browsers.
	// +optional
	Extensions []string `json:"extensions,omitempty"`

	// Env is a list of environment variables injected into autoscaled browser pods.
	// +optional
	Env []corev1.EnvVar `json:"env,omitempty"`

	// ReclaimPolicy for the PVC when the autoscaled browser is deleted.
	// +kubebuilder:validation:Enum=Retain;Delete
	// +kubebuilder:default="Delete"
	// +optional
	ReclaimPolicy string `json:"reclaimPolicy,omitempty"`
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
	// PageCount is the current number of open pages/sessions on this browser.
	// +optional
	PageCount int `json:"pageCount,omitempty"`
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

	// TotalPageCount is the sum of open pages across all registered browsers.
	// +optional
	TotalPageCount int `json:"totalPageCount,omitempty"`

	// AutoscaledBrowserCount is the number of Browser CRs created by autoscaling.
	// +optional
	AutoscaledBrowserCount int `json:"autoscaledBrowserCount,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:shortName=bc
// +kubebuilder:printcolumn:name="Phase",type=string,JSONPath=`.status.phase`
// +kubebuilder:printcolumn:name="URL",type=string,JSONPath=`.status.url`
// +kubebuilder:printcolumn:name="Browsers",type=integer,JSONPath=`.status.registeredBrowserCount`
// +kubebuilder:printcolumn:name="Pages",type=integer,JSONPath=`.status.totalPageCount`,priority=1
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
