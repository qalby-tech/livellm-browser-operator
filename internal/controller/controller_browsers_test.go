package controller

import (
	"context"
	"reflect"
	"strings"
	"testing"

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
		"browsers agent-1 and agent-2 share id default; only agent-1 is used",
		"browsers agent-1 and remote default share id default; only agent-1 is used",
	}
	if !reflect.DeepEqual(notes, want) {
		t.Errorf("notes %q, want %q", notes, want)
	}
}

func TestCollectBrowsersRemoteAuth(t *testing.T) {
	sec := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "ws-api-remote", Namespace: "ns"},
		Data:       map[string][]byte{"cloud": []byte("Bearer from-secret")},
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
