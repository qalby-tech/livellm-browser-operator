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

	// Autodiscover, when set, controls namespace-wide browser discovery.
	// nil/true: register every Running Browser in the namespace (optionally
	// filtered by browserSelector). false: register ONLY the browsers named in
	// Browsers plus ExternalBrowsers — used for singleton (one browser) and
	// grouped-manual controllers. An explicit false is honoured; nil stays
	// "every browser" (the platform controller relies on it).
	// +optional
	Autodiscover *bool `json:"autodiscover,omitempty"`

	// Browsers is an explicit list of in-namespace Browser CR names to register
	// with this controller (in addition to any autodiscovered ones).
	// +optional
	Browsers []string `json:"browsers,omitempty"`

	// ExternalBrowsers are remote/BYO browsers reachable at a user-supplied CDP
	// websocket endpoint, registered alongside in-cluster Browsers.
	// +optional
	ExternalBrowsers []ExternalBrowser `json:"externalBrowsers,omitempty"`

	// Env is a list of environment variables injected into the controller container.
	// +optional
	Env []corev1.EnvVar `json:"env,omitempty"`

	// Deprecated: the platform never sets it; the controller picks the browser
	// with the fewest open tabs and has no "full". Kept for stored CRs.
	//
	// MaxPagesPerBrowser sets the maximum number of concurrent pages (sessions)
	// a single browser may hold.  When the limit is reached, new sessions are
	// routed to a different browser or — if autoscaleBrowser is true — a new
	// Browser CR is created automatically.
	// Defaults to 50 when autoscaleBrowser is true.
	// +optional
	MaxPagesPerBrowser *int32 `json:"maxPagesPerBrowser,omitempty"`

	// Deprecated: the platform never sets it. Kept for stored CRs.
	//
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

	// PodLabels are extra labels stamped onto the controller pods (not the
	// Deployment or the selector). General-purpose passthrough — e.g. set
	// istio.io/dataplane-mode=none to keep a pod out of the ambient mesh, or a
	// marker label whose change forces a clean rollout. Empty by default.
	// +optional
	PodLabels map[string]string `json:"podLabels,omitempty"`
}

// ExternalBrowser is a remote/BYO browser registered by ws endpoint.
type ExternalBrowser struct {
	// ID is the browser id: the value of the X-Browser-Id header and the
	// <id> of the /browsers/<id>/ path.
	ID string `json:"id"`
	// WsURL is the CDP websocket endpoint (ws:// or wss://).
	WsURL string `json:"wsUrl"`
	// AuthHeader optionally sets one HTTP header sent on the CDP connect. The
	// value is "Name: value" when the text before the first ':' is a header
	// name (letters, digits and '-' only, e.g. "X-Api-Key: abc"); any other
	// value is sent whole as "Authorization: <value>" (e.g. "Bearer abc",
	// "Bearer user:pass"). Prefer AuthHeaderSecretRef, which keeps the value
	// off this object.
	// +optional
	AuthHeader string `json:"authHeader,omitempty"`
	// AuthHeaderSecretRef reads the same value (same rule as AuthHeader) from a
	// key of a Secret in the Controller's namespace. It wins over AuthHeader.
	// A missing Secret or key registers the browser without a header and says
	// so in status.message. The operator copies the header into the
	// controller's browser registry; a change to the Secret is picked up
	// within a minute.
	// +optional
	AuthHeaderSecretRef *corev1.SecretKeySelector `json:"authHeaderSecretRef,omitempty"`
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
// +kubebuilder:validation:Enum=Creating;Running
type ControllerPhase string

const (
	ControllerPhaseCreating ControllerPhase = "Creating"
	ControllerPhaseRunning  ControllerPhase = "Running"
)

// RegisteredBrowser records a browser that has been registered with the controller.
type RegisteredBrowser struct {
	// Name is the Browser CR name (the id, for a remote browser).
	Name string `json:"name"`
	// ProfileUID is the browser id: the X-Browser-Id value and the <id> of
	// the /browsers/<id>/ path.
	ProfileUID string `json:"profileUid"`
	// Deprecated: no longer filled (status carries no internal or remote
	// addresses). Kept so stored statuses stay valid.
	// +optional
	WsURL string `json:"wsUrl,omitempty"`
	// Remote is true for a browser from spec.externalBrowsers.
	// +optional
	Remote bool `json:"remote,omitempty"`
	// PageCount is the number of sessions that live on this browser.
	// +optional
	PageCount int `json:"pageCount,omitempty"`
	// OpenTabs is the number of tabs open in the browser, including ones a
	// person opened (0 while the controller has not connected to it). Left
	// out when the controller could not be asked: absent means unknown.
	// +optional
	OpenTabs *int `json:"openTabs,omitempty"`
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

	// TotalPageCount is the sum of sessions across all registered browsers.
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
