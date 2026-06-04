package controller

import (
	"encoding/json"
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
	cdpPort         = 9222
	profileMountDir = "/home/headless/Desktop/app/profiles"
	cookiesMountDir = "/etc/livellm/cookies"
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
// mergePodLabels returns a copy of base with extra merged on top. Used for the
// pod template only (never the Deployment metadata or the immutable selector),
// so a Browser/Controller spec.podLabels change rolls the pods cleanly.
func mergePodLabels(base, extra map[string]string) map[string]string {
	out := make(map[string]string, len(base)+len(extra))
	for k, v := range base {
		out[k] = v
	}
	for k, v := range extra {
		out[k] = v
	}
	return out
}

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
func applyDeploymentSpec(deploy *appsv1.Deployment, browser *browserv1.Browser, defaultImg string, pullPolicy string, defaultEnv []corev1.EnvVar, defaultRes *browserv1.ResourcesSpec) {
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

	// Resource requirements: spec.Resources beats chart-default beats hard-default.
	requests := corev1.ResourceList{
		corev1.ResourceCPU:    resource.MustParse("500m"),
		corev1.ResourceMemory: resource.MustParse("2Gi"),
	}
	limits := corev1.ResourceList{
		corev1.ResourceCPU:    resource.MustParse("1"),
		corev1.ResourceMemory: resource.MustParse("4Gi"),
	}
	applyResourcesOverride(requests, limits, defaultRes)
	applyResourcesOverride(requests, limits, browser.Spec.Resources)

	volumeMounts := []corev1.VolumeMount{
		{Name: "profile-data", MountPath: profileMountDir},
		{Name: "dshm", MountPath: "/dev/shm"},
	}
	volumes := []corev1.Volume{
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
	}
	// Mount the cookies ConfigMap/Secret (if any) so the browser loads them at
	// boot via BROWSER_COOKIES_FILE — replaces the old Redis desired-state path.
	if vol, mount := cookiesVolume(browser); vol != nil {
		volumes = append(volumes, *vol)
		volumeMounts = append(volumeMounts, *mount)
	}

	deploy.Labels = lbls
	deploy.Spec = appsv1.DeploymentSpec{
		Replicas: &replicas,
		Strategy: appsv1.DeploymentStrategy{Type: appsv1.RecreateDeploymentStrategyType},
		Selector: &metav1.LabelSelector{MatchLabels: sel},
		Template: corev1.PodTemplateSpec{
			ObjectMeta: metav1.ObjectMeta{Labels: mergePodLabels(lbls, browser.Spec.PodLabels)},
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
							{Name: "cdp", ContainerPort: cdpPort},
						},
						Env: buildBrowserEnv(browser, defaultEnv, browser.Spec.Env),
						Resources: corev1.ResourceRequirements{
							Requests: requests,
							Limits:   limits,
						},
						VolumeMounts: volumeMounts,
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
									Path: "/health",
									Port: intstr.FromInt32(int32(launcherPort)),
								},
							},
							InitialDelaySeconds: 15,
							PeriodSeconds:       10,
							FailureThreshold:    30,
						},
					},
				},
				Volumes: volumes,
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
			{Name: "cdp", Port: cdpPort, TargetPort: intstr.FromInt32(cdpPort)},
		},
	}
}

// ────────────────────────────────────────────────────────────
// Helpers
// ────────────────────────────────────────────────────────────

func buildBrowserEnv(browser *browserv1.Browser, defaultEnv []corev1.EnvVar, extraEnv []corev1.EnvVar) []corev1.EnvVar {
	// NB: deliberately no NODE_OPTIONS default here — the browser pod hosts
	// Chrome itself, which must keep the bulk of the pod's memory budget.
	// Setting --max-old-space-size to anything large would let V8 starve
	// Chrome and cause kubelet OOMKills. If a user really needs to tune the
	// in-pod Node driver heap, they can pass NODE_OPTIONS via spec.env or
	// DEFAULT_BROWSER_ENV.
	env := []corev1.EnvVar{
		{Name: "VNC_PW", Value: "headless"},
		{Name: "VNC_RESOLUTION", Value: "1920x1080"},
		{Name: "DISPLAY", Value: ":1"},
		// Pin the in-pod CDP proxy to a fixed port; the Service targets it, so
		// the controller's ws_url is deterministic (one browser per pod).
		{Name: "CDP_PORT", Value: fmt.Sprintf("%d", cdpPort)},
	}

	// Desired state passed declaratively (replaces the Redis desired-state
	// channel): the browser reads these at startup.
	if len(browser.Spec.Extensions) > 0 {
		if data, err := json.Marshal(browser.Spec.Extensions); err == nil {
			env = append(env, corev1.EnvVar{Name: "BROWSER_EXTENSIONS", Value: string(data)})
		}
	}
	if browser.Spec.Proxy != nil && browser.Spec.Proxy.Server != "" {
		env = append(env, corev1.EnvVar{Name: "BROWSER_PROXY_SERVER", Value: browser.Spec.Proxy.Server})
		if browser.Spec.Proxy.Username != "" {
			env = append(env, corev1.EnvVar{Name: "BROWSER_PROXY_USERNAME", Value: browser.Spec.Proxy.Username})
		}
		if browser.Spec.Proxy.Password != "" {
			env = append(env, corev1.EnvVar{Name: "BROWSER_PROXY_PASSWORD", Value: browser.Spec.Proxy.Password})
		}
		if browser.Spec.Proxy.Bypass != "" {
			env = append(env, corev1.EnvVar{Name: "BROWSER_PROXY_BYPASS", Value: browser.Spec.Proxy.Bypass})
		}
	}
	if browser.Spec.Cookies != nil {
		env = append(env, corev1.EnvVar{
			Name:  "BROWSER_COOKIES_FILE",
			Value: fmt.Sprintf("%s/%s", cookiesMountDir, cookiesKey(browser)),
		})
	}

	env = append(env, defaultEnv...)
	env = append(env, extraEnv...)
	return env
}

// cookiesKey returns the data key within the cookies ConfigMap/Secret.
func cookiesKey(browser *browserv1.Browser) string {
	c := browser.Spec.Cookies
	if c == nil {
		return "cookies.json"
	}
	if c.ConfigMapRef != nil && c.ConfigMapRef.Key != "" {
		return c.ConfigMapRef.Key
	}
	if c.SecretRef != nil && c.SecretRef.Key != "" {
		return c.SecretRef.Key
	}
	return "cookies.json"
}

// cookiesVolume builds a read-only volume + mount for the cookies source, or
// (nil, nil) when the browser has no cookies configured.
func cookiesVolume(browser *browserv1.Browser) (*corev1.Volume, *corev1.VolumeMount) {
	c := browser.Spec.Cookies
	if c == nil {
		return nil, nil
	}
	vol := &corev1.Volume{Name: "cookies"}
	switch {
	case c.ConfigMapRef != nil:
		vol.VolumeSource = corev1.VolumeSource{
			ConfigMap: &corev1.ConfigMapVolumeSource{
				LocalObjectReference: corev1.LocalObjectReference{Name: c.ConfigMapRef.Name},
			},
		}
	case c.SecretRef != nil:
		vol.VolumeSource = corev1.VolumeSource{
			Secret: &corev1.SecretVolumeSource{SecretName: c.SecretRef.Name},
		}
	default:
		return nil, nil
	}
	mount := &corev1.VolumeMount{Name: "cookies", MountPath: cookiesMountDir, ReadOnly: true}
	return vol, mount
}

func resourcePtr(q resource.Quantity) *resource.Quantity {
	return &q
}

// applyResourcesOverride mutates requests/limits in-place from an override.
// Missing fields in the override are kept as-is.
func applyResourcesOverride(requests, limits corev1.ResourceList, override *browserv1.ResourcesSpec) {
	if override == nil {
		return
	}
	if v, ok := override.Requests["cpu"]; ok {
		requests[corev1.ResourceCPU] = resource.MustParse(v)
	}
	if v, ok := override.Requests["memory"]; ok {
		requests[corev1.ResourceMemory] = resource.MustParse(v)
	}
	if v, ok := override.Limits["cpu"]; ok {
		limits[corev1.ResourceCPU] = resource.MustParse(v)
	}
	if v, ok := override.Limits["memory"]; ok {
		limits[corev1.ResourceMemory] = resource.MustParse(v)
	}
}

// nodeMaxOldSpaceMiB returns min(memLimit/2, 4096) MiB, floored at 512 MiB.
// Used to size --max-old-space-size for the Node-only controller pod.
func nodeMaxOldSpaceMiB(memLimit resource.Quantity) int64 {
	const (
		hardCapMiB = int64(4096)
		floorMiB   = int64(512)
	)
	bytes := memLimit.Value()
	if bytes <= 0 {
		return hardCapMiB
	}
	halfMiB := bytes / (2 * 1024 * 1024)
	if halfMiB > hardCapMiB {
		return hardCapMiB
	}
	if halfMiB < floorMiB {
		return floorMiB
	}
	return halfMiB
}

func int64Ptr(v int64) *int64 {
	return &v
}

func boolPtr(v bool) *bool {
	return &v
}

func envContains(env []corev1.EnvVar, name string) bool {
	for _, e := range env {
		if e.Name == name {
			return true
		}
	}
	return false
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
