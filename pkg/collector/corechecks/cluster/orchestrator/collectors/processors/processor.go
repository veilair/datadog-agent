// Unless explicitly stated otherwise all files in this repository are licensed
// under the Apache License Version 2.0.
// This product includes software developed at Datadog (https://www.datadoghq.com/).
// Copyright 2016-present Datadog, Inc.

//go:build kubeapiserver && orchestrator
// +build kubeapiserver,orchestrator

package processors

import (
	model "github.com/DataDog/agent-payload/v5/process"
	msgModel "github.com/DataDog/agent-payload/v5/process"
	"github.com/DataDog/datadog-agent/pkg/orchestrator"
	"github.com/DataDog/datadog-agent/pkg/orchestrator/config"
	"github.com/DataDog/datadog-agent/pkg/util/log"
	jsoniter "github.com/json-iterator/go"
	"k8s.io/apimachinery/pkg/types"
)

type ProcessorContext struct {
	Cfg        *config.OrchestratorConfig
	ClusterID  string
	MsgGroupID int32
	NodeType   orchestrator.NodeType
}

type Handlers interface {
	AfterMarshalling(ctx *ProcessorContext, resource, resourceModel interface{}, yaml []byte) error
	BeforeCacheCheck(ctx *ProcessorContext, resource, resourceModel interface{}) error
	BeforeMarshalling(ctx *ProcessorContext, resource, resourceModel interface{}) error
	BuildMessageBody(ctx *ProcessorContext, resourceModels []interface{}, groupSize int) model.MessageBody
	ExtractResource(ctx *ProcessorContext, resource interface{}) (model interface{})
	ResourceList(ctx *ProcessorContext, list interface{}) (resources []interface{})
	ResourceUID(ctx *ProcessorContext, resource, resourceModel interface{}) types.UID
	ResourceVersion(ctx *ProcessorContext, resource, resourceModel interface{}) string
	Scrub(ctx *ProcessorContext, resource interface{})
}

type Processor struct {
	h Handlers
}

func NewProcessor(h Handlers) *Processor {
	return &Processor{
		h: h,
	}
}

func (p *Processor) ChunkResources(ctx *ProcessorContext, resources []interface{}, chunkCount int) [][]interface{} {
	chunks := make([][]interface{}, 0, chunkCount)
	chunkSize := ctx.Cfg.MaxPerMessage

	for counter := 1; counter <= chunkCount; counter++ {
		chunkStart, chunkEnd := orchestrator.ChunkRange(len(resources), chunkCount, chunkSize, counter)
		chunks = append(chunks, resources[chunkStart:chunkEnd])
	}

	return chunks
}

func (p *Processor) Process(ctx *ProcessorContext, list interface{}) (messages []msgModel.MessageBody, processed int) {
	// This default allows detection of panic recoveries.
	processed = -1

	// Make sure to recover if a panic occurs.
	defer recoverOnPanic()

	resourceList := p.h.ResourceList(ctx, list)
	resourceModels := make([]interface{}, 0, len(resourceList))

	for _, resource := range resourceList {

		// Scrub the resource.
		p.h.Scrub(ctx, resource)

		// Extract resource.
		resourceModel := p.h.ExtractResource(ctx, resource)

		// Execute code before cache check.
		p.h.BeforeCacheCheck(ctx, resource, resourceModel)

		// Cache check
		resourceUID := p.h.ResourceUID(ctx, resource, resourceModel)
		resourceVersion := p.h.ResourceVersion(ctx, resource, resourceModel)

		if orchestrator.SkipKubernetesResource(resourceUID, resourceVersion, ctx.NodeType) {
			continue
		}

		// Execute code before marshalling.
		p.h.BeforeMarshalling(ctx, resource, resourceModel)

		// Marshal the resource to generate the YAML field.
		yaml, err := jsoniter.Marshal(resource)
		if err != nil {
			log.Warnf(newMarshallingError(err).Error())
			continue
		}

		// Execute code after marshalling.
		p.h.AfterMarshalling(ctx, resource, resourceModel, yaml)

		resourceModels = append(resourceModels, resourceModel)
	}

	// Split messages in chunks
	chunkCount := orchestrator.GroupSize(len(resourceModels), ctx.Cfg.MaxPerMessage)
	chunks := p.ChunkResources(ctx, resourceModels, chunkCount)

	messages = make([]msgModel.MessageBody, 0, chunkCount)
	for i := 0; i < chunkCount; i++ {
		messages = append(messages, p.h.BuildMessageBody(ctx, chunks[i], chunkCount))
	}

	return messages, len(resourceModels)
}
