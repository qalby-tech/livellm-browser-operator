package controller

import (
	"reflect"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	browserv1 "github.com/livellm/browser-operator/api/v1alpha1"
)

func TestControllerDeploymentNodeSelector(t *testing.T) {
	var d appsv1.Deployment
	cr := &browserv1.Controller{ObjectMeta: metav1.ObjectMeta{Name: "api", Namespace: "ns"}}
	applyControllerDeploymentSpec(&d, cr, "img", "", nil, nil)
	if d.Spec.Template.Spec.NodeSelector != nil {
		t.Errorf("no nodeSelector asked, got %v", d.Spec.Template.Spec.NodeSelector)
	}

	cr.Spec.NodeSelector = map[string]string{"kubernetes.io/hostname": "h"}
	applyControllerDeploymentSpec(&d, cr, "img", "", nil, nil)
	if got := d.Spec.Template.Spec.NodeSelector; !reflect.DeepEqual(got, map[string]string{"kubernetes.io/hostname": "h"}) {
		t.Errorf("nodeSelector %v", got)
	}

	cr.Spec.NodeSelector = nil
	applyControllerDeploymentSpec(&d, cr, "img", "", nil, nil)
	if d.Spec.Template.Spec.NodeSelector != nil {
		t.Errorf("cleared nodeSelector must render nil, got %v", d.Spec.Template.Spec.NodeSelector)
	}
}
