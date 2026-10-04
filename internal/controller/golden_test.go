package controller

import (
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"

	browserv1 "github.com/livellm/browser-operator/api/v1alpha1"
)

// updateGolden rewrites testdata/golden from the current code. Run it only on
// a tree whose resources.go is the released one, so the files keep meaning
// "what the live operator renders today".
var updateGolden = flag.Bool("update", false, "rewrite testdata/golden")

// Operator settings as the live chart sets them (DEFAULT_BROWSER_ENV and
// DEFAULT_BROWSER_RESOURCES are unset there).
const (
	goldenImage      = "kamasalyamov/livellm-browser:dev-2.2.8"
	goldenPullPolicy = "Always"
)

// TestLiveBrowsersGolden renders every Browser CR that runs on the station
// (testdata/live/browsers.json, scrubbed from `kubectl get browsers -A`) and
// requires the Deployment and Service to equal what the released operator
// renders. A change that fails here restarts live browsers.
func TestLiveBrowsersGolden(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("testdata", "live", "browsers.json"))
	if err != nil {
		t.Fatal(err)
	}
	var browsers []browserv1.Browser
	if err := json.Unmarshal(raw, &browsers); err != nil {
		t.Fatal(err)
	}
	if len(browsers) == 0 {
		t.Fatal("no fixtures")
	}
	for i := range browsers {
		b := &browsers[i]
		base := b.Namespace + "-" + b.Name

		var d appsv1.Deployment
		applyDeploymentSpec(&d, b, goldenImage, goldenPullPolicy, nil, nil)
		var s corev1.Service
		applyServiceSpec(&s, b)

		checkGolden(t, base+".deployment.json", &d, &appsv1.Deployment{})
		checkGolden(t, base+".service.json", &s, &corev1.Service{})
	}
}

func checkGolden[T any](t *testing.T, name string, got *T, want *T) {
	t.Helper()
	path := filepath.Join("testdata", "golden", name)
	if *updateGolden {
		data, err := json.MarshalIndent(got, "", "  ")
		if err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, append(data, '\n'), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, want); err != nil {
		t.Fatal(err)
	}
	// Round-trip what we rendered too, so both sides carry the same
	// representation (quantities, nil vs empty).
	gotJSON, err := json.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	var norm T
	if err := json.Unmarshal(gotJSON, &norm); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(&norm, want) {
		pretty, _ := json.MarshalIndent(got, "", "  ")
		t.Errorf("%s: render differs from the released operator:\n%s", name, pretty)
	}
}
