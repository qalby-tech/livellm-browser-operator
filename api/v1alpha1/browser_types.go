package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// BrowserSpec defines the desired state of a Browser instance.
type BrowserSpec struct {
	// ProfileUID is the unique profile identifier used as browser_id in the controller.
	// Defaults to the CR name if empty.
	ProfileUID string `json:"profileUid"`

	// Running controls whether the browser workload is up. When false, the Deployment is scaled to zero (PVC retained).
	// When omitted, defaults to true.
	// +kubebuilder:default=true
	// +optional
	Running *bool `json:"running,omitempty"`

	// Image overrides the browser container image.
	// If empty, the operator uses its configured default (DEFAULT_BROWSER_IMAGE env var).
	// +optional
	Image string `json:"image,omitempty"`

	// Resources defines CPU/memory requests and limits for the browser pod.
	// +optional
	Resources *ResourcesSpec `json:"resources,omitempty"`

	// Storage is the PVC size for profile data (e.g. "1Gi").
	// +kubebuilder:default="1Gi"
	// +optional
	Storage string `json:"storage,omitempty"`

	// ShmSize is the /dev/shm size for Chrome (e.g. "4Gi").
	// +kubebuilder:default="4Gi"
	// +optional
	ShmSize string `json:"shmSize,omitempty"`

	// Proxy configures HTTP proxy for the browser.
	// Only applied when profileUid != "default" (operator creates a new browser via launcher API).
	// +optional
	Proxy *ProxySpec `json:"proxy,omitempty"`

	// Extensions is a list of Chrome Web Store extension IDs to install at creation time.
	// The launcher downloads and injects them into the profile before Chrome starts.
	// +optional
	Extensions []string `json:"extensions,omitempty"`

	// Cookies loads a JSON array of cookies from a ConfigMap or Secret
	// and injects them into the browser at creation time.
	// +optional
	Cookies *CookiesSource `json:"cookies,omitempty"`

	// ReclaimPolicy determines what happens to the PVC when the Browser CR is deleted.
	// "Retain" (default) keeps profile data; "Delete" removes it.
	// +kubebuilder:validation:Enum=Retain;Delete
	// +kubebuilder:default="Retain"
	// +optional
	ReclaimPolicy string `json:"reclaimPolicy,omitempty"`
}

// CookiesSource references a ConfigMap or Secret containing a JSON array of cookies.
// Exactly one of ConfigMapRef or SecretRef must be set.
type CookiesSource struct {
	// ConfigMapRef references a ConfigMap key containing a JSON array of cookies.
	// +optional
	ConfigMapRef *KeyRef `json:"configMapRef,omitempty"`
	// SecretRef references a Secret key containing a JSON array of cookies.
	// +optional
	SecretRef *KeyRef `json:"secretRef,omitempty"`
}

// KeyRef identifies a key within a ConfigMap or Secret.
type KeyRef struct {
	// Name of the ConfigMap or Secret.
	Name string `json:"name"`
	// Key within the resource. Defaults to "cookies.json".
	// +kubebuilder:default="cookies.json"
	// +optional
	Key string `json:"key,omitempty"`
}

// ProxySpec configures HTTP proxy for a browser.
type ProxySpec struct {
	// Server is the proxy URL (e.g. http://proxy:8080).
	Server string `json:"server"`
	// Username for proxy authentication.
	// +optional
	Username string `json:"username,omitempty"`
	// Password for proxy authentication.
	// +optional
	Password string `json:"password,omitempty"`
	// Bypass is a comma-separated list of hosts to bypass.
	// +optional
	Bypass string `json:"bypass,omitempty"`
}

// ResourcesSpec mirrors simplified k8s resource requirements.
type ResourcesSpec struct {
	// Requests describes the minimum resources required.
	// +optional
	Requests map[string]string `json:"requests,omitempty"`
	// Limits describes the maximum resources allowed.
	// +optional
	Limits map[string]string `json:"limits,omitempty"`
}

// BrowserPhase describes the lifecycle phase of a Browser.
// +kubebuilder:validation:Enum=Pending;Creating;Running;Failed;Stopped
type BrowserPhase string

const (
	BrowserPhasePending  BrowserPhase = "Pending"
	BrowserPhaseCreating BrowserPhase = "Creating"
	BrowserPhaseRunning  BrowserPhase = "Running"
	BrowserPhaseFailed   BrowserPhase = "Failed"
	BrowserPhaseStopped  BrowserPhase = "Stopped"
)

// BrowserStatus defines the observed state of a Browser.
type BrowserStatus struct {
	// Phase is the current lifecycle phase.
	// +optional
	Phase BrowserPhase `json:"phase,omitempty"`

	// PodName is the name of the running browser pod.
	// +optional
	PodName string `json:"podName,omitempty"`

	// PodIP is the cluster IP of the running browser pod.
	// +optional
	PodIP string `json:"podIP,omitempty"`

	// CdpPort is the CDP proxy port inside the pod.
	// +optional
	CdpPort int `json:"cdpPort,omitempty"`

	// WsEndpoint is the WebSocket path (e.g. /devtools/browser/...).
	// +optional
	WsEndpoint string `json:"wsEndpoint,omitempty"`

	// WsURL is the full CDP WebSocket URL: ws://<podIP>:<cdpPort><wsEndpoint>
	// +optional
	WsURL string `json:"wsUrl,omitempty"`

	// Message is a human-readable status message.
	// +optional
	Message string `json:"message,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:shortName=br
// +kubebuilder:printcolumn:name="Profile",type=string,JSONPath=`.spec.profileUid`
// +kubebuilder:printcolumn:name="Phase",type=string,JSONPath=`.status.phase`
// +kubebuilder:printcolumn:name="WS URL",type=string,JSONPath=`.status.wsUrl`,priority=1
// +kubebuilder:printcolumn:name="Pod IP",type=string,JSONPath=`.status.podIP`,priority=1
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// Browser is the Schema for the browsers API.
// Each Browser CR results in one browser pod with a persistent profile.
type Browser struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   BrowserSpec   `json:"spec,omitempty"`
	Status BrowserStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// BrowserList contains a list of Browser resources.
type BrowserList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []Browser `json:"items"`
}
