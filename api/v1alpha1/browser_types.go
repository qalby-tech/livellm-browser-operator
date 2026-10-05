package v1alpha1

import (
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// BrowserSpec defines the desired state of a Browser instance.
type BrowserSpec struct {
	// ProfileUID is the unique profile identifier used as browser_id in the controller.
	// Defaults to the CR name if empty.
	ProfileUID string `json:"profileUid"`

	// Running controls whether the browser workload is up. When false, the Deployment is scaled to zero (PVC retained).
	// When omitted, the controller treats it as true.
	// +optional
	Running *bool `json:"running,omitempty"`

	// Engine is the browser engine: chrome or camoufox. Absent means chrome.
	// It is set at creation by the platform and does not change afterwards.
	// +kubebuilder:validation:Enum=chrome;camoufox
	// +optional
	Engine string `json:"engine,omitempty"`

	// Image overrides the browser container image.
	// If empty, the operator uses its configured default (DEFAULT_BROWSER_IMAGE,
	// or DEFAULT_CAMOUFOX_IMAGE for a camoufox browser).
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

	// Proxy configures the HTTP proxy for the browser. Passed to the pod as
	// BROWSER_PROXY_* env and applied to the default browser at startup.
	// A username or password set here reaches the browser container as env and is
	// readable by anyone who can connect to the browser.
	// +optional
	Proxy *ProxySpec `json:"proxy,omitempty"`

	// Extensions is a list of Chrome Web Store extension IDs to install at creation time.
	// The launcher downloads and injects them into the profile before Chrome starts.
	// A camoufox browser takes no extensions: the field is ignored there.
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

	// Env is a list of environment variables injected into the browser container.
	// +optional
	Env []corev1.EnvVar `json:"env,omitempty"`

	// PodLabels are extra labels stamped onto the workload pods (not the
	// Deployment or the selector). General-purpose passthrough — e.g. set
	// istio.io/dataplane-mode=none to keep a pod out of the ambient mesh, or a
	// marker label whose change forces a clean rollout. Empty by default.
	// +optional
	PodLabels map[string]string `json:"podLabels,omitempty"`

	// NodeSelector restricts which nodes the browser pod may run on (a plain
	// passthrough to the pod's nodeSelector). Empty by default.
	// +optional
	NodeSelector map[string]string `json:"nodeSelector,omitempty"`

	// Control adds the control sidecar to the browser pod: it holds proxy logins,
	// serves profile snapshots and, whenever spec.proxy is also set, runs a relay
	// that passes no traffic until its config arrives. It reads its settings from
	// the named Secret, which is mounted into the sidecar only. When omitted, the
	// pod is rendered without it.
	// +optional
	Control *ControlSpec `json:"control,omitempty"`
}

// ControlSpec configures the browser's control sidecar.
type ControlSpec struct {
	// SecretName is the Secret in the Browser's namespace that holds the
	// sidecar's key, config and proxy logins. Only the sidecar mounts it.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=253
	// +kubebuilder:validation:Pattern=`^[a-z0-9]([-a-z0-9]*[a-z0-9])?(\.[a-z0-9]([-a-z0-9]*[a-z0-9])?)*$`
	SecretName string `json:"secretName"`
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

// Browser engines (spec.engine). An absent engine means EngineChrome.
const (
	EngineChrome   = "chrome"
	EngineCamoufox = "camoufox"
)

// BrowserPhase describes the lifecycle phase of a Browser.
// +kubebuilder:validation:Enum=Creating;Running;Stopped
type BrowserPhase string

const (
	BrowserPhaseCreating BrowserPhase = "Creating"
	BrowserPhaseRunning  BrowserPhase = "Running"
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

	// WsURL is the browser's automation WebSocket URL: for chrome the CDP URL
	// ws://<name>.<namespace>.svc.cluster.local:9222/devtools/browser/<id>, for
	// camoufox the Playwright URL
	// ws://<name>.<namespace>.svc.cluster.local:9222/playwright/default
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
