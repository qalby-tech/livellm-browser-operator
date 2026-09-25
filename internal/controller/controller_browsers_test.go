package controller

import (
	"context"
	"reflect"
	"strings"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	browserv1 "github.com/livellm/browser-operator/api/v1alpha1"
)

func TestParseAuthHeader(t *testing.T) {
	cases := []struct {
		in   string
		want map[string]string
	}{
		{"", nil},
		{"   ", nil},
		{"Bearer abc", map[string]string{"Authorization": "Bearer abc"}},
		{"Bearer user:pass", map[string]string{"Authorization": "Bearer user:pass"}},
		{"Basic dXNlcjpwYXNz", map[string]string{"Authorization": "Basic dXNlcjpwYXNz"}},
		{"X-Api-Key: k", map[string]string{"X-Api-Key": "k"}},
		{"Authorization: Bearer abc", map[string]string{"Authorization": "Bearer abc"}},
		{"X-Api-Key:", nil},
		{":abc", map[string]string{"Authorization": ":abc"}},
		{"sk_live:abc", map[string]string{"Authorization": "sk_live:abc"}},
	}
	for _, c := range cases {
		if got := parseAuthHeader(c.in); !reflect.DeepEqual(got, c.want) {
			t.Errorf("parseAuthHeader(%q) = %v, want %v", c.in, got, c.want)
		}
	}
}

func TestDecodeControllerLoads(t *testing.T) {
	body := `[{"browser_id":"agent-1","connected":true,"healthy":true,"open_tabs":3,"session_count":1},
	          {"browser_id":"agent-2","connected":false,"healthy":true,"open_tabs":0,"session_count":0}]`
	got, ok := decodeControllerLoads(strings.NewReader(body))
	if !ok {
		t.Fatal("decode failed")
	}
	want := map[string]browserLoad{"agent-1": {OpenTabs: 3, Sessions: 1}, "agent-2": {}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
	if _, ok := decodeControllerLoads(strings.NewReader("not json")); ok {
		t.Error("bad body decoded")
	}
}

func newBrowser(name, profile, ws string) *browserv1.Browser {
	return &browserv1.Browser{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "ns"},
		Spec:       browserv1.BrowserSpec{ProfileUID: profile},
		Status:     browserv1.BrowserStatus{WsURL: ws},
	}
}

func newReconciler(t *testing.T, objs ...client.Object) *ControllerReconciler {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := browserv1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(objs...).Build()
	return &ControllerReconciler{Client: c, APIReader: c, Scheme: scheme}
}

func ids(reg []browserv1.RegisteredBrowser) []string {
	out := []string{}
	for _, b := range reg {
		out = append(out, b.ProfileUID)
	}
	return out
}

func TestCollectBrowsersAutodiscover(t *testing.T) {
	no, yes := false, true
	r := newReconciler(t,
		newBrowser("agent-1", "agent-1", "ws://agent-1"),
		newBrowser("agent-2", "agent-2", "ws://agent-2"),
	)
	cases := []struct {
		name string
		auto *bool
		list []string
		want []string
	}{
		{"nil means every browser (platform controller)", nil, nil, []string{"agent-1", "agent-2"}},
		{"true means every browser", &yes, nil, []string{"agent-1", "agent-2"}},
		{"explicit false drives only the named ones", &no, []string{"agent-2"}, []string{"agent-2"}},
		{"explicit false with none named drives none", &no, nil, []string{}},
	}
	for _, c := range cases {
		cr := &browserv1.Controller{
			ObjectMeta: metav1.ObjectMeta{Name: "api", Namespace: "ns"},
			Spec:       browserv1.ControllerSpec{Autodiscover: c.auto, Browsers: c.list},
		}
		entries, reg, notes, err := r.collectBrowsers(context.Background(), cr)
		if err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		if got := ids(reg); !reflect.DeepEqual(got, c.want) {
			t.Errorf("%s: registered %v, want %v", c.name, got, c.want)
		}
		if len(entries) != len(c.want) || len(notes) != 0 {
			t.Errorf("%s: entries %v notes %v", c.name, entries, notes)
		}
		for _, b := range reg {
			if b.WsURL != "" {
				t.Errorf("%s: status carries an address for %s", c.name, b.Name)
			}
		}
	}
}

func TestCollectBrowsersDuplicateIDs(t *testing.T) {
	r := newReconciler(t,
		newBrowser("agent-1", "default", "ws://agent-1"),
		newBrowser("agent-2", "default", "ws://agent-2"),
	)
	cr := &browserv1.Controller{
		ObjectMeta: metav1.ObjectMeta{Name: "api", Namespace: "ns"},
		Spec: browserv1.ControllerSpec{
			ExternalBrowsers: []browserv1.ExternalBrowser{{ID: "default", WsURL: "wss://remote"}},
		},
	}
	entries, reg, notes, err := r.collectBrowsers(context.Background(), cr)
	if err != nil {
		t.Fatal(err)
	}
	if len(reg) != 1 || reg[0].Name != "agent-1" || entries["default"] != "ws://agent-1" {
		t.Errorf("registered %v entries %v", reg, entries)
	}
	want := []string{
		"browser agent-1 and browser agent-2 share id default; only browser agent-1 is used",
		"browser agent-1 and remote browser 1 share id default; only browser agent-1 is used",
	}
	if !reflect.DeepEqual(notes, want) {
		t.Errorf("notes %q, want %q", notes, want)
	}
}

func TestCollectBrowsersRemoteAuth(t *testing.T) {
	sec := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "ws-api-remote", Namespace: "ns",
			Labels: map[string]string{remoteAuthLabel: "true"}},
		Data: map[string][]byte{"cloud": []byte("Bearer from-secret")},
	}
	r := newReconciler(t, sec)
	ref := func(key string) *corev1.SecretKeySelector {
		return &corev1.SecretKeySelector{LocalObjectReference: corev1.LocalObjectReference{Name: "ws-api-remote"}, Key: key}
	}
	no := false
	cr := &browserv1.Controller{
		ObjectMeta: metav1.ObjectMeta{Name: "api", Namespace: "ns"},
		Spec: browserv1.ControllerSpec{
			Autodiscover: &no,
			ExternalBrowsers: []browserv1.ExternalBrowser{
				{ID: "cloud", WsURL: "wss://a", AuthHeader: "Bearer inline", AuthHeaderSecretRef: ref("cloud")},
				{ID: "inline", WsURL: "wss://b", AuthHeader: "Bearer abc"},
				{ID: "gone", WsURL: "wss://c", AuthHeaderSecretRef: ref("gone")},
				{ID: "plain", WsURL: "wss://d"},
			},
		},
	}
	entries, reg, notes, err := r.collectBrowsers(context.Background(), cr)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]interface{}{
		"cloud":  map[string]interface{}{"wsUrl": "wss://a", "headers": map[string]string{"Authorization": "Bearer from-secret"}},
		"inline": map[string]interface{}{"wsUrl": "wss://b", "headers": map[string]string{"Authorization": "Bearer abc"}},
		"gone":   "wss://c",
		"plain":  "wss://d",
	}
	if !reflect.DeepEqual(entries, want) {
		t.Errorf("entries %v, want %v", entries, want)
	}
	if len(notes) != 1 || !strings.Contains(notes[0], "remote browser gone") {
		t.Errorf("notes %q", notes)
	}
	for _, b := range reg {
		if !b.Remote || b.WsURL != "" {
			t.Errorf("status entry %+v", b)
		}
	}
}

// A Secret without the remote-browser-auth label (a pull credential, a
// database password) must never be read into the registry, whatever the
// Controller names.
func TestCollectBrowsersRemoteAuthUnlabelledSecret(t *testing.T) {
	for _, labels := range []map[string]string{nil, {remoteAuthLabel: "false"}, {"other": "true"}} {
		sec := &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Name: "registry-pull", Namespace: "ns", Labels: labels},
			Data:       map[string][]byte{".dockerconfigjson": []byte("Bearer platform-secret")},
		}
		r := newReconciler(t, sec)
		no := false
		cr := &browserv1.Controller{
			ObjectMeta: metav1.ObjectMeta{Name: "api", Namespace: "ns"},
			Spec: browserv1.ControllerSpec{
				Autodiscover: &no,
				ExternalBrowsers: []browserv1.ExternalBrowser{{ID: "x", WsURL: "wss://evil",
					AuthHeaderSecretRef: &corev1.SecretKeySelector{
						LocalObjectReference: corev1.LocalObjectReference{Name: "registry-pull"},
						Key:                  ".dockerconfigjson"}}},
			},
		}
		entries, _, notes, err := r.collectBrowsers(context.Background(), cr)
		if err != nil {
			t.Fatal(err)
		}
		if entries["x"] != "wss://evil" {
			t.Errorf("labels %v: entry %v carries the Secret", labels, entries["x"])
		}
		if len(notes) != 1 || !strings.Contains(notes[0], "remote browser x") {
			t.Errorf("labels %v: notes %q", labels, notes)
		}
	}
}

func TestCollectBrowsersDuplicateRemoteIDs(t *testing.T) {
	r := newReconciler(t)
	no := false
	cr := &browserv1.Controller{
		ObjectMeta: metav1.ObjectMeta{Name: "api", Namespace: "ns"},
		Spec: browserv1.ControllerSpec{
			Autodiscover: &no,
			ExternalBrowsers: []browserv1.ExternalBrowser{
				{ID: "cloud", WsURL: "wss://a"},
				{ID: "cloud", WsURL: "wss://b"},
			},
		},
	}
	entries, reg, notes, err := r.collectBrowsers(context.Background(), cr)
	if err != nil {
		t.Fatal(err)
	}
	if len(reg) != 1 || entries["cloud"] != "wss://a" {
		t.Errorf("registered %v entries %v", reg, entries)
	}
	want := []string{"remote browser 1 and remote browser 2 share id cloud; only remote browser 1 is used"}
	if !reflect.DeepEqual(notes, want) {
		t.Errorf("notes %q, want %q", notes, want)
	}
}

// Notes name a tenant's browsers by the name the person gave them, not by the
// object name.
func TestCollectBrowsersNotesUseWorkloadID(t *testing.T) {
	b1 := newBrowser("acme-one", "default", "ws://one")
	b1.Labels = map[string]string{workloadIDLabel: "one"}
	b2 := newBrowser("acme-two", "default", "ws://two")
	b2.Labels = map[string]string{workloadIDLabel: "two"}
	r := newReconciler(t, b1, b2)
	cr := &browserv1.Controller{ObjectMeta: metav1.ObjectMeta{Name: "api", Namespace: "ns"}}
	_, _, notes, err := r.collectBrowsers(context.Background(), cr)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"browser one and browser two share id default; only browser one is used"}
	if !reflect.DeepEqual(notes, want) {
		t.Errorf("notes %q, want %q", notes, want)
	}
}

func TestApplyLoadsUnknownTabs(t *testing.T) {
	reg := func() []browserv1.RegisteredBrowser {
		return []browserv1.RegisteredBrowser{{ProfileUID: "a"}, {ProfileUID: "b"}}
	}
	loads := map[string]browserLoad{"a": {OpenTabs: 3, Sessions: 2}}

	r := reg()
	counts, total := applyLoads(r, loads, true)
	if r[0].OpenTabs == nil || *r[0].OpenTabs != 3 || r[0].PageCount != 2 {
		t.Errorf("answered browser: %+v", r[0])
	}
	if r[1].OpenTabs != nil {
		t.Errorf("browser the controller did not report has tabs %d", *r[1].OpenTabs)
	}
	if total != 2 || counts["a"] != 2 {
		t.Errorf("counts %v total %d", counts, total)
	}

	r = reg()
	applyLoads(r, nil, false)
	for _, b := range r {
		if b.OpenTabs != nil {
			t.Errorf("controller not asked, yet %s has tabs %d", b.ProfileUID, *b.OpenTabs)
		}
	}

	r = reg()
	applyLoads(r, map[string]browserLoad{"a": {}, "b": {}}, true)
	if r[0].OpenTabs == nil || *r[0].OpenTabs != 0 {
		t.Errorf("a known zero must be reported: %+v", r[0])
	}
}

func TestEnsureBrowsersRegistrySecret(t *testing.T) {
	no := false
	cr := &browserv1.Controller{
		ObjectMeta: metav1.ObjectMeta{Name: "api", Namespace: "ns", UID: "ctrl-uid"},
		Spec: browserv1.ControllerSpec{
			Autodiscover:     &no,
			ExternalBrowsers: []browserv1.ExternalBrowser{{ID: "cloud", WsURL: "wss://a", AuthHeader: "Bearer abc"}},
		},
	}
	oldCM := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: "api-browsers", Namespace: "ns"},
		Data:       map[string]string{browsersConfigFile: `{"browsers":{"cloud":{"wsUrl":"wss://a","headers":{"Authorization":"Bearer abc"}}}}`},
	}
	r := newReconciler(t, cr, oldCM)
	ctx := context.Background()
	if err := r.ensureBrowsersRegistry(ctx, cr); err != nil {
		t.Fatal(err)
	}
	var sec corev1.Secret
	if err := r.Get(ctx, client.ObjectKey{Namespace: "ns", Name: "api-browsers"}, &sec); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(sec.Data[browsersConfigFile]), "Bearer abc") || !metav1.IsControlledBy(&sec, cr) {
		t.Errorf("registry Secret %+v", sec)
	}
	var cm corev1.ConfigMap
	if err := r.Get(ctx, client.ObjectKey{Namespace: "ns", Name: "api-browsers"}, &cm); err == nil {
		t.Error("the old registry ConfigMap (with the header) is still there")
	}

	// Removing the remote browser removes its header from the registry.
	cr.Spec.ExternalBrowsers = nil
	if err := r.ensureBrowsersRegistry(ctx, cr); err != nil {
		t.Fatal(err)
	}
	if err := r.Get(ctx, client.ObjectKey{Namespace: "ns", Name: "api-browsers"}, &sec); err != nil {
		t.Fatal(err)
	}
	if got := string(sec.Data[browsersConfigFile]); got != `{"browsers":{}}` {
		t.Errorf("registry after removal %s", got)
	}

	// A same-named Secret the controller doesn't own is never overwritten.
	foreign := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "b-browsers", Namespace: "ns"},
		Data: map[string][]byte{"k": []byte("v")}}
	cr2 := &browserv1.Controller{ObjectMeta: metav1.ObjectMeta{Name: "b", Namespace: "ns", UID: "b-uid"},
		Spec: browserv1.ControllerSpec{Autodiscover: &no}}
	r2 := newReconciler(t, cr2, foreign)
	if err := r2.ensureBrowsersRegistry(ctx, cr2); err == nil {
		t.Error("overwrote a Secret it does not own")
	}
}

func TestControllerDeploymentMountsRegistrySecret(t *testing.T) {
	var d appsv1.Deployment
	cr := &browserv1.Controller{ObjectMeta: metav1.ObjectMeta{Name: "api", Namespace: "ns"}}
	applyControllerDeploymentSpec(&d, cr, "img", "", nil, nil)
	vols := d.Spec.Template.Spec.Volumes
	if len(vols) != 1 || vols[0].Secret == nil || vols[0].Secret.SecretName != "api-browsers" || vols[0].ConfigMap != nil {
		t.Errorf("registry volume %+v", vols)
	}
}
