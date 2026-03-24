// Package v1alpha1 contains API Schema definitions for the livellm.io v1alpha1 API group.
//
// +kubebuilder:object:generate=true
// +groupName=livellm.io
package v1alpha1

import (
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/scheme"
)

var (
	// GroupVersion is the API group and version for Browser resources.
	GroupVersion = schema.GroupVersion{Group: "livellm.io", Version: "v1alpha1"}

	// SchemeBuilder is used to add Go types to the GroupVersionResource scheme.
	SchemeBuilder = &scheme.Builder{GroupVersion: GroupVersion}

	// AddToScheme adds the types in this group-version to the given scheme.
	AddToScheme = SchemeBuilder.AddToScheme
)

func init() {
	SchemeBuilder.Register(&Browser{}, &BrowserList{})
	SchemeBuilder.Register(&Controller{}, &ControllerList{})
}
