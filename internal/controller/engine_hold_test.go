package controller

import (
	"context"
	"strings"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	browserv1 "github.com/livellm/browser-operator/api/v1alpha1"
)

// editBrowser reads the stored Browser, applies edit and writes it back.
func editBrowser(t *testing.T, c client.Client, key types.NamespacedName, edit func(*browserv1.Browser)) *browserv1.Browser {
	t.Helper()
	var b browserv1.Browser
	if err := c.Get(context.Background(), key, &b); err != nil {
		t.Fatal(err)
	}
	edit(&b)
	if err := c.Update(context.Background(), &b); err != nil {
		t.Fatal(err)
	}
	return &b
}

func getBrowser(t *testing.T, c client.Client, key types.NamespacedName) browserv1.Browser {
	t.Helper()
	var b browserv1.Browser
	if err := c.Get(context.Background(), key, &b); err != nil {
		t.Fatal(err)
	}
	return b
}

func getDeployment(t *testing.T, c client.Client, key types.NamespacedName) appsv1.Deployment {
	t.Helper()
	var d appsv1.Deployment
	if err := c.Get(context.Background(), key, &d); err != nil {
		t.Fatal(err)
	}
	return d
}

// A camoufox browser already running when the platform stops offering
// camoufox keeps its image and stays managed: a stop and env edits reach its
// Deployment, and the stopped message keeps the browser's notes.
func TestCamoufoxNotOfferedKeepsRunningBrowser(t *testing.T) {
	b := camoufoxBrowser() // carries extensions
	r := newBrowserReconciler(t, b)
	r.DefaultCamoufoxImage, r.DefaultCamoufoxPullPolicy = "camoufox:1", "IfNotPresent"
	key := types.NamespacedName{Name: b.Name, Namespace: b.Namespace}
	reconcileBrowser(t, r, b)

	r.DefaultCamoufoxImage, r.DefaultCamoufoxPullPolicy = "", ""
	b = editBrowser(t, r.Client, key, func(b *browserv1.Browser) {
		no := false
		b.Spec.Running = &no
		b.Spec.Env = []corev1.EnvVar{{Name: "EXTRA", Value: "1"}}
	})
	reconcileBrowser(t, r, b)

	d := getDeployment(t, r.Client, key)
	if d.Spec.Replicas == nil || *d.Spec.Replicas != 0 {
		t.Errorf("replicas %v, want 0 after a stop", d.Spec.Replicas)
	}
	c := d.Spec.Template.Spec.Containers[0]
	if c.Image != "camoufox:1" || c.ImagePullPolicy != corev1.PullIfNotPresent {
		t.Errorf("image %q %q, want the kept camoufox:1 IfNotPresent", c.Image, c.ImagePullPolicy)
	}
	if v, ok := findEnv(c.Env, "EXTRA"); !ok || v != "1" {
		t.Errorf("env edit not applied: %q %v", v, ok)
	}
	if _, ok := findEnv(c.Env, "AUTOMATION_PORT"); !ok {
		t.Error("kept browser lost AUTOMATION_PORT")
	}
	got := getBrowser(t, r.Client, key)
	if got.Status.Phase != browserv1.BrowserPhaseStopped {
		t.Errorf("phase %q", got.Status.Phase)
	}
	for _, want := range []string{"Scaled to zero (spec.running=false)", "Camoufox browsers take no extensions", keptImageNote} {
		if !strings.Contains(got.Status.Message, want) {
			t.Errorf("message %q lacks %q", got.Status.Message, want)
		}
	}

	// Started again: still the kept image, waiting for its pod, note kept.
	b = editBrowser(t, r.Client, key, func(b *browserv1.Browser) { b.Spec.Running = nil })
	reconcileBrowser(t, r, b)
	d = getDeployment(t, r.Client, key)
	if d.Spec.Replicas == nil || *d.Spec.Replicas != 1 {
		t.Errorf("replicas %v, want 1 after a start", d.Spec.Replicas)
	}
	if img := d.Spec.Template.Spec.Containers[0].Image; img != "camoufox:1" {
		t.Errorf("image %q", img)
	}
	got = getBrowser(t, r.Client, key)
	if !strings.HasPrefix(got.Status.Message, "Waiting for browser pod to be ready") || !strings.Contains(got.Status.Message, keptImageNote) {
		t.Errorf("message %q", got.Status.Message)
	}
}

// A Deployment that was not rendered for camoufox never lends its image to a
// camoufox browser the platform doesn't offer.
func TestCamoufoxNotOfferedIgnoresChromeDeployment(t *testing.T) {
	b := camoufoxBrowser()
	old := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: b.Name, Namespace: b.Namespace},
		Spec: appsv1.DeploymentSpec{Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{
			Containers: []corev1.Container{{Name: "browser", Image: "chrome:1",
				Env: []corev1.EnvVar{{Name: "CDP_PORT", Value: "9222"}}}},
		}}},
	}
	r := newBrowserReconciler(t, b, old)
	key := types.NamespacedName{Name: b.Name, Namespace: b.Namespace}
	reconcileBrowser(t, r, b)

	d := getDeployment(t, r.Client, key)
	if c := d.Spec.Template.Spec.Containers[0]; c.Image != "chrome:1" || len(c.Env) != 1 {
		t.Errorf("chrome-shaped Deployment changed: %q %v", c.Image, c.Env)
	}
	if got := getBrowser(t, r.Client, key); got.Status.Message != notOfferedMessage || got.Status.WsURL != "" {
		t.Errorf("status %+v", got.Status)
	}
}

// A chrome browser's stopped message is unchanged by engine notes.
func TestChromeStoppedMessageUnchanged(t *testing.T) {
	b := controlBrowser(false)
	b.Spec.Extensions = []string{"dknlfmjaanfblgfdfebhijalfmhmjjjo"}
	no := false
	b.Spec.Running = &no
	r := newBrowserReconciler(t, b)
	reconcileBrowser(t, r, b)
	got := getBrowser(t, r.Client, types.NamespacedName{Name: b.Name, Namespace: b.Namespace})
	if got.Status.Message != "Scaled to zero (spec.running=false)" {
		t.Errorf("message %q", got.Status.Message)
	}
}

// A camoufox profile disk records its engine; a chrome one carries no
// annotation, as before.
func TestProfileDiskEngineAnnotation(t *testing.T) {
	if a := buildPVC(controlBrowser(true)).Annotations; a != nil {
		t.Errorf("chrome PVC annotations %v", a)
	}
	b := controlBrowser(true)
	b.Spec.Engine = "chrome"
	if a := buildPVC(b).Annotations; a != nil {
		t.Errorf("engine chrome PVC annotations %v", a)
	}
	if a := buildPVC(camoufoxBrowser()).Annotations; a[engineAnnotation] != "camoufox" {
		t.Errorf("camoufox PVC annotations %v", a)
	}
}

// A browser whose engine was dropped from the spec is never started as
// chrome on its camoufox profile disk: Deployment and Service stay as they
// were, the automation address is cleared, and only a stop gets through.
func TestChromeOnCamoufoxDiskHeld(t *testing.T) {
	b := camoufoxBrowser()
	b.Spec.Extensions = nil
	r := newBrowserReconciler(t, b)
	r.DefaultCamoufoxImage = "camoufox:1"
	key := types.NamespacedName{Name: b.Name, Namespace: b.Namespace}
	reconcileBrowser(t, r, b)

	var pvc corev1.PersistentVolumeClaim
	if err := r.Get(context.Background(), types.NamespacedName{Name: b.Name + "-profile", Namespace: b.Namespace}, &pvc); err != nil {
		t.Fatal(err)
	}
	if pvc.Annotations[engineAnnotation] != "camoufox" {
		t.Fatalf("PVC annotations %v", pvc.Annotations)
	}
	// Pretend it ran: a wsUrl in status.
	got := getBrowser(t, r.Client, key)
	got.Status.WsURL, got.Status.PodName = "ws://x", "pod"
	if err := r.Status().Update(context.Background(), &got); err != nil {
		t.Fatal(err)
	}

	b = editBrowser(t, r.Client, key, func(b *browserv1.Browser) { b.Spec.Engine = "" })
	reconcileBrowser(t, r, b)

	d := getDeployment(t, r.Client, key)
	c := d.Spec.Template.Spec.Containers[0]
	if c.Image != "camoufox:1" {
		t.Errorf("image %q: chrome rendered on a camoufox disk", c.Image)
	}
	if _, ok := findEnv(c.Env, "CDP_PORT"); ok {
		t.Error("CDP_PORT rendered on a camoufox disk")
	}
	var s corev1.Service
	if err := r.Get(context.Background(), key, &s); err != nil {
		t.Fatal(err)
	}
	for _, p := range s.Spec.Ports {
		if p.Name == "cdp" {
			t.Error("Service switched to cdp on a camoufox disk")
		}
	}
	got = getBrowser(t, r.Client, key)
	if got.Status.Message != foreignDiskMessage || got.Status.WsURL != "" || got.Status.PodName != "" {
		t.Errorf("status %+v", got.Status)
	}

	b = editBrowser(t, r.Client, key, func(b *browserv1.Browser) {
		no := false
		b.Spec.Running = &no
	})
	reconcileBrowser(t, r, b)
	d = getDeployment(t, r.Client, key)
	if d.Spec.Replicas == nil || *d.Spec.Replicas != 0 {
		t.Errorf("replicas %v, want 0 after a stop", d.Spec.Replicas)
	}
	if img := d.Spec.Template.Spec.Containers[0].Image; img != "camoufox:1" {
		t.Errorf("image %q after a stop", img)
	}
	if got = getBrowser(t, r.Client, key); got.Status.Phase != browserv1.BrowserPhaseStopped {
		t.Errorf("phase %q", got.Status.Phase)
	}
}

// A chrome browser on an un-annotated disk renders as always (the guard
// reads only the camoufox annotation).
func TestChromeOnPlainDiskRenders(t *testing.T) {
	b := controlBrowser(false)
	disk := buildPVC(b)
	r := newBrowserReconciler(t, b, disk)
	reconcileBrowser(t, r, b)
	d := getDeployment(t, r.Client, types.NamespacedName{Name: b.Name, Namespace: b.Namespace})
	if img := d.Spec.Template.Spec.Containers[0].Image; img != "chrome:1" {
		t.Errorf("image %q", img)
	}
}

func newControllerReconcilerApps(t *testing.T, objs ...client.Object) *ControllerReconciler {
	t.Helper()
	scheme := runtime.NewScheme()
	for _, add := range []func(*runtime.Scheme) error{browserv1.AddToScheme, corev1.AddToScheme, appsv1.AddToScheme} {
		if err := add(scheme); err != nil {
			t.Fatal(err)
		}
	}
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(objs...).
		WithStatusSubresource(&browserv1.Browser{}, &browserv1.Controller{}).Build()
	return &ControllerReconciler{Client: c, APIReader: c, Scheme: scheme, DefaultControllerImage: "ctl:1"}
}

// A camoufox Browser API already running when camoufox stops being offered
// keeps its image, its registry stays current, and edits reach it.
func TestCamoufoxControllerNotOfferedKeepsRunning(t *testing.T) {
	cr := &browserv1.Controller{
		ObjectMeta: metav1.ObjectMeta{Name: "fox", Namespace: "ns"},
		Spec:       browserv1.ControllerSpec{Engine: "camoufox"},
	}
	r := newControllerReconcilerApps(t, cr)
	r.DefaultCamoufoxAPIImage = "fox-api:1"
	ctx := context.Background()
	key := types.NamespacedName{Name: "fox", Namespace: "ns"}
	if _, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: key}); err != nil {
		t.Fatal(err)
	}

	r.DefaultCamoufoxAPIImage = ""
	fox := engineBrowser("b1", "camoufox")
	fox.Namespace = "ns"
	fox.Status.WsURL = "ws://b1"
	if err := r.Create(ctx, fox); err != nil {
		t.Fatal(err)
	}
	var cur browserv1.Controller
	if err := r.Get(ctx, key, &cur); err != nil {
		t.Fatal(err)
	}
	cur.Spec.Env = []corev1.EnvVar{{Name: "EXTRA", Value: "1"}}
	if err := r.Update(ctx, &cur); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: key}); err != nil {
		t.Fatal(err)
	}

	var d appsv1.Deployment
	if err := r.Get(ctx, key, &d); err != nil {
		t.Fatal(err)
	}
	c := d.Spec.Template.Spec.Containers[0]
	if c.Image != "fox-api:1" {
		t.Errorf("image %q, want the kept fox-api:1", c.Image)
	}
	if v, ok := findEnv(c.Env, "EXTRA"); !ok || v != "1" {
		t.Errorf("env edit not applied: %q %v", v, ok)
	}
	var sec corev1.Secret
	if err := r.Get(ctx, types.NamespacedName{Name: "fox-browsers", Namespace: "ns"}, &sec); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(sec.Data[browsersConfigFile]), `"b1"`) {
		t.Errorf("registry not refreshed: %s", sec.Data[browsersConfigFile])
	}
	if err := r.Get(ctx, key, &cur); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(cur.Status.Message, "this Browser API keeps the image it runs") {
		t.Errorf("message %q", cur.Status.Message)
	}
}

// A camoufox Browser API with nothing rendered reports no registered
// browsers, whatever an earlier status said.
func TestCamoufoxControllerNotOfferedClearsRegistered(t *testing.T) {
	cr := &browserv1.Controller{
		ObjectMeta: metav1.ObjectMeta{Name: "fox", Namespace: "ns"},
		Spec:       browserv1.ControllerSpec{Engine: "camoufox"},
		Status: browserv1.ControllerStatus{
			Phase:                  browserv1.ControllerPhaseRunning,
			RegisteredBrowsers:     []browserv1.RegisteredBrowser{{Name: "b1", ProfileUID: "b1"}},
			RegisteredBrowserCount: 1,
		},
	}
	r := newControllerReconcilerApps(t, cr)
	ctx := context.Background()
	key := types.NamespacedName{Name: "fox", Namespace: "ns"}
	if _, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: key}); err != nil {
		t.Fatal(err)
	}
	var got browserv1.Controller
	if err := r.Get(ctx, key, &got); err != nil {
		t.Fatal(err)
	}
	if got.Status.Message != notOfferedMessage || len(got.Status.RegisteredBrowsers) != 0 || got.Status.RegisteredBrowserCount != 0 {
		t.Errorf("status %+v", got.Status)
	}
}

// An autoscaled camoufox browser gets no extensions from the template; a
// chrome one still does.
func TestAutoscaleCamoufoxSkipsExtensions(t *testing.T) {
	yes := true
	limit := int32(1)
	for _, engine := range []string{"", "camoufox"} {
		cr := &browserv1.Controller{
			ObjectMeta: metav1.ObjectMeta{Name: "api", Namespace: "ns", UID: "u"},
			Spec: browserv1.ControllerSpec{
				Engine: engine, AutoscaleBrowser: &yes, MaxPagesPerBrowser: &limit,
				AutoscaleBrowserTemplate: &browserv1.AutoscaleBrowserTemplateSpec{Extensions: []string{"ext"}},
			},
		}
		r := newReconciler(t, cr)
		reg := []browserv1.RegisteredBrowser{{Name: "b", ProfileUID: "b"}}
		if err := r.autoscaleBrowsers(context.Background(), cr, reg, map[string]int{"b": 1}); err != nil {
			t.Fatal(err)
		}
		var got browserv1.Browser
		if err := r.Get(context.Background(), client.ObjectKey{Namespace: "ns", Name: "api-autoscale-1"}, &got); err != nil {
			t.Fatal(err)
		}
		want := 1
		if engine == "camoufox" {
			want = 0
		}
		if len(got.Spec.Extensions) != want {
			t.Errorf("engine %q: extensions %v", engine, got.Spec.Extensions)
		}
	}
}
