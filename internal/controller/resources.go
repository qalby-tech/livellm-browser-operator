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
	launcherPort    = 9000
	vncPort         = 5901
	novncPort       = 6901
	profileMountDir = "/home/headless/Desktop/app/profiles"
	defaultImage    = "kamasalyamov/livellm-browser:2.0.1"
	defaultStorage  = "1Gi"
	defaultShmSize  = "4Gi"

	// Must match browser image Dockerfile (USER headless → UID/GID 1000).
	// PVC mounts are often root:root without fsGroup; headless cannot mkdir profiles/default otherwise.
	headlessUID int64 = 1000
	headlessGID int64 = 1000
)

// labels returns the standard label set for all child resources.
func labels(name string) map[string]string {
	return map[string]string{
		"app.kubernetes.io/name":       "livellm-browser",
		"app.kubernetes.io/instance":   name,
		"app.kubernetes.io/managed-by": "livellm-operator",
		"livellm.io/browser":           name,
	}
}

// selectorLabels returns the minimal labels used by Deployment selector & Service.
func selectorLabels(name string) map[string]string {
	return map[string]string{
		"livellm.io/browser": name,
	}
}

// browserWorkloadWanted is true when spec.running is nil or true; false when explicitly false.
func browserWorkloadWanted(browser *browserv1.Browser) bool {
	if browser.Spec.Running == nil {
		return true
	}
	return *browser.Spec.Running
}

// ────────────────────────────────────────────────────────────
// PVC
// ────────────────────────────────────────────────────────────

func buildPVC(browser *browserv1.Browser) *corev1.PersistentVolumeClaim {
	storage := browser.Spec.Storage
	if storage == "" {
		storage = defaultStorage
	}

	return &corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{
			Name:      fmt.Sprintf("%s-profile", browser.Name),
			Namespace: browser.Namespace,
			Labels:    labels(browser.Name),
		},
		Spec: corev1.PersistentVolumeClaimSpec{
			AccessModes: []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
			Resources: corev1.VolumeResourceRequirements{
				Requests: corev1.ResourceList{
					corev1.ResourceStorage: resource.MustParse(storage),
				},
			},
		},
	}
}

// ────────────────────────────────────────────────────────────
// Deployment
// ────────────────────────────────────────────────────────────

// applyDeploymentSpec sets the desired spec on an existing or new Deployment object.
// Used inside controllerutil.CreateOrUpdate's mutate function.
func applyDeploymentSpec(deploy *appsv1.Deployment, browser *browserv1.Browser, defaultImg string, pullPolicy string, redisURL string, defaultEnv []corev1.EnvVar) {
	if defaultImg == "" {
		defaultImg = defaultImage
	}
	image := browser.Spec.Image
	if image == "" {
		image = defaultImg
	}
	if pullPolicy == "" {
		pullPolicy = "IfNotPresent"
	}
	shmSize := browser.Spec.ShmSize
	if shmSize == "" {
		shmSize = defaultShmSize
	}

	replicas := int32(1)
	if !browserWorkloadWanted(browser) {
		replicas = 0
	}
	lbls := labels(browser.Name)
	sel := selectorLabels(browser.Name)

	// Resource requirements
	requests := corev1.ResourceList{
		corev1.ResourceCPU:    resource.MustParse("500m"),
		corev1.ResourceMemory: resource.MustParse("2Gi"),
	}
	limits := corev1.ResourceList{
		corev1.ResourceCPU:    resource.MustParse("1"),
		corev1.ResourceMemory: resource.MustParse("4Gi"),
	}
	if browser.Spec.Resources != nil {
		if v, ok := browser.Spec.Resources.Requests["cpu"]; ok {
			requests[corev1.ResourceCPU] = resource.MustParse(v)
		}
		if v, ok := browser.Spec.Resources.Requests["memory"]; ok {
			requests[corev1.ResourceMemory] = resource.MustParse(v)
		}
		if v, ok := browser.Spec.Resources.Limits["cpu"]; ok {
			limits[corev1.ResourceCPU] = resource.MustParse(v)
		}
		if v, ok := browser.Spec.Resources.Limits["memory"]; ok {
			limits[corev1.ResourceMemory] = resource.MustParse(v)
		}
	}

	deploy.Labels = lbls
	deploy.Spec = appsv1.DeploymentSpec{
		Replicas: &replicas,
		Strategy: appsv1.DeploymentStrategy{Type: appsv1.RecreateDeploymentStrategyType},
		Selector: &metav1.LabelSelector{MatchLabels: sel},
		Template: corev1.PodTemplateSpec{
			ObjectMeta: metav1.ObjectMeta{Labels: lbls},
			Spec: corev1.PodSpec{
				SecurityContext: &corev1.PodSecurityContext{
					RunAsUser:  int64Ptr(headlessUID),
					RunAsGroup: int64Ptr(headlessGID),
					FSGroup:    int64Ptr(headlessGID),
				},
				Containers: []corev1.Container{
					{
						Name:            "browser",
						Image:           image,
						ImagePullPolicy: corev1.PullPolicy(pullPolicy),
						Ports: []corev1.ContainerPort{
							{Name: "vnc", ContainerPort: vncPort},
							{Name: "novnc", ContainerPort: novncPort},
							{Name: "launcher", ContainerPort: int32(launcherPort)},
						},
						Env: buildBrowserEnv(redisURL, defaultEnv, browser.Spec.Env),
						Resources: corev1.ResourceRequirements{
							Requests: requests,
							Limits:   limits,
						},
						VolumeMounts: []corev1.VolumeMount{
							{Name: "profile-data", MountPath: profileMountDir},
							{Name: "dshm", MountPath: "/dev/shm"},
						},
						ReadinessProbe: &corev1.Probe{
							ProbeHandler: corev1.ProbeHandler{
								HTTPGet: &corev1.HTTPGetAction{
									Path: "/health",
									Port: intstr.FromInt32(int32(launcherPort)),
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
									Path: "/health",
									Port: intstr.FromInt32(int32(launcherPort)),
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
									Path: "/browsers",
									Port: intstr.FromInt32(int32(launcherPort)),
								},
							},
							InitialDelaySeconds: 5,
							PeriodSeconds:       5,
							FailureThreshold:    60,
						},
					},
				},
				Volumes: []corev1.Volume{
					{
						Name: "profile-data",
						VolumeSource: corev1.VolumeSource{
							PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{
								ClaimName: fmt.Sprintf("%s-profile", browser.Name),
							},
						},
					},
					{
						Name: "dshm",
						VolumeSource: corev1.VolumeSource{
							EmptyDir: &corev1.EmptyDirVolumeSource{
								Medium:    corev1.StorageMediumMemory,
								SizeLimit: resourcePtr(resource.MustParse(shmSize)),
							},
						},
					},
				},
			},
		},
	}
}

// ────────────────────────────────────────────────────────────
// Service
// ────────────────────────────────────────────────────────────

// applyServiceSpec sets the desired spec on an existing or new Service object.
func applyServiceSpec(svc *corev1.Service, browser *browserv1.Browser) {
	lbls := labels(browser.Name)
	sel := selectorLabels(browser.Name)

	svc.Labels = lbls
	svc.Spec = corev1.ServiceSpec{
		Selector: sel,
		Type:     corev1.ServiceTypeClusterIP,
		Ports: []corev1.ServicePort{
			{Name: "launcher", Port: int32(launcherPort), TargetPort: intstr.FromInt32(int32(launcherPort))},
			{Name: "vnc", Port: vncPort, TargetPort: intstr.FromInt32(vncPort)},
			{Name: "novnc", Port: novncPort, TargetPort: intstr.FromInt32(novncPort)},
		},
	}
}

// ────────────────────────────────────────────────────────────
// Helpers
// ────────────────────────────────────────────────────────────

func buildBrowserEnv(redisURL string, defaultEnv []corev1.EnvVar, extraEnv []corev1.EnvVar) []corev1.EnvVar {
	// NODE_OPTIONS sized for the in-pod Playwright/patchright Node driver.
	// Last-write-wins — overridable via spec.env or DEFAULT_BROWSER_ENV.
	env := []corev1.EnvVar{
		{Name: "VNC_PW", Value: "headless"},
		{Name: "VNC_RESOLUTION", Value: "1920x1080"},
		{Name: "DISPLAY", Value: ":1"},
		{Name: "REDIS_URL", Value: redisURL},
		{Name: "NODE_OPTIONS", Value: "--max-old-space-size=4096"},
		{
			Name: "POD_IP",
			ValueFrom: &corev1.EnvVarSource{
				FieldRef: &corev1.ObjectFieldSelector{FieldPath: "status.podIP"},
			},
		},
	}
	env = append(env, defaultEnv...)
	env = append(env, extraEnv...)
	return env
}

func resourcePtr(q resource.Quantity) *resource.Quantity {
	return &q
}

func int64Ptr(v int64) *int64 {
	return &v
}

// isPodReady returns true if all containers in the pod are ready.
func isPodReady(pod *corev1.Pod) bool {
	for _, c := range pod.Status.Conditions {
		if c.Type == corev1.PodReady && c.Status == corev1.ConditionTrue {
			return true
		}
	}
	return false
}
