package controller

import (
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

func applyControllerDeploymentSpec(deploy *appsv1.Deployment, ctrlCR *browserv1.Controller, defaultImg string) {
	if defaultImg == "" {
		defaultImg = defaultControllerImage
	}
	image := ctrlCR.Spec.Image
	if image == "" {
		image = defaultImg
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
		corev1.ResourceMemory: resource.MustParse("1Gi"),
	}
	if ctrlCR.Spec.Resources != nil {
		if v, ok := ctrlCR.Spec.Resources.Requests["cpu"]; ok {
			requests[corev1.ResourceCPU] = resource.MustParse(v)
		}
		if v, ok := ctrlCR.Spec.Resources.Requests["memory"]; ok {
			requests[corev1.ResourceMemory] = resource.MustParse(v)
		}
		if v, ok := ctrlCR.Spec.Resources.Limits["cpu"]; ok {
			limits[corev1.ResourceCPU] = resource.MustParse(v)
		}
		if v, ok := ctrlCR.Spec.Resources.Limits["memory"]; ok {
			limits[corev1.ResourceMemory] = resource.MustParse(v)
		}
	}

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
						Name:  "controller",
						Image: image,
						Ports: []corev1.ContainerPort{
							{Name: "http", ContainerPort: int32(controllerPort)},
						},
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
							InitialDelaySeconds: 10,
							PeriodSeconds:       5,
							TimeoutSeconds:      3,
							FailureThreshold:    6,
						},
						LivenessProbe: &corev1.Probe{
							ProbeHandler: corev1.ProbeHandler{
								HTTPGet: &corev1.HTTPGetAction{
									Path: "/parser/ping",
									Port: intstr.FromInt32(int32(controllerPort)),
								},
							},
							InitialDelaySeconds: 20,
							PeriodSeconds:       15,
							TimeoutSeconds:      5,
							FailureThreshold:    3,
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
