// Unless explicitly stated otherwise all files in this repository are licensed
// under the Apache License Version 2.0.
// This product includes software developed at Datadog (https://www.datadoghq.com/).
// Copyright 2016-present Datadog, Inc.

package agent

import (
	"context"
	"errors"

	"google.golang.org/grpc"

	coreconfig "github.com/DataDog/datadog-agent/pkg/config"
	"github.com/DataDog/datadog-agent/pkg/security/api"
)

// RuntimeSecurityClient is used to send request to security module
type RuntimeSecurityClient struct {
	conn *grpc.ClientConn
}

// DumpProcessCache send a dump request
func (c *RuntimeSecurityClient) DumpProcessCache() (string, error) {
	apiClient := api.NewSecurityModuleClient(c.conn)

	response, err := apiClient.DumpProcessCache(context.Background(), &api.DumpProcessCacheParams{})
	if err != nil {
		return "", err
	}

	return response.Filename, nil
}

// DumpActivity send a dump activity request
func (c *RuntimeSecurityClient) DumpActivity(tags []string, comm string, timeout int32, withGraph bool, differentiateArgs bool) (string, string, error) {
	apiClient := api.NewSecurityModuleClient(c.conn)

	response, err := apiClient.DumpActivity(context.Background(), &api.DumpActivityParams{
		Tags:              tags,
		Comm:              comm,
		Timeout:           timeout,
		WithGraph:         withGraph,
		DifferentiateArgs: differentiateArgs,
	})
	if err != nil {
		return "", "", err
	}

	return response.OutputFilename, response.GraphFilename, nil
}

// ListActivityDumps lists the active activity dumps
func (c *RuntimeSecurityClient) ListActivityDumps() ([]string, error) {
	apiClient := api.NewSecurityModuleClient(c.conn)

	response, err := apiClient.ListActivityDumps(context.Background(), &api.ListActivityDumpsParams{})
	if err != nil {
		return nil, err
	}

	return response.DumpTags, nil
}

// StopActivityDump stops an active dump if it exists
func (c *RuntimeSecurityClient) StopActivityDump(tags []string, comm string) (string, error) {
	apiClient := api.NewSecurityModuleClient(c.conn)

	response, err := apiClient.StopActivityDump(context.Background(), &api.StopActivityDumpParams{
		Tags: tags,
		Comm: comm,
	})
	if err != nil {
		return "", err
	}

	return response.Error, nil
}

// GetConfig retrieves the config of the runtime security module
func (c *RuntimeSecurityClient) GetConfig() (*api.SecurityConfigMessage, error) {
	apiClient := api.NewSecurityModuleClient(c.conn)

	response, err := apiClient.GetConfig(context.Background(), &api.GetConfigParams{})
	if err != nil {
		return nil, err
	}
	return response, nil
}

// RunSelfTest instructs the system probe to run a self test
func (c *RuntimeSecurityClient) RunSelfTest() (*api.SecuritySelfTestResultMessage, error) {
	apiClient := api.NewSecurityModuleClient(c.conn)

	response, err := apiClient.RunSelfTest(context.Background(), &api.RunSelfTestParams{})
	if err != nil {
		return nil, err
	}
	return response, nil
}

// Close closes the connection
func (c *RuntimeSecurityClient) Close() {
	c.conn.Close()
}

// NewRuntimeSecurityClient instantiates a new RuntimeSecurityClient
func NewRuntimeSecurityClient() (*RuntimeSecurityClient, error) {
	socketPath := coreconfig.Datadog.GetString("runtime_security_config.socket")
	if socketPath == "" {
		return nil, errors.New("runtime_security_config.socket must be set")
	}

	path := "unix://" + socketPath
	conn, err := grpc.Dial(path, grpc.WithInsecure())
	if err != nil {
		return nil, err
	}

	return &RuntimeSecurityClient{
		conn: conn,
	}, nil
}
