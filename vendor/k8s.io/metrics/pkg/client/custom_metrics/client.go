/*
Copyright 2017 The Kubernetes Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package custom_metrics

import (
	"fmt"

	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime/schema"
	serializer "k8s.io/apimachinery/pkg/runtime/serializer"
	"k8s.io/client-go/pkg/api"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/util/flowcontrol"
	"k8s.io/metrics/pkg/apis/custom_metrics/v1alpha1"
)

type customMetricsClient struct {
	client rest.Interface
	mapper meta.RESTMapper
}

func New(client rest.Interface) CustomMetricsClient {
	return NewForMapper(client, api.Registry.RESTMapper())
}

func NewForConfig(c *rest.Config) (CustomMetricsClient, error) {
	configShallowCopy := *c
	if configShallowCopy.RateLimiter == nil && configShallowCopy.QPS > 0 {
		configShallowCopy.RateLimiter = flowcontrol.NewTokenBucketRateLimiter(configShallowCopy.QPS, configShallowCopy.Burst)
	}
	configShallowCopy.APIPath = "/apis"
	if configShallowCopy.UserAgent == "" {
		configShallowCopy.UserAgent = rest.DefaultKubernetesUserAgent()
	}
	configShallowCopy.GroupVersion = &v1alpha1.SchemeGroupVersion
	configShallowCopy.NegotiatedSerializer = serializer.DirectCodecFactory{CodecFactory: api.Codecs}

	client, err := rest.RESTClientFor(&configShallowCopy)
	if err != nil {
		return nil, err
	}

	return New(client), nil
}

func NewForConfigOrDie(c *rest.Config) CustomMetricsClient {
	client, err := NewForConfig(c)
	if err != nil {
		panic(err)
	}
	return client
}

func NewForMapper(client rest.Interface, mapper meta.RESTMapper) CustomMetricsClient {
	return &customMetricsClient{
		client: client,
		mapper: mapper,
	}
}

func (c *customMetricsClient) RootScopedMetrics() MetricsInterface {
	return &rootScopedMetrics{c}
}

func (c *customMetricsClient) NamespacedMetrics(namespace string) MetricsInterface {
	return &namespacedMetrics{
		client:    c,
		namespace: namespace,
	}
}

func (c *customMetricsClient) qualResourceForKind(groupKind schema.GroupKind) (string, error) {
	mapping, err := c.mapper.RESTMapping(groupKind)
	if err != nil {
		return "", fmt.Errorf("unable to map kind %s to resource: %v", groupKind.String(), err)
	}

	groupResource := schema.GroupResource{
		Group:    mapping.GroupVersionKind.Group,
		Resource: mapping.Resource,
	}

	return groupResource.String(), nil
}

type rootScopedMetrics struct {
	client *customMetricsClient
}

func (m *rootScopedMetrics) getForNamespace(namespace string, metricName string) (*v1alpha1.MetricValue, error) {
	res := &v1alpha1.MetricValueList{}
	err := m.client.client.Get().
		Resource("metrics").
		Namespace(namespace).
		Name(metricName).
		Do().
		Into(res)

	if err != nil {
		return nil, err
	}

	if len(res.Items) != 1 {
		return nil, fmt.Errorf("the custom metrics API server returned %v results when we asked for exactly one", len(res.Items))
	}

	return &res.Items[0], nil
}

func (m *rootScopedMetrics) GetForObject(groupKind schema.GroupKind, name string, metricName string) (*v1alpha1.MetricValue, error) {
	// handle namespace separately
	if groupKind.Kind == "Namespace" && groupKind.Group == "" {
		return m.getForNamespace(name, metricName)
	}

	resourceName, err := m.client.qualResourceForKind(groupKind)
	if err != nil {
		return nil, err
	}

	res := &v1alpha1.MetricValueList{}
	err = m.client.client.Get().
		Resource(resourceName).
		Name(name).
		SubResource(metricName).
		Do().
		Into(res)

	if err != nil {
		return nil, err
	}

	if len(res.Items) != 1 {
		return nil, fmt.Errorf("the custom metrics API server returned %v results when we asked for exactly one", len(res.Items))
	}

	return &res.Items[0], nil
}

func (m *rootScopedMetrics) GetForObjects(groupKind schema.GroupKind, selector labels.Selector, metricName string) (*v1alpha1.MetricValueList, error) {
	// we can't wildcard-fetch for namespaces
	if groupKind.Kind == "Namespace" && groupKind.Group == "" {
		return nil, fmt.Errorf("cannot fetch metrics for multiple namespaces at once")
	}

	resourceName, err := m.client.qualResourceForKind(groupKind)
	if err != nil {
		return nil, err
	}

	res := &v1alpha1.MetricValueList{}
	err = m.client.client.Get().
		Resource(resourceName).
		Name(v1alpha1.AllObjects).
		SubResource(metricName).
		LabelsSelectorParam(selector).
		Do().
		Into(res)

	if err != nil {
		return nil, err
	}

	return res, nil
}

type namespacedMetrics struct {
	client    *customMetricsClient
	namespace string
}

func (m *namespacedMetrics) GetForObject(groupKind schema.GroupKind, name string, metricName string) (*v1alpha1.MetricValue, error) {
	resourceName, err := m.client.qualResourceForKind(groupKind)
	if err != nil {
		return nil, err
	}

	res := &v1alpha1.MetricValueList{}
	err = m.client.client.Get().
		Resource(resourceName).
		Namespace(m.namespace).
		Name(name).
		SubResource(metricName).
		Do().
		Into(res)

	if err != nil {
		return nil, err
	}

	if len(res.Items) != 1 {
		return nil, fmt.Errorf("the custom metrics API server returned %v results when we asked for exactly one")
	}

	return &res.Items[0], nil
}

func (m *namespacedMetrics) GetForObjects(groupKind schema.GroupKind, selector labels.Selector, metricName string) (*v1alpha1.MetricValueList, error) {
	resourceName, err := m.client.qualResourceForKind(groupKind)
	if err != nil {
		return nil, err
	}

	res := &v1alpha1.MetricValueList{}
	err = m.client.client.Get().
		Resource(resourceName).
		Namespace(m.namespace).
		Name(v1alpha1.AllObjects).
		SubResource(metricName).
		LabelsSelectorParam(selector).
		Do().
		Into(res)

	if err != nil {
		return nil, err
	}

	return res, nil
}
