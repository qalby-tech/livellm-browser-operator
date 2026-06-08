package controller

import (
	"fmt"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"

	browserv1 "github.com/livellm/browser-operator/api/v1alpha1"
)

const (
	agentPort         = 8800
	defaultAgentImage = "kamasalyamov/livellm-browser:agent-0.1.0"
)

func agentLabels(name string) map[string]string {
	return map[string]string{
		"app.kubernetes.io/name":       "livellm-agent",
		"app.kubernetes.io/instance":   name,
		"app.kubernetes.io/managed-by": "livellm-operator",
		"livellm.io/browseragent":      name,
	}
}

func agentSelectorLabels(name string) map[string]string {
	return map[string]string{
		"livellm.io/browseragent": name,
	}
}

func agentWorkloadWanted(a *browserv1.BrowserAgent) bool {
	return a.Spec.Running == nil || *a.Spec.Running
}

// resolvedTarget carries the CDP wiring the reconciler computed from spec.target.
type resolvedTarget struct {
	cdpWsURL      string // the CDP endpoint BrowserSession connects to
	controllerURL string // /parser base, when targeting a Controller
	browserID     string // registry browser_id, when targeting a Controller
}

// secretEnv builds an env var sourced from a Secret key, or nil if unset.
func secretEnv(name string, sel *corev1.SecretKeySelector) *corev1.EnvVar {
	if sel == nil {
		return nil
	}
	return &corev1.EnvVar{Name: name, ValueFrom: &corev1.EnvVarSource{SecretKeyRef: sel}}
}

func buildAgentEnv(a *browserv1.BrowserAgent, tgt resolvedTarget, defaultEnv []corev1.EnvVar) []corev1.EnvVar {
	env := []corev1.EnvVar{
		{Name: "AGENT_TENANT_ID", Value: a.Namespace},
		{Name: "AGENT_BROWSER_AGENT_REF", Value: a.Name},
		{Name: "AGENT_MODEL_PROVIDER", Value: a.Spec.Model.Provider},
	}
	if tgt.cdpWsURL != "" {
		env = append(env, corev1.EnvVar{Name: "AGENT_CDP_WS_URL", Value: tgt.cdpWsURL})
	}
	if tgt.controllerURL != "" {
		env = append(env, corev1.EnvVar{Name: "AGENT_CONTROLLER_URL", Value: tgt.controllerURL})
	}
	if tgt.browserID != "" {
		env = append(env, corev1.EnvVar{Name: "AGENT_BROWSER_ID", Value: tgt.browserID})
	}
	if a.Spec.Model.Name != "" {
		env = append(env, corev1.EnvVar{Name: "AGENT_MODEL_NAME", Value: a.Spec.Model.Name})
	}
	if a.Spec.Model.BaseURL != "" {
		env = append(env, corev1.EnvVar{Name: "AGENT_MODEL_BASE_URL", Value: a.Spec.Model.BaseURL})
	}
	if e := secretEnv("AGENT_MODEL_API_KEY", a.Spec.Model.APIKeySecret); e != nil {
		env = append(env, *e)
	}

	if a.Spec.Control != nil {
		if a.Spec.Control.URL != "" {
			env = append(env, corev1.EnvVar{Name: "AGENT_CONTROL_URL", Value: a.Spec.Control.URL})
		}
		if e := secretEnv("AGENT_CONTROL_TOKEN", a.Spec.Control.TokenSecret); e != nil {
			env = append(env, *e)
		}
	}

	if a.Spec.Artifacts != nil {
		if a.Spec.Artifacts.Endpoint != "" {
			env = append(env, corev1.EnvVar{Name: "AGENT_ARTIFACT_ENDPOINT", Value: a.Spec.Artifacts.Endpoint})
		}
		if a.Spec.Artifacts.Bucket != "" {
			env = append(env, corev1.EnvVar{Name: "AGENT_ARTIFACT_BUCKET", Value: a.Spec.Artifacts.Bucket})
		}
		if e := secretEnv("AGENT_ARTIFACT_ACCESS_KEY", a.Spec.Artifacts.AccessKeySecret); e != nil {
			env = append(env, *e)
		}
		if e := secretEnv("AGENT_ARTIFACT_SECRET_KEY", a.Spec.Artifacts.SecretKeySecret); e != nil {
			env = append(env, *e)
		}
	}

	recording := a.Spec.Recording == nil || a.Spec.Recording.Enabled == nil || *a.Spec.Recording.Enabled
	env = append(env, corev1.EnvVar{Name: "AGENT_RECORDING_ENABLED", Value: fmt.Sprintf("%t", recording)})

	env = append(env, defaultEnv...)
	env = append(env, a.Spec.Env...)
	return env
}

func applyAgentDeploymentSpec(deploy *appsv1.Deployment, a *browserv1.BrowserAgent, tgt resolvedTarget, defaultImg, pullPolicy string, defaultEnv []corev1.EnvVar, defaultRes *browserv1.ResourcesSpec) {
	if defaultImg == "" {
		defaultImg = defaultAgentImage
	}
	image := a.Spec.Image
	if image == "" {
		image = defaultImg
	}
	if pullPolicy == "" {
		pullPolicy = "IfNotPresent"
	}

	replicas := int32(1)
	if !agentWorkloadWanted(a) {
		replicas = 0
	}

	lbls := agentLabels(a.Name)
	sel := agentSelectorLabels(a.Name)

	requests := corev1.ResourceList{
		corev1.ResourceCPU:    resource.MustParse("250m"),
		corev1.ResourceMemory: resource.MustParse("512Mi"),
	}
	limits := corev1.ResourceList{
		corev1.ResourceCPU:    resource.MustParse("1"),
		corev1.ResourceMemory: resource.MustParse("1Gi"),
	}
	applyResourcesOverride(requests, limits, defaultRes)
	applyResourcesOverride(requests, limits, a.Spec.Resources)

	var pullSecrets []corev1.LocalObjectReference
	for _, s := range a.Spec.ImagePullSecrets {
		pullSecrets = append(pullSecrets, corev1.LocalObjectReference{Name: s})
	}

	deploy.Labels = lbls
	deploy.Spec = appsv1.DeploymentSpec{
		Replicas: &replicas,
		Strategy: appsv1.DeploymentStrategy{Type: appsv1.RollingUpdateDeploymentStrategyType},
		Selector: &metav1.LabelSelector{MatchLabels: sel},
		Template: corev1.PodTemplateSpec{
			ObjectMeta: metav1.ObjectMeta{Labels: mergePodLabels(lbls, a.Spec.PodLabels)},
			Spec: corev1.PodSpec{
				ImagePullSecrets: pullSecrets,
				Containers: []corev1.Container{
					{
						Name:            "agent",
						Image:           image,
						ImagePullPolicy: corev1.PullPolicy(pullPolicy),
						Ports: []corev1.ContainerPort{
							{Name: "http", ContainerPort: int32(agentPort)},
						},
						Env: buildAgentEnv(a, tgt, defaultEnv),
						Resources: corev1.ResourceRequirements{
							Requests: requests,
							Limits:   limits,
						},
						ReadinessProbe: &corev1.Probe{
							ProbeHandler: corev1.ProbeHandler{
								HTTPGet: &corev1.HTTPGetAction{
									Path: "/health/ready",
									Port: intstr.FromInt32(int32(agentPort)),
								},
							},
							InitialDelaySeconds: 5,
							PeriodSeconds:       10,
							TimeoutSeconds:      3,
							FailureThreshold:    6,
						},
						LivenessProbe: &corev1.Probe{
							ProbeHandler: corev1.ProbeHandler{
								HTTPGet: &corev1.HTTPGetAction{
									Path: "/health/ping",
									Port: intstr.FromInt32(int32(agentPort)),
								},
							},
							InitialDelaySeconds: 15,
							PeriodSeconds:       10,
							TimeoutSeconds:      5,
							FailureThreshold:    3,
						},
						StartupProbe: &corev1.Probe{
							ProbeHandler: corev1.ProbeHandler{
								HTTPGet: &corev1.HTTPGetAction{
									Path: "/health/ping",
									Port: intstr.FromInt32(int32(agentPort)),
								},
							},
							InitialDelaySeconds: 5,
							PeriodSeconds:       5,
							FailureThreshold:    30,
						},
					},
				},
			},
		},
	}
}

func applyAgentServiceSpec(svc *corev1.Service, a *browserv1.BrowserAgent) {
	lbls := agentLabels(a.Name)
	sel := agentSelectorLabels(a.Name)

	svc.Labels = lbls
	svc.Spec = corev1.ServiceSpec{
		Selector: sel,
		Type:     corev1.ServiceTypeClusterIP,
		Ports: []corev1.ServicePort{
			{Name: "http", Port: int32(agentPort), TargetPort: intstr.FromInt32(int32(agentPort))},
		},
	}
}
