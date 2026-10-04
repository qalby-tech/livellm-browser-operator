package controller

import (
	"reflect"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	browserv1 "github.com/livellm/browser-operator/api/v1alpha1"
)

func controlBrowser(proxy bool) *browserv1.Browser {
	b := &browserv1.Browser{
		ObjectMeta: metav1.ObjectMeta{Name: "ws-b1", Namespace: "tenant-ws"},
		Spec: browserv1.BrowserSpec{
			ProfileUID: "b1",
			Control:    &browserv1.ControlSpec{SecretName: "ws-b1-browser"},
		},
	}
	if proxy {
		b.Spec.Proxy = &browserv1.ProxySpec{Server: "http://127.0.0.1:3128"}
	}
	return b
}

func renderBrowser(b *browserv1.Browser) (*appsv1.Deployment, *corev1.Service) {
	var d appsv1.Deployment
	applyDeploymentSpec(&d, b, "img:1", "Always", nil, nil)
	var s corev1.Service
	applyServiceSpec(&s, b)
	return &d, &s
}

func findEnv(env []corev1.EnvVar, name string) (string, bool) {
	for _, e := range env {
		if e.Name == name {
			return e.Value, true
		}
	}
	return "", false
}

func TestKeeperSidecar(t *testing.T) {
	d, _ := renderBrowser(controlBrowser(false))
	pod := d.Spec.Template.Spec

	if len(pod.Containers) != 1 || pod.Containers[0].Name != "browser" {
		t.Fatalf("containers %v, want only browser", pod.Containers)
	}
	if len(pod.InitContainers) != 1 {
		t.Fatalf("initContainers %d, want 1", len(pod.InitContainers))
	}
	k := pod.InitContainers[0]
	if k.Name != "keeper" {
		t.Errorf("sidecar name %q", k.Name)
	}
	if k.RestartPolicy == nil || *k.RestartPolicy != corev1.ContainerRestartPolicyAlways {
		t.Errorf("sidecar restartPolicy %v, want Always", k.RestartPolicy)
	}
	if k.Image != "img:1" || k.ImagePullPolicy != corev1.PullAlways {
		t.Errorf("sidecar image %q/%q, want the browser's", k.Image, k.ImagePullPolicy)
	}
	if !reflect.DeepEqual(k.Command, []string{"/usr/local/bin/livellm-keeper"}) {
		t.Errorf("command %v", k.Command)
	}
	if k.ReadinessProbe != nil {
		t.Error("sidecar must have no readiness probe")
	}
	if k.StartupProbe == nil || k.StartupProbe.TCPSocket == nil || k.StartupProbe.TCPSocket.Port.IntValue() != 9300 ||
		k.StartupProbe.PeriodSeconds != 1 || k.StartupProbe.FailureThreshold != 30 {
		t.Errorf("startup probe %+v", k.StartupProbe)
	}
	if k.LivenessProbe == nil || k.LivenessProbe.HTTPGet == nil || k.LivenessProbe.HTTPGet.Path != "/healthz" ||
		k.LivenessProbe.HTTPGet.Port.IntValue() != 9300 || k.LivenessProbe.PeriodSeconds != 20 || k.LivenessProbe.FailureThreshold != 3 {
		t.Errorf("liveness probe %+v", k.LivenessProbe)
	}
	if len(k.Ports) != 1 || k.Ports[0].Name != "keeper" || k.Ports[0].ContainerPort != 9300 {
		t.Errorf("ports %v", k.Ports)
	}
	sc := k.SecurityContext
	if sc == nil || sc.RunAsUser == nil || *sc.RunAsUser != 1000 || sc.RunAsGroup == nil || *sc.RunAsGroup != 1000 ||
		sc.ReadOnlyRootFilesystem == nil || !*sc.ReadOnlyRootFilesystem ||
		sc.AllowPrivilegeEscalation == nil || *sc.AllowPrivilegeEscalation ||
		sc.Capabilities == nil || !reflect.DeepEqual(sc.Capabilities.Drop, []corev1.Capability{"ALL"}) {
		t.Errorf("sidecar securityContext %+v", sc)
	}
	req, lim := k.Resources.Requests, k.Resources.Limits
	if req.Cpu().String() != "25m" || req.Memory().String() != "48Mi" || lim.Cpu().String() != "300m" || lim.Memory().String() != "192Mi" {
		t.Errorf("sidecar resources %v / %v", req, lim)
	}
	if v, _ := findEnv(k.Env, "KEEPER_LAUNCHER"); v != "http://127.0.0.1:9000" {
		t.Errorf("KEEPER_LAUNCHER %q", v)
	}
	if pod.ShareProcessNamespace != nil {
		t.Error("shareProcessNamespace must stay unset")
	}
}

func TestKeeperVolumesOnlyInSidecar(t *testing.T) {
	d, _ := renderBrowser(controlBrowser(true))
	pod := d.Spec.Template.Spec

	vols := map[string]corev1.Volume{}
	for _, v := range pod.Volumes {
		vols[v.Name] = v
	}
	sec, ok := vols["keeper-secret"]
	if !ok || sec.Secret == nil || sec.Secret.SecretName != "ws-b1-browser" ||
		sec.Secret.Optional == nil || !*sec.Secret.Optional ||
		sec.Secret.DefaultMode == nil || *sec.Secret.DefaultMode != 0o440 {
		t.Errorf("keeper-secret volume %+v", sec)
	}
	run, ok := vols["keeper-run"]
	if !ok || run.EmptyDir == nil || run.EmptyDir.Medium != corev1.StorageMediumMemory ||
		run.EmptyDir.SizeLimit == nil || run.EmptyDir.SizeLimit.String() != "8Mi" {
		t.Errorf("keeper-run volume %+v", run)
	}

	mounts := func(c corev1.Container) map[string]string {
		m := map[string]string{}
		for _, vm := range c.VolumeMounts {
			m[vm.Name] = vm.MountPath
		}
		return m
	}
	km := mounts(pod.InitContainers[0])
	want := map[string]string{
		"profile-data":  "/home/headless/Desktop/app/profiles",
		"keeper-secret": "/etc/livellm/keeper",
		"keeper-run":    "/run/keeper",
	}
	if !reflect.DeepEqual(km, want) {
		t.Errorf("sidecar mounts %v, want %v", km, want)
	}
	bm := mounts(pod.Containers[0])
	for _, n := range []string{"keeper-secret", "keeper-run"} {
		if _, ok := bm[n]; ok {
			t.Errorf("browser container mounts %s", n)
		}
	}
	for _, p := range bm {
		if p == "/etc/livellm/keeper" || p == "/run/keeper" {
			t.Errorf("browser container mounts %s", p)
		}
	}
}

func TestKeeperBrowserSecurityContext(t *testing.T) {
	d, _ := renderBrowser(controlBrowser(false))
	sc := d.Spec.Template.Spec.Containers[0].SecurityContext
	if sc == nil || sc.AllowPrivilegeEscalation == nil || *sc.AllowPrivilegeEscalation ||
		sc.Capabilities == nil || !reflect.DeepEqual(sc.Capabilities.Drop, []corev1.Capability{"ALL"}) {
		t.Errorf("browser securityContext %+v", sc)
	}
}

func TestKeeperServicePort(t *testing.T) {
	_, s := renderBrowser(controlBrowser(false))
	var found bool
	for _, p := range s.Spec.Ports {
		if p.Name == "keeper" {
			found = p.Port == 9300 && p.TargetPort.IntValue() == 9300 && p.Protocol == corev1.ProtocolTCP
		}
	}
	if !found {
		t.Errorf("service ports %v, want keeper 9300/TCP", s.Spec.Ports)
	}
	if n := len(s.Spec.Ports); n != 5 || s.Spec.Ports[n-1].Name != "keeper" {
		t.Errorf("keeper port must be appended last, got %v", s.Spec.Ports)
	}
}

func TestKeeperRelayOnlyWithProxy(t *testing.T) {
	d, _ := renderBrowser(controlBrowser(false))
	if _, ok := findEnv(d.Spec.Template.Spec.InitContainers[0].Env, "KEEPER_RELAY"); ok {
		t.Error("KEEPER_RELAY without spec.proxy")
	}

	d, _ = renderBrowser(controlBrowser(true))
	pod := d.Spec.Template.Spec
	if v, _ := findEnv(pod.InitContainers[0].Env, "KEEPER_RELAY"); v != "required" {
		t.Errorf("KEEPER_RELAY %q, want required", v)
	}
	// The browser points at the relay, with no credentials.
	if v, _ := findEnv(pod.Containers[0].Env, "BROWSER_PROXY_SERVER"); v != "http://127.0.0.1:3128" {
		t.Errorf("BROWSER_PROXY_SERVER %q", v)
	}
	for _, n := range []string{"BROWSER_PROXY_USERNAME", "BROWSER_PROXY_PASSWORD", "BROWSER_PROXY_BYPASS", "KEEPER_RELAY", "KEEPER_LAUNCHER"} {
		if _, ok := findEnv(pod.Containers[0].Env, n); ok {
			t.Errorf("browser container has %s", n)
		}
	}
}

// An empty proxy server (the CRD allows it) gives the browser no proxy, so
// the relay must not be required either: both follow spec.proxy.server.
func TestKeeperRelayFollowsProxyServer(t *testing.T) {
	b := controlBrowser(true)
	b.Spec.Proxy.Server = ""
	d, _ := renderBrowser(b)
	pod := d.Spec.Template.Spec
	if _, ok := findEnv(pod.InitContainers[0].Env, "KEEPER_RELAY"); ok {
		t.Error("KEEPER_RELAY with an empty spec.proxy.server")
	}
	if _, ok := findEnv(pod.Containers[0].Env, "BROWSER_PROXY_SERVER"); ok {
		t.Error("BROWSER_PROXY_SERVER with an empty spec.proxy.server")
	}
}

// Without spec.control nothing of the sidecar renders, even with a proxy:
// the existing spec.proxy behaviour is kept as is.
func TestNoControlNoSidecar(t *testing.T) {
	b := &browserv1.Browser{
		ObjectMeta: metav1.ObjectMeta{Name: "b", Namespace: "ns"},
		Spec: browserv1.BrowserSpec{
			Proxy: &browserv1.ProxySpec{Server: "http://p:8080", Username: "u", Password: "pw", Bypass: "x"},
		},
	}
	d, s := renderBrowser(b)
	pod := d.Spec.Template.Spec
	if pod.InitContainers != nil {
		t.Errorf("initContainers %v", pod.InitContainers)
	}
	if pod.Containers[0].SecurityContext != nil {
		t.Errorf("browser securityContext %+v", pod.Containers[0].SecurityContext)
	}
	for _, v := range pod.Volumes {
		if v.Name == "keeper-secret" || v.Name == "keeper-run" {
			t.Errorf("volume %s", v.Name)
		}
	}
	if len(s.Spec.Ports) != 4 {
		t.Errorf("service ports %v", s.Spec.Ports)
	}
	for _, kv := range [][2]string{
		{"BROWSER_PROXY_SERVER", "http://p:8080"},
		{"BROWSER_PROXY_USERNAME", "u"},
		{"BROWSER_PROXY_PASSWORD", "pw"},
		{"BROWSER_PROXY_BYPASS", "x"},
	} {
		if v, _ := findEnv(pod.Containers[0].Env, kv[0]); v != kv[1] {
			t.Errorf("%s = %q, want %q", kv[0], v, kv[1])
		}
	}
}

// Turning control off again renders the plain pod (no leftovers from a
// previous mutate on the same object).
func TestControlOffAfterOn(t *testing.T) {
	b := controlBrowser(true)
	var d appsv1.Deployment
	applyDeploymentSpec(&d, b, "img:1", "Always", nil, nil)
	b.Spec.Control = nil
	b.Spec.Proxy = nil
	applyDeploymentSpec(&d, b, "img:1", "Always", nil, nil)
	var fresh appsv1.Deployment
	applyDeploymentSpec(&fresh, b, "img:1", "Always", nil, nil)
	if !reflect.DeepEqual(d.Spec, fresh.Spec) {
		t.Error("render after control off differs from a fresh render")
	}
}
