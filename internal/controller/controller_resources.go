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
	controllerPort         = 8000
	defaultControllerImage = "kamasalyamov/livellm-browser:controller-2.0.1"
)

func controllerLabels(name string) map[string]string {
	return map[string]string{
		"app.kubernetes.io/name":       "livellm-controller",
		"app.kubernetes.io/instance":   name,
		"app.kubernetes.io/managed-by": "livellm-operator",
		"livellm.io/controller":        name,
	}
}

func controllerSelectorLabels(name string) map[string]string {
	return map[string]string{
		"livellm.io/controller": name,
	}
}

// ────────────────────────────────────────────────────────────
// Controller Deployment
// ────────────────────────────────────────────────────────────

func applyControllerDeploymentSpec(deploy *appsv1.Deployment, ctrlCR *browserv1.Controller, defaultImg string, pullPolicy string, redisURL string, defaultEnv []corev1.EnvVar, defaultRes *browserv1.ResourcesSpec) {
	if defaultImg == "" {
		defaultImg = defaultControllerImage
	}
	image := ctrlCR.Spec.Image
	if image == "" {
		image = defaultImg
	}
	if pullPolicy == "" {
		pullPolicy = "IfNotPresent"
	}

	replicas := int32(1)
	if ctrlCR.Spec.Replicas != nil {
		replicas = *ctrlCR.Spec.Replicas
	}

	lbls := controllerLabels(ctrlCR.Name)
	sel := controllerSelectorLabels(ctrlCR.Name)

	requests := corev1.ResourceList{
		corev1.ResourceCPU:    resource.MustParse("250m"),
		corev1.ResourceMemory: resource.MustParse("512Mi"),
	}
	limits := corev1.ResourceList{
		corev1.ResourceCPU:    resource.MustParse("500m"),
		corev1.ResourceMemory: resource.MustParse("2Gi"),
	}
	applyResourcesOverride(requests, limits, defaultRes)
	applyResourcesOverride(requests, limits, ctrlCR.Spec.Resources)

	// NODE_OPTIONS scales from the pod's memory limit unless the user already
	// provides it in spec.env or DEFAULT_CONTROLLER_ENV, in which case theirs wins.
	nodeOptionsOverridden := envContains(defaultEnv, "NODE_OPTIONS") || envContains(ctrlCR.Spec.Env, "NODE_OPTIONS")
	heapMiB := nodeMaxOldSpaceMiB(limits[corev1.ResourceMemory])
	env := []corev1.EnvVar{
		{Name: "REDIS_URL", Value: redisURL},
	}
	if !nodeOptionsOverridden {
		env = append(env, corev1.EnvVar{Name: "NODE_OPTIONS", Value: fmt.Sprintf("--max-old-space-size=%d", heapMiB)})
	}
	env = append(env, defaultEnv...)
	if ctrlCR.Spec.MaxPagesPerBrowser != nil {
		env = append(env, corev1.EnvVar{
			Name:  "MAX_PAGES_PER_BROWSER",
			Value: fmt.Sprintf("%d", *ctrlCR.Spec.MaxPagesPerBrowser),
		})
	}
	env = append(env, ctrlCR.Spec.Env...)

	deploy.Labels = lbls
	deploy.Spec = appsv1.DeploymentSpec{
		Replicas: &replicas,
		Strategy: appsv1.DeploymentStrategy{Type: appsv1.RollingUpdateDeploymentStrategyType},
		Selector: &metav1.LabelSelector{MatchLabels: sel},
		Template: corev1.PodTemplateSpec{
			ObjectMeta: metav1.ObjectMeta{Labels: lbls},
			Spec: corev1.PodSpec{
				Containers: []corev1.Container{
					{
						Name:            "controller",
						Image:           image,
						ImagePullPolicy: corev1.PullPolicy(pullPolicy),
						Ports: []corev1.ContainerPort{
							{Name: "http", ContainerPort: int32(controllerPort)},
						},
						Env: env,
						Resources: corev1.ResourceRequirements{
							Requests: requests,
							Limits:   limits,
						},
						ReadinessProbe: &corev1.Probe{
							ProbeHandler: corev1.ProbeHandler{
								HTTPGet: &corev1.HTTPGetAction{
									Path: "/parser/ping",
									Port: intstr.FromInt32(int32(controllerPort)),
								},
							},
							InitialDelaySeconds: 5,
							PeriodSeconds:       5,
							TimeoutSeconds:      3,
							FailureThreshold:    6,
						},
						LivenessProbe: &corev1.Probe{
							ProbeHandler: corev1.ProbeHandler{
								HTTPGet: &corev1.HTTPGetAction{
									Path: "/parser/healthz",
									Port: intstr.FromInt32(int32(controllerPort)),
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
									Path: "/parser/ping",
									Port: intstr.FromInt32(int32(controllerPort)),
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

// ────────────────────────────────────────────────────────────
// Controller Service
// ────────────────────────────────────────────────────────────

func applyControllerServiceSpec(svc *corev1.Service, ctrlCR *browserv1.Controller) {
	lbls := controllerLabels(ctrlCR.Name)
	sel := controllerSelectorLabels(ctrlCR.Name)

	svc.Labels = lbls
	svc.Spec = corev1.ServiceSpec{
		Selector: sel,
		Type:     corev1.ServiceTypeClusterIP,
		Ports: []corev1.ServicePort{
			{Name: "http", Port: int32(controllerPort), TargetPort: intstr.FromInt32(int32(controllerPort))},
		},
	}
}
