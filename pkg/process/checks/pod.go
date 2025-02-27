// Unless explicitly stated otherwise all files in this repository are licensed
// under the Apache License Version 2.0.
// This product includes software developed at Datadog (https://www.datadoghq.com/).
// Copyright 2016-present Datadog, Inc.

// +build kubelet,orchestrator

package checks

import (
	"context"
	"time"

	model "github.com/DataDog/agent-payload/v5/process"
	"github.com/DataDog/datadog-agent/pkg/orchestrator/redact"
	"github.com/DataDog/datadog-agent/pkg/process/config"
	"github.com/DataDog/datadog-agent/pkg/process/util"
	"github.com/DataDog/datadog-agent/pkg/util/kubernetes/clustername"
	"github.com/DataDog/datadog-agent/pkg/util/kubernetes/kubelet"
	orchutil "github.com/DataDog/datadog-agent/pkg/util/orchestrator"
)

// Pod is a singleton PodCheck.
var Pod = &PodCheck{}

// PodCheck is a check that returns container metadata and stats.
type PodCheck struct {
	sysInfo                 *model.SystemInfo
	containerFailedLogLimit *util.LogLimit
	scrubber                *redact.DataScrubber
}

// Init initializes a PodCheck instance.
func (c *PodCheck) Init(cfg *config.AgentConfig, info *model.SystemInfo) {
	c.sysInfo = info
	c.containerFailedLogLimit = util.NewLogLimit(10, time.Minute*10)
	c.scrubber = redact.NewDefaultDataScrubber()
}

// Name returns the name of the ProcessCheck.
func (c *PodCheck) Name() string { return config.PodCheckName }

// RealTime indicates if this check only runs in real-time mode.
func (c *PodCheck) RealTime() bool { return false }

// Run runs the PodCheck to collect a list of running pods
func (c *PodCheck) Run(cfg *config.AgentConfig, groupID int32) ([]model.MessageBody, error) {
	kubeUtil, err := kubelet.GetKubeUtil()
	if err != nil {
		return nil, err
	}

	clusterID, err := clustername.GetClusterID()
	if err != nil {
		return nil, err
	}

	podList, err := kubeUtil.GetRawLocalPodList(context.TODO())
	if err != nil {
		return nil, err
	}

	return orchutil.ProcessPodList(podList, groupID, cfg.HostName, clusterID, cfg.Orchestrator)
}
