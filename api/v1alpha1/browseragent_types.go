package v1alpha1

import (
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// AgentTarget selects the browser the agent drives over CDP. Exactly one of
// BrowserRef / ControllerRef / ExternalWsURL should be set:
//   - BrowserRef: an in-namespace Browser CR — plain CDP to its status.wsUrl.
//   - ControllerRef (+ BrowserID): drive a browser via a Controller; the agent
//     also registers the controller's /parser endpoints as tools.
//   - ExternalWsURL: a BYO/remote CDP websocket endpoint.
type AgentTarget struct {
	// BrowserRef is an in-namespace Browser CR name (plain CDP).
	// +optional
	BrowserRef string `json:"browserRef,omitempty"`

	// ControllerRef is an in-namespace Controller CR name. When set, the agent
	// resolves the CDP endpoint via the controller and registers its
	// search/content/interact/attribute endpoints as tools.
	// +optional
	ControllerRef string `json:"controllerRef,omitempty"`

	// BrowserID is the registered browser_id (profileUid) to drive when
	// ControllerRef is set. Defaults to the only browser if the controller has one.
	// +optional
	BrowserID string `json:"browserId,omitempty"`

	// ExternalWsURL is a BYO/remote CDP websocket endpoint (ws:// or wss://).
	// +optional
	ExternalWsURL string `json:"externalWsUrl,omitempty"`
}

// AgentModel points the agent's brain at the tenant's own AI provider. There is
// no implicit default — if this does not resolve to a provider + key, the agent
// refuses the task (model_not_configured) and never bills a platform key.
type AgentModel struct {
	// Provider is the LLM provider: anthropic | openai | openai-compatible | google.
	Provider string `json:"provider"`

	// Name is the model id (a vision-capable model is preferred).
	// +optional
	Name string `json:"name,omitempty"`

	// BaseURL optionally overrides the provider endpoint (proxy / compatible API).
	// +optional
	BaseURL string `json:"baseUrl,omitempty"`

	// APIKeySecret references the Secret key holding the provider API key. It is
	// injected into the pod via secretKeyRef and held only for the run.
	// +optional
	APIKeySecret *corev1.SecretKeySelector `json:"apiKeySecret,omitempty"`
}

// AgentControl configures the cloud_gateways control channel the UI connects to
// for live per-step review.
type AgentControl struct {
	// URL is the gateway websocket base (e.g. wss://<gateway>/agent/...).
	// +optional
	URL string `json:"url,omitempty"`

	// TokenSecret references the Secret key holding the control-channel token.
	// +optional
	TokenSecret *corev1.SecretKeySelector `json:"tokenSecret,omitempty"`
}

// AgentArtifacts configures the MinIO/S3 store for run video + trajectory JSON.
type AgentArtifacts struct {
	// Endpoint is the S3-compatible endpoint (e.g. http://minio.minio:9000).
	// +optional
	Endpoint string `json:"endpoint,omitempty"`
	// Bucket holds run artifacts. Defaults to "browser-agent".
	// +optional
	Bucket string `json:"bucket,omitempty"`
	// AccessKeySecret / SecretKeySecret reference the S3 credential Secret keys.
	// +optional
	AccessKeySecret *corev1.SecretKeySelector `json:"accessKeySecret,omitempty"`
	// +optional
	SecretKeySecret *corev1.SecretKeySelector `json:"secretKeySecret,omitempty"`
}

// AgentRecording toggles MP4 recording of runs (recorder lands later; the flag
// is plumbed now so the spec is stable).
type AgentRecording struct {
	// +kubebuilder:default=true
	// +optional
	Enabled *bool `json:"enabled,omitempty"`
}

// BrowserAgentSpec defines the desired state of a BrowserAgent.
type BrowserAgentSpec struct {
	// Target selects the browser to drive over CDP.
	Target AgentTarget `json:"target"`

	// Model is the tenant's AI provider for the agent's brain (REQUIRED at run time).
	Model AgentModel `json:"model"`

	// Control configures the live review/control channel.
	// +optional
	Control *AgentControl `json:"control,omitempty"`

	// Artifacts configures where run video + trajectory JSON are stored.
	// +optional
	Artifacts *AgentArtifacts `json:"artifacts,omitempty"`

	// Recording toggles run video capture.
	// +optional
	Recording *AgentRecording `json:"recording,omitempty"`

	// Running controls whether the agent runtime is up. When false, the
	// Deployment is scaled to zero. Omitted ⇒ true.
	// +optional
	Running *bool `json:"running,omitempty"`

	// Image overrides the agent container image (default: DEFAULT_AGENT_IMAGE).
	// +optional
	Image string `json:"image,omitempty"`

	// Resources defines CPU/memory requests and limits for the agent pod.
	// +optional
	Resources *ResourcesSpec `json:"resources,omitempty"`

	// ImagePullSecrets are names of Secrets used to pull the (private) agent image.
	// +optional
	ImagePullSecrets []string `json:"imagePullSecrets,omitempty"`

	// Env is a list of extra environment variables for the agent container.
	// +optional
	Env []corev1.EnvVar `json:"env,omitempty"`

	// PodLabels are extra labels stamped onto the agent pods (not the Deployment
	// or the selector). General-purpose passthrough.
	// +optional
	PodLabels map[string]string `json:"podLabels,omitempty"`
}

// BrowserAgentPhase describes the lifecycle phase of a BrowserAgent.
// +kubebuilder:validation:Enum=Pending;Creating;Running;Failed;Stopped
type BrowserAgentPhase string

const (
	BrowserAgentPhasePending  BrowserAgentPhase = "Pending"
	BrowserAgentPhaseCreating BrowserAgentPhase = "Creating"
	BrowserAgentPhaseRunning  BrowserAgentPhase = "Running"
	BrowserAgentPhaseFailed   BrowserAgentPhase = "Failed"
	BrowserAgentPhaseStopped  BrowserAgentPhase = "Stopped"
)

// BrowserAgentStatus defines the observed state of a BrowserAgent.
type BrowserAgentStatus struct {
	// Phase is the current lifecycle phase.
	// +optional
	Phase BrowserAgentPhase `json:"phase,omitempty"`

	// PodName is the name of a running agent pod.
	// +optional
	PodName string `json:"podName,omitempty"`

	// URL is the in-cluster base URL for the agent API (e.g. http://name.ns:8800).
	// POST <url>/act launches a task.
	// +optional
	URL string `json:"url,omitempty"`

	// ResolvedWsURL is the CDP endpoint the agent was wired to drive.
	// +optional
	ResolvedWsURL string `json:"resolvedWsUrl,omitempty"`

	// ControlURL echoes spec.control.url for the UI to connect to.
	// +optional
	ControlURL string `json:"controlUrl,omitempty"`

	// Message is a human-readable status message.
	// +optional
	Message string `json:"message,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:shortName=ba
// +kubebuilder:printcolumn:name="Phase",type=string,JSONPath=`.status.phase`
// +kubebuilder:printcolumn:name="Provider",type=string,JSONPath=`.spec.model.provider`
// +kubebuilder:printcolumn:name="Target",type=string,JSONPath=`.status.resolvedWsUrl`,priority=1
// +kubebuilder:printcolumn:name="URL",type=string,JSONPath=`.status.url`,priority=1
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// BrowserAgent is the Schema for the browseragents API.
// Each BrowserAgent CR deploys a livellm-agent runtime that drives a Browser
// (directly or via a Controller) to complete tasks as reviewable trajectories.
type BrowserAgent struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   BrowserAgentSpec   `json:"spec,omitempty"`
	Status BrowserAgentStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// BrowserAgentList contains a list of BrowserAgent resources.
type BrowserAgentList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []BrowserAgent `json:"items"`
}
