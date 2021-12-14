package dogstatsd

import (
	"time"

	"github.com/DataDog/datadog-agent/pkg/aggregator"
	"github.com/DataDog/datadog-agent/pkg/aggregator/ckey"
	"github.com/DataDog/datadog-agent/pkg/config"
	"github.com/DataDog/datadog-agent/pkg/metrics"
	"github.com/DataDog/datadog-agent/pkg/tagset"
)

// batcher batches multiple metrics before submission
// this struct is not safe for concurrent use
type batcher struct {
	samples      [][]metrics.MetricSample
	samplesCount []int

	events        []*metrics.Event
	serviceChecks []*metrics.ServiceCheck

	// output channels
	choutEvents        chan<- []*metrics.Event
	choutServiceChecks chan<- []*metrics.ServiceCheck

	metricSamplePool *metrics.MetricSamplePool

	demux aggregator.Demultiplexer
	// buffer slice allocated once per contextResolver to combine and sort
	// tags, origin detection tags and k8s tags.
	tagsBuffer    *tagset.HashingTagsAccumulator
	keyGenerator  *ckey.KeyGenerator
	pipelineCount int
}

// XXX(remy): implement this using fastrange instead https://github.com/lemire/fastrange
func fastrange(key ckey.ContextKey, pipelineCount int) uint64 {
	//	log.Infof("fastrange(%d, %d) = %d", uint64(key), uint64(pipelinesCount), uint64(key)%uint64(pipelinesCount))
	return uint64(key) % uint64(pipelineCount)
}

func newBatcher(demux aggregator.Demultiplexer) *batcher {
	agg := demux.Aggregator()
	e, sc := agg.GetBufferedChannels()
	pipelineCount := config.Datadog.GetInt("dogstatsd_pipeline_count")
	samples := make([][]metrics.MetricSample, pipelineCount)
	samplesCount := make([]int, pipelineCount)

	for i := range samples {
		samples[i] = agg.MetricSamplePool.GetBatch()
		samplesCount[i] = 0
	}

	return &batcher{
		samples:            samples,
		samplesCount:       samplesCount,
		metricSamplePool:   agg.MetricSamplePool,
		choutEvents:        e,
		choutServiceChecks: sc,

		demux:         demux,
		pipelineCount: pipelineCount,
		tagsBuffer:    tagset.NewHashingTagsAccumulator(),
		keyGenerator:  ckey.NewKeyGenerator(),
	}
}

func (b *batcher) appendSample(sample metrics.MetricSample) {
	var shardKey uint64
	if b.pipelineCount > 1 {
		b.tagsBuffer.Append(sample.Tags...) // XXX(remy): try to reuse this later in the pipeline
		h := b.keyGenerator.Generate(sample.Name, sample.Host, b.tagsBuffer)
		b.tagsBuffer.Reset()
		shardKey = fastrange(h, b.pipelineCount)
	}

	if b.samplesCount[shardKey] == len(b.samples[shardKey]) {
		b.flushSamples(shardKey)
	}

	b.samples[shardKey][b.samplesCount[shardKey]] = sample
	b.samplesCount[shardKey]++
}

func (b *batcher) appendEvent(event *metrics.Event) {
	b.events = append(b.events, event)
}

func (b *batcher) appendServiceCheck(serviceCheck *metrics.ServiceCheck) {
	b.serviceChecks = append(b.serviceChecks, serviceCheck)
}

func (b *batcher) flushSamples(shard uint64) {
	if b.samplesCount[shard] > 0 {
		t1 := time.Now()
		b.demux.AddTimeSamples(shard, b.samples[shard][:b.samplesCount[shard]])
		t2 := time.Now()
		tlmChannel.Observe(float64(t2.Sub(t1).Nanoseconds()), "metrics")

		b.samplesCount[shard] = 0
		b.samples[shard] = b.metricSamplePool.GetBatch()
	}
}

// flush pushes all batched metrics to the aggregator.
func (b *batcher) flush() {
	for i := 0; i < b.pipelineCount; i++ {
		b.flushSamples(uint64(i))
	}

	if len(b.events) > 0 {
		t1 := time.Now()
		b.choutEvents <- b.events
		t2 := time.Now()
		tlmChannel.Observe(float64(t2.Sub(t1).Nanoseconds()), "events")

		b.events = []*metrics.Event{}
	}
	if len(b.serviceChecks) > 0 {
		t1 := time.Now()
		b.choutServiceChecks <- b.serviceChecks
		t2 := time.Now()
		tlmChannel.Observe(float64(t2.Sub(t1).Nanoseconds()), "service_checks")

		b.serviceChecks = []*metrics.ServiceCheck{}
	}
}
