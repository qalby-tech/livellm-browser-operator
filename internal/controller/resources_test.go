package controller

import (
	"reflect"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	browserv1 "github.com/livellm/browser-operator/api/v1alpha1"
)

func TestNodeMaxOldSpaceMiB(t *testing.T) {
	cases := []struct {
		limit string
		want  int64
	}{
		{"", 4096},     // unknown limit — conservative default
		{"256Mi", 512}, // floor
		{"1Gi", 512},   // half=512, limit-2048 negative — floor wins
		{"2Gi", 1024},  // historical half-split
		{"4Gi", 2048},  // half=2048 > limit-2048=2048 (equal)
		{"8Gi", 6144},  // limit-2048 beats half
		{"12Gi", 8192}, // limit-2048=10240 capped
		{"32Gi", 8192}, // hard cap
	}
	for _, c := range cases {
		var q resource.Quantity
		if c.limit != "" {
			q = resource.MustParse(c.limit)
		}
		if got := nodeMaxOldSpaceMiB(q); got != c.want {
			t.Errorf("nodeMaxOldSpaceMiB(%q) = %d, want %d", c.limit, got, c.want)
		}
	}
}

func TestBrowserDeploymentNodeSelector(t *testing.T) {
	var d appsv1.Deployment
	b := &browserv1.Browser{ObjectMeta: metav1.ObjectMeta{Name: "b", Namespace: "ns"}}
	applyDeploymentSpec(&d, b, "img", "", nil, nil)
	if d.Spec.Template.Spec.NodeSelector != nil {
		t.Errorf("no nodeSelector asked, got %v", d.Spec.Template.Spec.NodeSelector)
	}

	b.Spec.NodeSelector = map[string]string{}
	applyDeploymentSpec(&d, b, "img", "", nil, nil)
	if d.Spec.Template.Spec.NodeSelector != nil {
		t.Errorf("empty nodeSelector must render nil, got %v", d.Spec.Template.Spec.NodeSelector)
	}

	b.Spec.NodeSelector = map[string]string{"kubernetes.io/hostname": "h"}
	applyDeploymentSpec(&d, b, "img", "", nil, nil)
	got := d.Spec.Template.Spec.NodeSelector
	if !reflect.DeepEqual(got, map[string]string{"kubernetes.io/hostname": "h"}) {
		t.Errorf("nodeSelector %v", got)
	}
	got["x"] = "y"
	if _, ok := b.Spec.NodeSelector["x"]; ok {
		t.Error("pod template shares the CR's map")
	}
}
