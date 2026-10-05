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

func TestCollectBrowsersEngineFilter(t *testing.T) {
	no := false
	r := newReconciler(t,
		engineBrowser("c1", ""),
		engineBrowser("c2", "chrome"),
		engineBrowser("f1", "camoufox"),
		engineBrowser("f2", "camoufox"),
	)
	ctx := context.Background()
	cases := []struct {
		name      string
		engine    string
		auto      *bool
		list      []string
		want      []string
		wantNotes []string
	}{
		{"chrome autodiscover sees chrome only", "", nil, nil, []string{"c1", "c2"}, nil},
		{"explicit chrome is chrome", "chrome", nil, nil, []string{"c1", "c2"}, nil},
		{"camoufox autodiscover sees camoufox only", "camoufox", nil, nil, []string{"f1", "f2"}, nil},
		{"chrome named members: camoufox left out", "", &no, []string{"c1", "f1"}, []string{"c1"},
			[]string{"browser f1 runs Camoufox; this Browser API drives Chrome browsers, so it is left out"}},
		{"camoufox named members: chrome left out", "camoufox", &no, []string{"f2", "c2", "c2"}, []string{"f2"},
			[]string{"browser c2 runs Chrome; this Browser API drives Camoufox browsers, so it is left out"}},
		{"named mismatch noted with autodiscover on", "camoufox", nil, []string{"c1"}, []string{"f1", "f2"},
			[]string{"browser c1 runs Chrome; this Browser API drives Camoufox browsers, so it is left out"}},
	}
	for _, c := range cases {
		cr := &browserv1.Controller{
			ObjectMeta: metav1.ObjectMeta{Name: "api", Namespace: "ns"},
			Spec:       browserv1.ControllerSpec{Engine: c.engine, Autodiscover: c.auto, Browsers: c.list},
		}
		entries, reg, notes, err := r.collectBrowsers(ctx, cr)
		if err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		if got := ids(reg); !reflect.DeepEqual(got, c.want) {
			t.Errorf("%s: registered %v, want %v", c.name, got, c.want)
		}
		if len(entries) != len(c.want) {
			t.Errorf("%s: entries %v", c.name, entries)
		}
		if !reflect.DeepEqual(notes, c.wantNotes) {
			t.Errorf("%s: notes %q, want %q", c.name, notes, c.wantNotes)
		}
	}
}

func TestBrowsersRegistryEntryShapes(t *testing.T) {
	no := false
	ctx := context.Background()

	// Chrome: plain strings, unchanged.
	ch := &browserv1.Controller{
		ObjectMeta: metav1.ObjectMeta{Name: "api", Namespace: "ns", UID: "u1"},
		Spec:       browserv1.ControllerSpec{Autodiscover: &no, Browsers: []string{"c1"}},
	}
	r := newReconciler(t, ch, engineBrowser("c1", ""), engineBrowser("f1", "camoufox"))
	if err := r.ensureBrowsersRegistry(ctx, ch); err != nil {
		t.Fatal(err)
	}
	var sec corev1.Secret
	if err := r.Get(ctx, client.ObjectKey{Namespace: "ns", Name: "api-browsers"}, &sec); err != nil {
		t.Fatal(err)
	}
	if got := string(sec.Data[browsersConfigFile]); got != `{"browsers":{"c1":"ws://c1"}}` {
		t.Errorf("chrome registry %s", got)
	}

	// Camoufox: {wsUrl, engine}.
	cf := &browserv1.Controller{
		ObjectMeta: metav1.ObjectMeta{Name: "fox", Namespace: "ns", UID: "u2"},
		Spec:       browserv1.ControllerSpec{Engine: "camoufox"},
	}
	r = newReconciler(t, cf, engineBrowser("c1", ""), engineBrowser("f1", "camoufox"))
	if err := r.ensureBrowsersRegistry(ctx, cf); err != nil {
		t.Fatal(err)
	}
	if err := r.Get(ctx, client.ObjectKey{Namespace: "ns", Name: "fox-browsers"}, &sec); err != nil {
		t.Fatal(err)
	}
	if got := string(sec.Data[browsersConfigFile]); got != `{"browsers":{"f1":{"engine":"camoufox","wsUrl":"ws://f1"}}}` {
		t.Errorf("camoufox registry %s", got)
	}
}

func TestCamoufoxControllerIgnoresRemoteBrowsers(t *testing.T) {
	r := newReconciler(t, engineBrowser("f1", "camoufox"))
	for _, c := range []struct {
		ext  []browserv1.ExternalBrowser
		note string
	}{
		{[]browserv1.ExternalBrowser{{ID: "cloud", WsURL: "wss://a", AuthHeader: "Bearer x"}},
			"the remote browser is left out: remote browsers go only in a Chrome Browser API"},
		{[]browserv1.ExternalBrowser{{ID: "a", WsURL: "wss://a"}, {ID: "b", WsURL: "wss://b"}},
			"the 2 remote browsers are left out: remote browsers go only in a Chrome Browser API"},
	} {
		cr := &browserv1.Controller{
			ObjectMeta: metav1.ObjectMeta{Name: "fox", Namespace: "ns"},
			Spec:       browserv1.ControllerSpec{Engine: "camoufox", ExternalBrowsers: c.ext},
		}
		entries, reg, notes, err := r.collectBrowsers(context.Background(), cr)
		if err != nil {
			t.Fatal(err)
		}
		if got := ids(reg); !reflect.DeepEqual(got, []string{"f1"}) || len(entries) != 1 {
			t.Errorf("registered %v entries %v", got, entries)
		}
		if !reflect.DeepEqual(notes, []string{c.note}) {
			t.Errorf("notes %q, want %q", notes, c.note)
		}
	}
}

func TestAutoscaleCopiesEngine(t *testing.T) {
	yes := true
	limit := int32(1)
	for _, engine := range []string{"", "camoufox"} {
		cr := &browserv1.Controller{
			ObjectMeta: metav1.ObjectMeta{Name: "api", Namespace: "ns", UID: "u"},
			Spec: browserv1.ControllerSpec{
				Engine: engine, AutoscaleBrowser: &yes, MaxPagesPerBrowser: &limit,
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
		if got.Spec.Engine != engine {
			t.Errorf("controller engine %q: autoscaled browser engine %q", engine, got.Spec.Engine)
		}
	}
}

func TestControllerImageAndEnvByEngine(t *testing.T) {
	r := &ControllerReconciler{
		DefaultControllerImage: "ctl:1", DefaultControllerPullPolicy: "Always",
		DefaultCamoufoxAPIImage: "fox-api:1", DefaultCamoufoxAPIPullPolicy: "IfNotPresent",
	}
	for _, c := range []struct {
		engine, spec, wantImg, wantPP string
		wantEnv                       bool
	}{
		{"", "", "ctl:1", "Always", false},
		{"chrome", "", "ctl:1", "Always", false},
		{"camoufox", "", "fox-api:1", "IfNotPresent", true},
		{"camoufox", "own:2", "own:2", "IfNotPresent", true},
	} {
		cr := &browserv1.Controller{
			ObjectMeta: metav1.ObjectMeta{Name: "api", Namespace: "ns"},
			Spec:       browserv1.ControllerSpec{Engine: c.engine, Image: c.spec},
		}
		img, pp := r.controllerImage(cr)
		var d appsv1.Deployment
		applyControllerDeploymentSpec(&d, cr, img, pp, nil, nil)
		ct := d.Spec.Template.Spec.Containers[0]
		if ct.Image != c.wantImg || string(ct.ImagePullPolicy) != c.wantPP {
			t.Errorf("engine %q: image %q %q", c.engine, ct.Image, ct.ImagePullPolicy)
		}
		v, ok := findEnv(ct.Env, "BROWSER_ENGINE")
		if ok != c.wantEnv || (ok && v != "camoufox") {
			t.Errorf("engine %q: BROWSER_ENGINE %q %v", c.engine, v, ok)
		}
	}

	// Explicit chrome renders exactly as absent.
	var d1, d2 appsv1.Deployment
	cr := &browserv1.Controller{ObjectMeta: metav1.ObjectMeta{Name: "api", Namespace: "ns"}}
	applyControllerDeploymentSpec(&d1, cr, "ctl:1", "Always", nil, nil)
	cr.Spec.Engine = "chrome"
	applyControllerDeploymentSpec(&d2, cr, "ctl:1", "Always", nil, nil)
	if !reflect.DeepEqual(d1, d2) {
		t.Error("engine chrome controller renders differently from no engine")
	}
}

func TestCamoufoxControllerNotOffered(t *testing.T) {
	cr := &browserv1.Controller{
		ObjectMeta: metav1.ObjectMeta{Name: "fox", Namespace: "ns"},
		Spec:       browserv1.ControllerSpec{Engine: "camoufox"},
	}
	scheme := runtime.NewScheme()
	for _, add := range []func(*runtime.Scheme) error{browserv1.AddToScheme, corev1.AddToScheme, appsv1.AddToScheme} {
		if err := add(scheme); err != nil {
			t.Fatal(err)
		}
	}
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(cr).
		WithStatusSubresource(&browserv1.Controller{}).Build()
	r := &ControllerReconciler{Client: c, APIReader: c, Scheme: scheme, DefaultControllerImage: "ctl:1"}
	ctx := context.Background()
	key := types.NamespacedName{Name: "fox", Namespace: "ns"}
	if _, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: key}); err != nil {
		t.Fatal(err)
	}
	for _, obj := range []client.Object{&appsv1.Deployment{}, &corev1.Service{}} {
		if err := r.Get(ctx, key, obj); !apierrors.IsNotFound(err) {
			t.Errorf("%T rendered for a camoufox controller without an image (err %v)", obj, err)
		}
	}
	var sec corev1.Secret
	if err := r.Get(ctx, types.NamespacedName{Name: "fox-browsers", Namespace: "ns"}, &sec); !apierrors.IsNotFound(err) {
		t.Errorf("registry written for a camoufox controller without an image (err %v)", err)
	}
	var got browserv1.Controller
	if err := r.Get(ctx, key, &got); err != nil {
		t.Fatal(err)
	}
	if got.Status.Message != "Camoufox isn't offered on this platform" {
		t.Errorf("status %+v", got.Status)
	}

	r.DefaultCamoufoxAPIImage = "fox-api:1"
	if _, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: key}); err != nil {
		t.Fatal(err)
	}
	var d appsv1.Deployment
	if err := r.Get(ctx, key, &d); err != nil {
		t.Fatal(err)
	}
	if img := d.Spec.Template.Spec.Containers[0].Image; img != "fox-api:1" {
		t.Errorf("image %q", img)
	}
}
