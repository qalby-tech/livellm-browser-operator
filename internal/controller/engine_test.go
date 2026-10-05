package controller

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	browserv1 "github.com/livellm/browser-operator/api/v1alpha1"
)

func camoufoxBrowser() *browserv1.Browser {
	b := controlBrowser(true)
	b.Spec.Engine = browserv1.EngineCamoufox
	b.Spec.Extensions = []string{"dknlfmjaanfblgfdfebhijalfmhmjjjo"}
	return b
}

func portNames(ports []corev1.ContainerPort) []string {
	out := []string{}
	for _, p := range ports {
		out = append(out, p.Name)
	}
	return out
}

func TestCamoufoxBrowserRender(t *testing.T) {
	b := camoufoxBrowser()
	d, s := renderBrowser(b)
	pod := d.Spec.Template.Spec
	c := pod.Containers[0]

	if got, want := portNames(c.Ports), []string{"vnc", "novnc", "launcher", "playwright"}; !reflect.DeepEqual(got, want) {
		t.Errorf("container ports %v, want %v", got, want)
	}
	for _, p := range c.Ports {
		if p.Name == "playwright" && p.ContainerPort != 9222 {
			t.Errorf("playwright port %d", p.ContainerPort)
		}
	}
	if v, ok := findEnv(c.Env, "AUTOMATION_PORT"); !ok || v != "9222" {
		t.Errorf("AUTOMATION_PORT %q %v", v, ok)
	}
	for _, name := range []string{"CDP_PORT", "BROWSER_EXTENSIONS"} {
		if _, ok := findEnv(c.Env, name); ok {
			t.Errorf("%s rendered for a camoufox browser", name)
		}
	}
	// The rest of the browser env is the same as chrome's.
	if v, _ := findEnv(c.Env, "BROWSER_PROXY_SERVER"); v != "http://127.0.0.1:3128" {
		t.Errorf("BROWSER_PROXY_SERVER %q", v)
	}

	if len(pod.InitContainers) != 1 {
		t.Fatalf("initContainers %d", len(pod.InitContainers))
	}
	k := pod.InitContainers[0]
	if v, ok := findEnv(k.Env, "KEEPER_ENGINE"); !ok || v != "camoufox" {
		t.Errorf("KEEPER_ENGINE %q %v", v, ok)
	}
	if v, _ := findEnv(k.Env, "KEEPER_RELAY"); v != "required" {
		t.Errorf("KEEPER_RELAY %q", v)
	}
	if k.Image != c.Image {
		t.Errorf("sidecar image %q, browser %q", k.Image, c.Image)
	}

	svcPorts := []string{}
	for _, p := range s.Spec.Ports {
		svcPorts = append(svcPorts, p.Name)
		if p.Name == "playwright" && (p.Port != 9222 || p.TargetPort.IntValue() != 9222) {
			t.Errorf("service playwright port %+v", p)
		}
	}
	if want := []string{"launcher", "vnc", "novnc", "playwright", "keeper"}; !reflect.DeepEqual(svcPorts, want) {
		t.Errorf("service ports %v, want %v", svcPorts, want)
	}

	if got, want := browserWsURL(b, "b1"), "ws://ws-b1.tenant-ws.svc.cluster.local:9222/playwright/default"; got != want {
		t.Errorf("wsUrl %q, want %q", got, want)
	}
	if got := browserMessage(b, "Browser is ready"); !strings.Contains(got, "take no extensions") {
		t.Errorf("message %q lacks the extensions note", got)
	}
	b.Spec.Extensions = nil
	if got := browserMessage(b, "Browser is ready"); got != "Browser is ready" {
		t.Errorf("message %q", got)
	}
}

// A chrome browser keeps every byte: the keeper gets no KEEPER_ENGINE and the
// ports stay cdp/CDP_PORT, and an explicit "chrome" renders as an absent engine.
func TestChromeEngineRendersAsAbsent(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("testdata", "live", "browsers.json"))
	if err != nil {
		t.Fatal(err)
	}
	var browsers []browserv1.Browser
	if err := json.Unmarshal(raw, &browsers); err != nil {
		t.Fatal(err)
	}
	browsers = append(browsers, *controlBrowser(true), *controlBrowser(false))
	for i := range browsers {
		absent := browsers[i].DeepCopy()
		absent.Spec.Engine = ""
		chrome := absent.DeepCopy()
		chrome.Spec.Engine = browserv1.EngineChrome

		d1, s1 := renderBrowser(absent)
		d2, s2 := renderBrowser(chrome)
		if !reflect.DeepEqual(d1, d2) || !reflect.DeepEqual(s1, s2) {
			t.Errorf("%s: engine chrome renders differently from no engine", absent.Name)
		}
		if _, ok := findEnv(d1.Spec.Template.Spec.Containers[0].Env, "CDP_PORT"); !ok {
			t.Errorf("%s: CDP_PORT missing", absent.Name)
		}
		for _, k := range d1.Spec.Template.Spec.InitContainers {
			if _, ok := findEnv(k.Env, "KEEPER_ENGINE"); ok {
				t.Errorf("%s: chrome keeper has KEEPER_ENGINE", absent.Name)
			}
		}
		if got := browserWsURL(chrome, "p"); got != "ws://"+absent.Name+"."+absent.Namespace+".svc.cluster.local:9222/devtools/browser/p" {
			t.Errorf("chrome wsUrl %q", got)
		}
		if got := browserMessage(chrome, "Browser is ready"); got != "Browser is ready" {
			t.Errorf("chrome message %q", got)
		}
	}
}

func newBrowserReconciler(t *testing.T, objs ...client.Object) *BrowserReconciler {
	t.Helper()
	scheme := runtime.NewScheme()
	for _, add := range []func(*runtime.Scheme) error{browserv1.AddToScheme, corev1.AddToScheme, appsv1.AddToScheme} {
		if err := add(scheme); err != nil {
			t.Fatal(err)
		}
	}
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(objs...).
		WithStatusSubresource(&browserv1.Browser{}, &browserv1.Controller{}).Build()
	return &BrowserReconciler{
		Client: c, Scheme: scheme,
		DefaultBrowserImage: "chrome:1", DefaultBrowserPullPolicy: "Always",
	}
}

func reconcileBrowser(t *testing.T, r *BrowserReconciler, b *browserv1.Browser) {
	t.Helper()
	req := ctrl.Request{NamespacedName: types.NamespacedName{Name: b.Name, Namespace: b.Namespace}}
	if _, err := r.Reconcile(context.Background(), req); err != nil {
		t.Fatal(err)
	}
}

func TestCamoufoxNotOffered(t *testing.T) {
	b := camoufoxBrowser()
	b.Spec.Extensions = nil
	r := newBrowserReconciler(t, b)
	reconcileBrowser(t, r, b)

	ctx := context.Background()
	key := types.NamespacedName{Name: b.Name, Namespace: b.Namespace}
	for _, obj := range []client.Object{&appsv1.Deployment{}, &corev1.Service{}} {
		if err := r.Get(ctx, key, obj); !apierrors.IsNotFound(err) {
			t.Errorf("%T rendered for a camoufox browser without an image (err %v)", obj, err)
		}
	}
	var pvc corev1.PersistentVolumeClaim
	if err := r.Get(ctx, types.NamespacedName{Name: b.Name + "-profile", Namespace: b.Namespace}, &pvc); !apierrors.IsNotFound(err) {
		t.Errorf("profile disk created for a camoufox browser without an image (err %v)", err)
	}
	var got browserv1.Browser
	if err := r.Get(ctx, key, &got); err != nil {
		t.Fatal(err)
	}
	if got.Status.Message != "Camoufox isn't offered on this platform" || got.Status.WsURL != "" {
		t.Errorf("status %+v", got.Status)
	}

	// Offered: the camoufox default image (and its pull policy) is used.
	r.DefaultCamoufoxImage, r.DefaultCamoufoxPullPolicy = "camoufox:1", "IfNotPresent"
	reconcileBrowser(t, r, b)
	var d appsv1.Deployment
	if err := r.Get(ctx, key, &d); err != nil {
		t.Fatal(err)
	}
	c := d.Spec.Template.Spec.Containers[0]
	if c.Image != "camoufox:1" || c.ImagePullPolicy != corev1.PullIfNotPresent {
		t.Errorf("camoufox image %q %q", c.Image, c.ImagePullPolicy)
	}
	if k := d.Spec.Template.Spec.InitContainers[0]; k.Image != "camoufox:1" {
		t.Errorf("sidecar image %q", k.Image)
	}

	// spec.image alone is enough, even with no camoufox default.
	b2 := camoufoxBrowser()
	b2.Name, b2.Spec.Image = "own", "rc-camoufox:1"
	r2 := newBrowserReconciler(t, b2)
	reconcileBrowser(t, r2, b2)
	if err := r2.Get(ctx, types.NamespacedName{Name: "own", Namespace: b2.Namespace}, &d); err != nil {
		t.Fatal(err)
	}
	if img := d.Spec.Template.Spec.Containers[0].Image; img != "rc-camoufox:1" {
		t.Errorf("spec.image %q", img)
	}

	// A chrome browser still gets the chrome default, camoufox offered or not.
	ch := controlBrowser(false)
	ch.Name = "chrome"
	r3 := newBrowserReconciler(t, ch)
	r3.DefaultCamoufoxImage = "camoufox:1"
	reconcileBrowser(t, r3, ch)
	if err := r3.Get(ctx, types.NamespacedName{Name: "chrome", Namespace: ch.Namespace}, &d); err != nil {
		t.Fatal(err)
	}
	if c := d.Spec.Template.Spec.Containers[0]; c.Image != "chrome:1" || c.ImagePullPolicy != corev1.PullAlways {
		t.Errorf("chrome image %q %q", c.Image, c.ImagePullPolicy)
	}
}

func engineBrowser(name, engine string) *browserv1.Browser {
	b := newBrowser(name, name, "ws://"+name)
	b.Spec.Engine = engine
	return b
}

// A pool holds browsers of both engines: autodiscover and named members
// register every engine, each entry in its own engine's shape, and remote
// browsers sit next to Camoufox members.
func TestCollectBrowsersMixedPool(t *testing.T) {
	no := false
	r := newReconciler(t,
		engineBrowser("c1", ""),
		engineBrowser("c2", "chrome"),
		engineBrowser("f1", "camoufox"),
		engineBrowser("f2", "camoufox"),
	)
	fox := func(id string) map[string]interface{} {
		return map[string]interface{}{"wsUrl": "ws://" + id, "engine": "camoufox"}
	}
	remote := []browserv1.ExternalBrowser{
		{ID: "cloud", WsURL: "wss://a", AuthHeader: "Bearer x"},
		{ID: "open", WsURL: "wss://b"},
	}
	cases := []struct {
		name        string
		auto        *bool
		list        []string
		ext         []browserv1.ExternalBrowser
		want        []string
		wantEntries map[string]interface{}
	}{
		{"autodiscover registers both engines", nil, nil, nil, []string{"c1", "c2", "f1", "f2"},
			map[string]interface{}{"c1": "ws://c1", "c2": "ws://c2", "f1": fox("f1"), "f2": fox("f2")}},
		{"named members of either engine", &no, []string{"f2", "c1"}, nil, []string{"f2", "c1"},
			map[string]interface{}{"c1": "ws://c1", "f2": fox("f2")}},
		{"remote browsers next to a camoufox member", &no, []string{"f1"}, remote, []string{"f1", "cloud", "open"},
			map[string]interface{}{
				"f1":    fox("f1"),
				"cloud": map[string]interface{}{"wsUrl": "wss://a", "headers": map[string]string{"Authorization": "Bearer x"}},
				"open":  "wss://b",
			}},
	}
	for _, c := range cases {
		cr := &browserv1.Controller{
			ObjectMeta: metav1.ObjectMeta{Name: "api", Namespace: "ns"},
			Spec:       browserv1.ControllerSpec{Autodiscover: c.auto, Browsers: c.list, ExternalBrowsers: c.ext},
		}
		entries, reg, notes, err := r.collectBrowsers(context.Background(), cr)
		if err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		if got := ids(reg); !reflect.DeepEqual(got, c.want) {
			t.Errorf("%s: registered %v, want %v", c.name, got, c.want)
		}
		if !reflect.DeepEqual(entries, c.wantEntries) {
			t.Errorf("%s: entries %#v, want %#v", c.name, entries, c.wantEntries)
		}
		if len(notes) != 0 {
			t.Errorf("%s: notes %q", c.name, notes)
		}
	}
}

func TestBrowsersRegistryEntryShapes(t *testing.T) {
	ctx := context.Background()
	cr := &browserv1.Controller{
		ObjectMeta: metav1.ObjectMeta{Name: "api", Namespace: "ns", UID: "u1"},
		Spec: browserv1.ControllerSpec{ExternalBrowsers: []browserv1.ExternalBrowser{
			{ID: "cloud", WsURL: "wss://a", AuthHeader: "Bearer x"},
		}},
	}
	r := newReconciler(t, cr, engineBrowser("c1", ""), engineBrowser("f1", "camoufox"))
	if err := r.ensureBrowsersRegistry(ctx, cr); err != nil {
		t.Fatal(err)
	}
	var sec corev1.Secret
	if err := r.Get(ctx, client.ObjectKey{Namespace: "ns", Name: "api-browsers"}, &sec); err != nil {
		t.Fatal(err)
	}
	want := `{"browsers":{"c1":"ws://c1","cloud":{"headers":{"Authorization":"Bearer x"},"wsUrl":"wss://a"},"f1":{"engine":"camoufox","wsUrl":"ws://f1"}}}`
	if got := string(sec.Data[browsersConfigFile]); got != want {
		t.Errorf("registry %s, want %s", got, want)
	}

	// A Chrome-only pool keeps plain strings, as before engines existed.
	ch := &browserv1.Controller{ObjectMeta: metav1.ObjectMeta{Name: "ch", Namespace: "ns", UID: "u2"}}
	r = newReconciler(t, ch, engineBrowser("c1", ""), engineBrowser("c2", "chrome"))
	if err := r.ensureBrowsersRegistry(ctx, ch); err != nil {
		t.Fatal(err)
	}
	if err := r.Get(ctx, client.ObjectKey{Namespace: "ns", Name: "ch-browsers"}, &sec); err != nil {
		t.Fatal(err)
	}
	if got := string(sec.Data[browsersConfigFile]); got != `{"browsers":{"c1":"ws://c1","c2":"ws://c2"}}` {
		t.Errorf("chrome registry %s", got)
	}
}

// The Browser API pod is the same whatever engines its members run: one
// image, no engine env.
func TestControllerDeploymentEngineBlind(t *testing.T) {
	render := func(objs ...client.Object) appsv1.Deployment {
		cr := &browserv1.Controller{ObjectMeta: metav1.ObjectMeta{Name: "api", Namespace: "ns"}}
		scheme := runtime.NewScheme()
		for _, add := range []func(*runtime.Scheme) error{browserv1.AddToScheme, corev1.AddToScheme, appsv1.AddToScheme} {
			if err := add(scheme); err != nil {
				t.Fatal(err)
			}
		}
		c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(append(objs, cr)...).
			WithStatusSubresource(&browserv1.Controller{}).Build()
		r := &ControllerReconciler{Client: c, APIReader: c, Scheme: scheme, DefaultControllerImage: "ctl:1", DefaultControllerPullPolicy: "Always"}
		key := types.NamespacedName{Name: "api", Namespace: "ns"}
		if _, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: key}); err != nil {
			t.Fatal(err)
		}
		var d appsv1.Deployment
		if err := r.Get(context.Background(), key, &d); err != nil {
			t.Fatal(err)
		}
		return d
	}
	chrome := render(engineBrowser("c1", ""))
	mixed := render(engineBrowser("c1", ""), engineBrowser("f1", "camoufox"))
	if !reflect.DeepEqual(chrome.Spec, mixed.Spec) {
		t.Error("a pool with a camoufox member renders a different Browser API pod")
	}
	ct := mixed.Spec.Template.Spec.Containers[0]
	if ct.Image != "ctl:1" || ct.ImagePullPolicy != corev1.PullAlways {
		t.Errorf("image %q %q", ct.Image, ct.ImagePullPolicy)
	}
	if v, ok := findEnv(ct.Env, "BROWSER_ENGINE"); ok {
		t.Errorf("BROWSER_ENGINE=%q rendered", v)
	}
}

// Autoscale makes Chrome browsers with the template's extensions, and only a
// Chrome member at the page limit triggers it: a Camoufox member at the limit
// does not (the new browser could not take its sessions).
func TestAutoscaleIsChrome(t *testing.T) {
	yes := true
	limit := int32(1)
	cr := &browserv1.Controller{
		ObjectMeta: metav1.ObjectMeta{Name: "api", Namespace: "ns", UID: "u"},
		Spec: browserv1.ControllerSpec{
			AutoscaleBrowser: &yes, MaxPagesPerBrowser: &limit,
			AutoscaleBrowserTemplate: &browserv1.AutoscaleBrowserTemplateSpec{Extensions: []string{"ext"}},
		},
	}
	ctx := context.Background()
	r := newReconciler(t, cr, engineBrowser("c1", ""), engineBrowser("f1", "camoufox"))
	entries, reg, _, err := r.collectBrowsers(ctx, cr)
	if err != nil {
		t.Fatal(err)
	}
	key := client.ObjectKey{Namespace: "ns", Name: "api-autoscale-1"}
	var got browserv1.Browser

	// Only the Camoufox member is at the limit: nothing is made.
	if err := r.autoscaleBrowsers(ctx, cr, reg, map[string]int{"f1": 1}, camoufoxMembers(entries)); err != nil {
		t.Fatal(err)
	}
	if err := r.Get(ctx, key, &got); !apierrors.IsNotFound(err) {
		t.Fatalf("a Camoufox member at the limit made %s (err %v)", key.Name, err)
	}

	// The Chrome member at the limit makes a Chrome browser.
	if err := r.autoscaleBrowsers(ctx, cr, reg, map[string]int{"c1": 1, "f1": 1}, camoufoxMembers(entries)); err != nil {
		t.Fatal(err)
	}
	if err := r.Get(ctx, key, &got); err != nil {
		t.Fatal(err)
	}
	if got.Spec.Engine != "" || !reflect.DeepEqual(got.Spec.Extensions, []string{"ext"}) {
		t.Errorf("autoscaled browser engine %q extensions %v", got.Spec.Engine, got.Spec.Extensions)
	}
}
