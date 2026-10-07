/*
Copyright 2023 K8sGPT Contributors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.

Modified for autonomous-sre-training: copied from k8sgpt-operator v0.2.27
api/v1alpha1/result_types.go, keeping the Result fields and JSON tags
without the controller-runtime scheme registration.
*/

// Package v1alpha1 is the K8sGPT Result type (core.k8sgpt.ai/v1alpha1) the
// watcher writes. Its fields and JSON tags are those of the k8sgpt-operator's
// api/v1alpha1 Result (github.com/k8sgpt-ai/k8sgpt-operator v0.2.27), so the
// objects are the same as with the operator's Go types, without the operator
// and controller-runtime modules. Results are written as unstructured
// objects through client-go's dynamic client.
package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

var (
	// GroupVersion is the API group and version of the Result resource.
	GroupVersion = schema.GroupVersion{Group: "core.k8sgpt.ai", Version: "v1alpha1"}
	// GroupVersionKind is set as apiVersion and kind on every Result written.
	GroupVersionKind = GroupVersion.WithKind("Result")
	// GroupVersionResource addresses Results through the dynamic client.
	GroupVersionResource = GroupVersion.WithResource("results")
)

type Failure struct {
	Text      string      `json:"text,omitempty"`
	Sensitive []Sensitive `json:"sensitive,omitempty"`
}

type Sensitive struct {
	Unmasked string `json:"unmasked,omitempty"`
	Masked   string `json:"masked,omitempty"`
}

// AutoRemediationPhase is the operator's auto-remediation phase. The watcher
// never sets it.
type AutoRemediationPhase int

type AutoRemediationStatus struct {
	Phase AutoRemediationPhase `json:"phase,omitempty"`
}

// ResultSpec defines the desired state of Result
type ResultSpec struct {
	Backend               string                `json:"backend"`
	AutoRemediationStatus AutoRemediationStatus `json:"autoRemediationStatus"`
	Kind                  string                `json:"kind"`
	Name                  string                `json:"name"`
	Error                 []Failure             `json:"error"`
	Details               string                `json:"details"`
	ParentObject          string                `json:"parentObject"`
}

// ResultStatus defines the observed state of Result
type ResultStatus struct {
	LifeCycle   string `json:"lifecycle,omitempty"`
	Webhook     string `json:"webhook,omitempty"`
	ContentHash string `json:"contentHash,omitempty"`
}

// Result is the Schema for the results API
type Result struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   ResultSpec   `json:"spec,omitempty"`
	Status ResultStatus `json:"status,omitempty"`
}
