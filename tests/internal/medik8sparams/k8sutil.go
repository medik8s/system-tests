package medik8sparams

import (
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

// NewUnstructuredList builds an UnstructuredList for the given GVK, used when registering
// custom resource types with the k8sreporter.
func NewUnstructuredList(group, version, kind string) *unstructured.UnstructuredList {
	l := &unstructured.UnstructuredList{}
	l.SetGroupVersionKind(schema.GroupVersionKind{Group: group, Version: version, Kind: kind})

	return l
}
