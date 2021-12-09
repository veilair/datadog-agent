// Unless explicitly stated otherwise all files in this repository are licensed
// under the Apache License Version 2.0.
// This product includes software developed at Datadog (https://www.datadoghq.com/).
// Copyright 2016-present Datadog, Inc.

package aggregator

import (
	"sync"
	"time"

	"github.com/DataDog/datadog-agent/pkg/aggregator/ckey"
	"github.com/DataDog/datadog-agent/pkg/collector/check"
	"github.com/DataDog/datadog-agent/pkg/config"
	"github.com/DataDog/datadog-agent/pkg/config/resolver"
	"github.com/DataDog/datadog-agent/pkg/epforwarder"
	"github.com/DataDog/datadog-agent/pkg/forwarder"
	"github.com/DataDog/datadog-agent/pkg/metrics"
	"github.com/DataDog/datadog-agent/pkg/serializer"
	"github.com/DataDog/datadog-agent/pkg/tagset"
	"github.com/DataDog/datadog-agent/pkg/util/log"
)

// DemultiplexerInstance is a shared global demultiplexer instance.
// Initialized by InitAndStartAgentDemultiplexer or InitAndStartServerlessDemultiplexer,
// could be nil otherwise.
//
// The plan is to deprecated this global instance at some point.
var demultiplexerInstance Demultiplexer

var demultiplexerInstanceMu sync.Mutex

// Demultiplexer is composed of multiple samplers (check and time/dogstatsd)
// a shared forwarder, the event platform forwarder, orchestrator data buffers
// and other data that need to be sent to the forwarders.
// DemultiplexerOptions let you configure which forwarders have to be started.
type Demultiplexer interface {
	// General

	Run()
	Stop(flush bool)

	// Aggregation API

	// AddTimeSamples adds time samples processed by the DogStatsD server into a time sampler pipeline.
	// The MetricSamples should have their hash computed.
	AddTimeSamples(sample []metrics.MetricSample)
	// AddCheckSample adds check sample sent by a check from one of the collectors into a check sampler pipeline.
	AddCheckSample(sample metrics.MetricSample)
	// FlushAggregatedData flushes all the aggregated data from the samplers to
	// the serialization/forwarding parts.
	FlushAggregatedData(start time.Time, waitForSerializer bool)
	// Aggregator returns an aggregator that anyone can use. This method exists
	// to keep compatibility with existing code while introducing the Demultiplexer,
	// however, the plan is to remove it anytime soon.
	//
	// Deprecated.
	Aggregator() *BufferedAggregator
	// Serializer returns a serializer that anyone can use. This method exists
	// to keep compatibility with existing code while introducing the Demultiplexer,
	// however, the plan is to remove it anytime soon.
	//
	// Deprecated.
	Serializer() serializer.MetricSerializer

	// Senders API, mainly used by collectors/checks

	GetSender(id check.ID) (Sender, error)
	SetSender(sender Sender, id check.ID) error
	DestroySender(id check.ID)
	GetDefaultSender() (Sender, error)
	ChangeAllSendersDefaultHostname(hostname string)
	cleanSenders()
}

// AgentDemultiplexer is the demultiplexer implementation for the main Agent
type AgentDemultiplexer struct {
	m sync.Mutex

	// options are the options with which the demultiplexer has been created
	options    DemultiplexerOptions
	aggregator *BufferedAggregator
	dataOutputs
	*senders

	// stopChan is used while closing the Demultiplexer instance, to cleanly
	// stop all running goroutines.
	stopChan chan struct{}

	// XXX(remy): factorize all these members in a struct?
	// XXX(remy): comment me
	timeSamplesCh chan []metrics.MetricSample
	// sharded statsdSamplers
	statsdSamplers    []*TimeSampler
	statsdSerializers []serializer.MetricSerializer
	// buffer slice allocated once per contextResolver to combine and sort
	// tags, origin detection tags and k8s tags.
	statsdTagsBuffer   *tagset.HashingTagsAccumulator
	statsdKeyGenerator *ckey.KeyGenerator

	// how many sharded statsdSamplers exists.
	// len(statsdSamplers) would return the same result but having it stored
	// it will provide more explicit visiblility / no extra function call for
	// every metric to distribute.
	statsdPipelinesCount int
}

// DemultiplexerOptions are the options used to initialize a Demultiplexer.
type DemultiplexerOptions struct {
	ForwarderOptions              *forwarder.Options
	UseNoopEventPlatformForwarder bool
	NoEventPlatformForwarder      bool
	NoOrchestratorForwarder       bool
	FlushInterval                 time.Duration

	DontStartForwarders bool // unit tests don't need the forwarders to be instanciated
}

type forwarders struct {
	shared        *forwarder.DefaultForwarder
	orchestrator  *forwarder.DefaultForwarder
	eventPlatform epforwarder.EventPlatformForwarder
}

type dataOutputs struct {
	forwarders       forwarders
	sharedSerializer serializer.MetricSerializer
}

// DefaultDemultiplexerOptions returns the default options to initialize a Demultiplexer.
func DefaultDemultiplexerOptions(options *forwarder.Options) DemultiplexerOptions {
	if options == nil {
		options = forwarder.NewOptions(nil)
	}

	return DemultiplexerOptions{
		ForwarderOptions: options,
		FlushInterval:    DefaultFlushInterval,
	}
}

// InitAndStartAgentDemultiplexer creates a new Demultiplexer and runs what's necessary
// in goroutines. As of today, only the embedded BufferedAggregator needs a separate goroutine.
// In the future, goroutines will be started for the event platform forwarder and/or orchestrator forwarder.
func InitAndStartAgentDemultiplexer(options DemultiplexerOptions, hostname string) *AgentDemultiplexer {
	demultiplexerInstanceMu.Lock()
	defer demultiplexerInstanceMu.Unlock()

	// prepare the multiple forwarders
	// -------------------------------

	log.Debugf("Starting forwarders")
	// orchestrator forwarder
	var orchestratorForwarder *forwarder.DefaultForwarder
	if !options.NoOrchestratorForwarder {
		orchestratorForwarder = buildOrchestratorForwarder()
	}

	// event platform forwarder
	var eventPlatformForwarder epforwarder.EventPlatformForwarder
	if !options.NoEventPlatformForwarder && options.UseNoopEventPlatformForwarder {
		eventPlatformForwarder = epforwarder.NewNoopEventPlatformForwarder()
	} else if !options.NoEventPlatformForwarder {
		eventPlatformForwarder = epforwarder.NewEventPlatformForwarder()
	}

	sharedForwarder := forwarder.NewDefaultForwarder(options.ForwarderOptions)

	// statsd samplers
	// ---------------

	bufferSize := config.Datadog.GetInt("aggregator_buffer_size")

	statsdPipelinesCount := config.Datadog.GetInt("dogstatsd_pipeline_count")
	if statsdPipelinesCount <= 0 {
		statsdPipelinesCount = 1
	}

	statsdSamplers := make([]*TimeSampler, statsdPipelinesCount)
	statsdSerializers := make([]serializer.MetricSerializer, statsdPipelinesCount)

	for i := 0; i < statsdPipelinesCount; i++ {
		statsdSerializers[i] = serializer.NewSerializer(sharedForwarder, orchestratorForwarder)
		statsdSamplers[i] = NewTimeSampler(TimeSamplerID(i), bucketSize, statsdSerializers[i])
	}

	// prepare the serializer
	// ----------------------

	sharedSerializer := serializer.NewSerializer(sharedForwarder, orchestratorForwarder)

	// prepare the embedded aggregator
	// --

	agg := InitAggregatorWithFlushInterval(sharedSerializer, eventPlatformForwarder, hostname, options.FlushInterval)

	// --

	demux := &AgentDemultiplexer{
		options: options,

		// Input
		aggregator: agg,

		// Output
		dataOutputs: dataOutputs{

			forwarders: forwarders{
				shared:        sharedForwarder,
				orchestrator:  orchestratorForwarder,
				eventPlatform: eventPlatformForwarder,
			},

			sharedSerializer: sharedSerializer,
		},

		senders: newSenders(agg),

		stopChan: make(chan struct{}),

		timeSamplesCh: make(chan []metrics.MetricSample, bufferSize),

		statsdSamplers:       statsdSamplers,
		statsdSerializers:    statsdSerializers,
		statsdPipelinesCount: statsdPipelinesCount,
		statsdTagsBuffer:     tagset.NewHashingTagsAccumulator(),
		statsdKeyGenerator:   ckey.NewKeyGenerator(),
	}

	if demultiplexerInstance != nil {
		log.Warn("A DemultiplexerInstance is already existing but InitAndStartAgentDemultiplexer has been called again. Current instance will be overridden")
	}
	demultiplexerInstance = demux

	go demux.Run()
	return demux
}

// Run runs all demultiplexer parts
func (d *AgentDemultiplexer) Run() {
	if !d.options.DontStartForwarders {
		if d.forwarders.orchestrator != nil {
			d.forwarders.orchestrator.Start() //nolint:errcheck
		} else {
			log.Debug("not starting the orchestrator forwarder")
		}
		if d.forwarders.eventPlatform != nil {
			d.forwarders.eventPlatform.Start()
		} else {
			log.Debug("not starting the event platform forwarder")
		}
		if d.forwarders.shared != nil {
			d.forwarders.shared.Start() //nolint:errcheck
		} else {
			log.Debug("not starting the shared forwarder")
		}
		log.Debug("Forwarders started")
	}

	// XXX(remy): if sharding pipelines > 1
	go d.samplesLoop()

	d.aggregator.run() // this is the blocking call
}

// AddAgentStartupTelemetry adds a startup event and count to be sent on the next flush
func (d *AgentDemultiplexer) AddAgentStartupTelemetry(agentVersion string) {
	if agentVersion != "" {
		d.aggregator.AddAgentStartupTelemetry(agentVersion)
	}
}

// Stop stops the demultiplexer.
// Resources are released, the instance should not be used after a call to `Stop()`.
func (d *AgentDemultiplexer) Stop(flush bool) {
	d.m.Lock()
	defer d.m.Unlock()

	d.stopChan <- struct{}{}

	if d.aggregator != nil {
		d.aggregator.Stop(flush)
	}
	d.aggregator = nil

	if !d.options.DontStartForwarders {
		if d.dataOutputs.forwarders.orchestrator != nil {
			d.dataOutputs.forwarders.orchestrator.Stop()
			d.dataOutputs.forwarders.orchestrator = nil
		}
		if d.dataOutputs.forwarders.eventPlatform != nil {
			d.dataOutputs.forwarders.eventPlatform.Stop()
			d.dataOutputs.forwarders.eventPlatform = nil
		}
		if d.dataOutputs.forwarders.shared != nil {
			d.dataOutputs.forwarders.shared.Stop()
			d.dataOutputs.forwarders.shared = nil
		}
	}

	d.dataOutputs.sharedSerializer = nil
	d.senders = nil
	demultiplexerInstance = nil
}

// FlushAggregatedData flushes all data from the aggregator to the serializer
// FIXME(remy): document thread-safety once aggregated API has been implemented
func (d *AgentDemultiplexer) FlushAggregatedData(start time.Time, waitForSerializer bool) {
	d.m.Lock()
	defer d.m.Unlock()

	if d.aggregator != nil {
		d.aggregator.Flush(start, waitForSerializer)
	}
}

// AddTimeSamples adds time samples processed by the DogStatsD server into a time sampler pipeline.
// The MetricSamples should have their hash computed.
// This function is called by different goroutines and is thread-safe.
func (d *AgentDemultiplexer) AddTimeSamples(samples []metrics.MetricSample) {
	// distribute the samples on the different statsd samplers
	// using this channel for latency reasons: its buffering + the fact that it is another goroutine
	// processing the samples, it should get back to the caller as fast as possible once the samples
	// are in the channel.
	d.timeSamplesCh <- samples
}

// samplesLoop is the main loop sending all received time samples to a selected time sampler.
// samplesLoop runs in its own goroutine.
func (d *AgentDemultiplexer) samplesLoop() {
	for {
		select {
		case ms := <-d.timeSamplesCh:
			// do this telemetry here and not in the samplers goroutines, this way
			// they won't compete for the telemetry locks.
			aggregatorDogstatsdMetricSample.Add(int64(len(ms)))
			tlmProcessed.Add(float64(len(ms)), "dogstatsd_metrics")
			t := timeNowNano()
			for i := 0; i < len(ms); i++ {
				d.addTimeSample(&ms[i], t)
			}
			d.aggregator.MetricSamplePool.PutBatch(ms)
		case <-d.stopChan:
			// stop all samplers
			// XXX(remy): should I do it here or in the main Stop() method?
			for _, sampler := range d.statsdSamplers {
				sampler.Stop()
			}
			log.Info("Stopping Demultiplexer sharding loop")
			return
		}
	}
}

// XXX(remy): implement this using fastrange instead https://github.com/lemire/fastrange
func fastrange(key ckey.ContextKey, pipelinesCount int) uint64 {
	//	log.Infof("fastrange(%d, %d) = %d", uint64(key), uint64(pipelinesCount), uint64(key)%uint64(pipelinesCount))
	return uint64(key) % uint64(pipelinesCount)
}

// addTimeSample sends the time sample for processing in one of the available time samplers, making
// sure it is always sent to the same.
// addTimeSample runs only once (per demultiplexer) in its own gorountine and is the one distributing
// the samples on the sampling goroutines.
func (d *AgentDemultiplexer) addTimeSample(sample *metrics.MetricSample, timestamp float64) {
	// no sharding
	if len(d.statsdSamplers) == 1 {
		d.statsdSamplers[0].addSample(sample, timestamp)
		return
	}

	d.statsdTagsBuffer.Append(sample.Tags...) // XXX(remy):
	shardKey := d.statsdKeyGenerator.Generate(sample.Name, sample.Host, d.statsdTagsBuffer)
	d.statsdTagsBuffer.Reset()

	shard := fastrange(shardKey, d.statsdPipelinesCount)
	// XXX(remy): another design would be to not send it through a channel here
	// but to directly do:
	//
	//    go d.statsdSamplers[shard].addSample(sample, timestamp)
	//
	// And to have the proper mutex on addSample and during the flush mechanism of the sampler
	// It will lead to _a lot_ of routines creation though.
	d.statsdSamplers[shard].addSample(sample, timestamp)
}

// AddCheckSample adds check sample sent by a check from one of the collectors into a check sampler pipeline.
func (d *AgentDemultiplexer) AddCheckSample(sample metrics.MetricSample) {
	panic("not implemented yet.")
}

// Serializer returns a serializer that anyone can use. This method exists
// to keep compatibility with existing code while introducing the Demultiplexer,
// however, the plan is to remove it anytime soon.
//
// Deprecated.
func (d *AgentDemultiplexer) Serializer() serializer.MetricSerializer {
	return d.dataOutputs.sharedSerializer
}

// Aggregator returns an aggregator that anyone can use. This method exists
// to keep compatibility with existing code while introducing the Demultiplexer,
// however, the plan is to remove it anytime soon.
//
// Deprecated.
func (d *AgentDemultiplexer) Aggregator() *BufferedAggregator {
	return d.aggregator
}

// ------------------------------

// ServerlessDemultiplexer is a simple demultiplexer used by the serverless flavor of the Agent
type ServerlessDemultiplexer struct {
	aggregator *BufferedAggregator
	serializer *serializer.Serializer
	forwarder  *forwarder.SyncForwarder
	*senders
}

// InitAndStartServerlessDemultiplexer creates and starts new Demultiplexer for the serverless agent.
func InitAndStartServerlessDemultiplexer(domainResolvers map[string]resolver.DomainResolver, hostname string, forwarderTimeout time.Duration) *ServerlessDemultiplexer {
	forwarder := forwarder.NewSyncForwarder(domainResolvers, forwarderTimeout)
	serializer := serializer.NewSerializer(forwarder, nil)
	aggregator := InitAggregator(serializer, nil, hostname)

	demux := &ServerlessDemultiplexer{
		aggregator: aggregator,
		serializer: serializer,
		forwarder:  forwarder,
		senders:    newSenders(aggregator),
	}

	demultiplexerInstance = demux

	go demux.Run()

	return demux
}

// Run runs all demultiplexer parts
func (d *ServerlessDemultiplexer) Run() {
	if d.forwarder != nil {
		d.forwarder.Start() //nolint:errcheck
	} else {
		log.Debug("not starting the forwarder")
	}
	log.Debug("Forwarder started")

	d.aggregator.run()
	log.Debug("Aggregator started")
}

// Stop stops the wrapped aggregator and the forwarder.
func (d *ServerlessDemultiplexer) Stop(flush bool) {
	d.aggregator.Stop(flush)

	if d.forwarder != nil {
		d.forwarder.Stop()
	}
}

// FlushAggregatedData flushes all data from the aggregator to the serializer
func (d *ServerlessDemultiplexer) FlushAggregatedData(start time.Time, waitForSerializer bool) {
	d.aggregator.Flush(start, waitForSerializer)
}

// AddTimeSamples adds time samples processed by the DogStatsD server into a time sampler pipeline.
// The MetricSamples should have their hash computed.
func (d *ServerlessDemultiplexer) AddTimeSamples(samples []metrics.MetricSample) {
	panic("not implemented yet.")
}

// AddCheckSample doesn't do anything in the Serverless Agent implementation.
func (d *ServerlessDemultiplexer) AddCheckSample(sample metrics.MetricSample) {
	panic("not implemented yet.")
}

// Serializer returns the shared serializer
func (d *ServerlessDemultiplexer) Serializer() serializer.MetricSerializer {
	return d.serializer
}

// Aggregator returns the main buffered aggregator
func (d *ServerlessDemultiplexer) Aggregator() *BufferedAggregator {
	return d.aggregator
}
