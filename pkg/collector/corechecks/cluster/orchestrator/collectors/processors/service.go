// Unless explicitly stated otherwise all files in this repository are licensed
// under the Apache License Version 2.0.
// This product includes software developed at Datadog (https://www.datadoghq.com/).
// Copyright 2016-present Datadog, Inc.

//go:build kubeapiserver && orchestrator
// +build kubeapiserver,orchestrator

package processors

import (
	model "github.com/DataDog/agent-payload/v5/process"
	"github.com/DataDog/datadog-agent/pkg/collector/corechecks/cluster/orchestrator/collectors/transformers"
	"github.com/DataDog/datadog-agent/pkg/orchestrator/redact"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
)

type K8sServiceHandlers struct{}

func (sp *K8sServiceHandlers) AfterMarshalling(ctx *ProcessorContext, resource, resourceModel interface{}, yaml []byte) error {
	m := resourceModel.(*model.Service)
	m.Yaml = yaml
	return nil
}

func (sp *K8sServiceHandlers) BeforeCacheCheck(ctx *ProcessorContext, resource, resourceModel interface{}) error {
	return nil
}

func (sp *K8sServiceHandlers) BeforeMarshalling(ctx *ProcessorContext, resource, resourceModel interface{}) error {
	return nil
}

func (sp *K8sServiceHandlers) BuildMessageBody(ctx *ProcessorContext, resourceModels []interface{}, groupSize int) model.MessageBody {
	var services []*model.Service

	for _, r := range resourceModels {
		services = append(services, r.(*model.Service))
	}

	return &model.CollectorService{
		ClusterName: ctx.Cfg.KubeClusterName,
		ClusterId:   ctx.ClusterID,
		GroupId:     ctx.MsgGroupID,
		GroupSize:   int32(groupSize),
		Services:    services,
		Tags:        ctx.Cfg.ExtraTags,
	}
}

func (sp *K8sServiceHandlers) ExtractResource(ctx *ProcessorContext, resource interface{}) (resourceModel interface{}) {
	s := resource.(*corev1.Service)
	return transformers.ExtractService(s)
}

func (sp *K8sServiceHandlers) ResourceList(ctx *ProcessorContext, list interface{}) (resources []interface{}) {
	resourceList := list.([]*corev1.Service)
	resources = make([]interface{}, 0, len(resourceList))

	for _, resource := range resourceList {
		resources = append(resources, resource)
	}

	return resources
}

func (sp *K8sServiceHandlers) ResourceUID(ctx *ProcessorContext, resource, resourceModel interface{}) types.UID {
	return resource.(*corev1.Pod).UID
}

func (sp *K8sServiceHandlers) ResourceVersion(ctx *ProcessorContext, resource, resourceModel interface{}) string {
	return resource.(*corev1.Service).ResourceVersion
}

func (sp *K8sServiceHandlers) Scrub(ctx *ProcessorContext, resource interface{}) {
	s := resource.(*corev1.Service)
	redact.RemoveLastAppliedConfigurationAnnotation(s.Annotations)
}
