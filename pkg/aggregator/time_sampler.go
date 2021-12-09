// Unless explicitly stated otherwise all files in this repository are licensed
// under the Apache License Version 2.0.
// This product includes software developed at Datadog (https://www.datadoghq.com/).
// Copyright 2016-present Datadog, Inc.

package aggregator

import (
	"time"

	"github.com/DataDog/datadog-agent/pkg/aggregator/ckey"
	"github.com/DataDog/datadog-agent/pkg/config"
	"github.com/DataDog/datadog-agent/pkg/metrics"
	"github.com/DataDog/datadog-agent/pkg/serializer"
	"github.com/DataDog/datadog-agent/pkg/util/log"
)

// SerieSignature holds the elements that allow to know whether two similar `Serie`s
// from the same bucket can be merged into one
type SerieSignature struct {
	mType      metrics.APIMetricType
	contextKey ckey.ContextKey
	nameSuffix string
}

type tsSample struct {
	sample    *metrics.MetricSample
	timestamp float64
}

// TimeSamplerID is a type ID for sharded time samplers.
type TimeSamplerID int

// TimeSampler aggregates metrics by buckets of 'interval' seconds
type TimeSampler struct {
	interval                    int64
	contextResolver             *timestampContextResolver
	metricsByTimestamp          map[int64]metrics.ContextMetrics
	counterLastSampledByContext map[ckey.ContextKey]float64
	lastCutOffTime              int64
	sketchMap                   sketchMap

	// id is a number to differentiate multiple time samplers
	// since we start running more than one with the demultiplexer introduction
	id TimeSamplerID

	serializer serializer.MetricSerializer

	stopChan chan struct{}

	// TODO(remy): comment me
	samples chan tsSample
}

// NewTimeSampler returns a newly initialized TimeSampler
func NewTimeSampler(id TimeSamplerID, interval int64, serializer serializer.MetricSerializer) *TimeSampler {
	if interval == 0 {
		interval = bucketSize
	}
	log.Infof("Creating TimeSample #%d", id)

	ts := &TimeSampler{
		interval:                    interval,
		contextResolver:             newTimestampContextResolver(),
		metricsByTimestamp:          map[int64]metrics.ContextMetrics{},
		counterLastSampledByContext: map[ckey.ContextKey]float64{},
		sketchMap:                   make(sketchMap),
		id:                          id,
		stopChan:                    make(chan struct{}),
		serializer:                  serializer,
		samples:                     make(chan tsSample, 100), // XXX(remy): size
	}

	go ts.processLoop(time.Second * time.Duration(interval))

	return ts
}

func (s *TimeSampler) calculateBucketStart(timestamp float64) int64 {
	return int64(timestamp) - int64(timestamp)%s.interval
}

func (s *TimeSampler) isBucketStillOpen(bucketStartTimestamp, timestamp int64) bool {
	return bucketStartTimestamp+s.interval > timestamp
}

// Add the metricSample to the correct bucket
func (s *TimeSampler) addSample(metricSample *metrics.MetricSample, timestamp float64) {
	// XXX(remy): temporary telemetry tag to know in which samplers the sample has been distributed
	//	metricSample.Tags = append(metricSample.Tags, fmt.Sprintf("timesampler:%d", s.id))

	s.samples <- tsSample{sample: metricSample, timestamp: timestamp}
}

func (s *TimeSampler) sample(metricSample *metrics.MetricSample, timestamp float64) {
	// Keep track of the context
	contextKey := s.contextResolver.trackContext(metricSample, timestamp)
	bucketStart := s.calculateBucketStart(timestamp)

	switch metricSample.Mtype {
	case metrics.DistributionType:
		s.sketchMap.insert(bucketStart, contextKey, metricSample.Value, metricSample.SampleRate)
	default:
		// If it's a new bucket, initialize it
		bucketMetrics, ok := s.metricsByTimestamp[bucketStart]
		if !ok {
			bucketMetrics = metrics.MakeContextMetrics()
			s.metricsByTimestamp[bucketStart] = bucketMetrics
		}
		// Update LastSampled timestamp for counters
		if metricSample.Mtype == metrics.CounterType {
			s.counterLastSampledByContext[contextKey] = timestamp
		}

		// Add sample to bucket
		if err := bucketMetrics.AddSample(contextKey, metricSample, timestamp, s.interval, nil); err != nil {
			log.Debugf("TimeSampler #%d Ignoring sample '%s' on host '%s' and tags '%s': %s", s.id, metricSample.Name, metricSample.Host, metricSample.Tags, err)
		}
	}
}

// Stop stops the time sampler. It can't be re-used after being stop,
// use NewTimeSampler instead.
func (s *TimeSampler) Stop() {
	s.stopChan <- struct{}{}
}

// We process all receivend samples in the `select`, but we also process a flush action,
// meaning that the time sampler will not process any sample while it is flushing.
// Note that it was the same design in the BufferedAggregator (but at the aggregator level,
// not sampler level).
// If we want to move to a design where we can flush while we are processing samples,
// we could consider implementing double-buffering or locking for every sample reception.
func (s *TimeSampler) processLoop(interval time.Duration) {
	ticker := time.NewTicker(interval)
	for {
		select {
		case <-s.stopChan:
			return
		case tss := <-s.samples:
			s.sample(tss.sample, tss.timestamp)
		case t := <-ticker.C:
			series, sketches := s.flush(float64(t.Unix())) // XXX(remy): is this conversation correct? note that it is in second
			// XXX(remy): better error management
			if s.serializer != nil {
				if err := s.serializer.SendSeries(series); err != nil {
					log.Errorf("flushLoop: %+v", err)
				}
				if err := s.serializer.SendSketch(sketches); err != nil {
					log.Errorf("flushLoop: %+v", err)
				}
			}
		}
	}
}

func (s *TimeSampler) newSketchSeries(ck ckey.ContextKey, points []metrics.SketchPoint) metrics.SketchSeries {
	ctx, _ := s.contextResolver.get(ck)
	ss := metrics.SketchSeries{
		Name:       ctx.Name,
		Tags:       ctx.Tags,
		Host:       ctx.Host,
		Interval:   s.interval,
		Points:     points,
		ContextKey: ck,
	}

	return ss
}

func (s *TimeSampler) flushSeries(cutoffTime int64) metrics.Series {
	var series []*metrics.Serie
	var rawSeries []*metrics.Serie

	serieBySignature := make(map[SerieSignature]*metrics.Serie)
	// Map to hold the expired contexts that will need to be deleted after the flush so that we stop sending zeros
	counterContextsToDelete := map[ckey.ContextKey]struct{}{}

	if len(s.metricsByTimestamp) > 0 {
		for bucketTimestamp, contextMetrics := range s.metricsByTimestamp {
			// disregard when the timestamp is too recent
			if s.isBucketStillOpen(bucketTimestamp, cutoffTime) {
				continue
			}

			// Add a 0 sample to all the counters that are not expired.
			// It is ok to add 0 samples to a counter that was already sampled for real in the bucket, since it won't change its value
			s.countersSampleZeroValue(bucketTimestamp, contextMetrics, counterContextsToDelete)

			rawSeries = append(rawSeries, s.flushContextMetrics(bucketTimestamp, contextMetrics)...)

			delete(s.metricsByTimestamp, bucketTimestamp)
		}
	} else if s.lastCutOffTime+s.interval <= cutoffTime {
		// Even if there is no metric in this flush, recreate empty counters,
		// but only if we've passed an interval since the last flush

		contextMetrics := metrics.MakeContextMetrics()

		s.countersSampleZeroValue(cutoffTime-s.interval, contextMetrics, counterContextsToDelete)

		rawSeries = append(rawSeries, s.flushContextMetrics(cutoffTime-s.interval, contextMetrics)...)
	}

	// Delete the contexts associated to an expired counter
	for context := range counterContextsToDelete {
		delete(s.counterLastSampledByContext, context)
	}

	for _, serie := range rawSeries {
		serieSignature := SerieSignature{serie.MType, serie.ContextKey, serie.NameSuffix}

		if existingSerie, ok := serieBySignature[serieSignature]; ok {
			existingSerie.Points = append(existingSerie.Points, serie.Points[0])
		} else {
			// Resolve context and populate new Serie
			context, ok := s.contextResolver.get(serie.ContextKey)
			if !ok {
				log.Errorf("TimeSampler #%d Ignoring all metrics on context key '%v': inconsistent context resolver state: the context is not tracked", s.id, serie.ContextKey)
				continue
			}
			serie.Name = context.Name + serie.NameSuffix
			serie.Tags = context.Tags
			serie.Host = context.Host
			serie.Interval = s.interval

			serieBySignature[serieSignature] = serie
			series = append(series, serie)
		}
	}

	return series
}

func (s *TimeSampler) flushSketches(cutoffTime int64) metrics.SketchSeriesList {
	pointsByCtx := make(map[ckey.ContextKey][]metrics.SketchPoint)
	sketches := make(metrics.SketchSeriesList, 0, len(pointsByCtx))

	s.sketchMap.flushBefore(cutoffTime, func(ck ckey.ContextKey, p metrics.SketchPoint) {
		if p.Sketch == nil {
			return
		}
		pointsByCtx[ck] = append(pointsByCtx[ck], p)
	})
	for ck, points := range pointsByCtx {
		sketches = append(sketches, s.newSketchSeries(ck, points))
	}

	return sketches
}

func (s *TimeSampler) flush(timestamp float64) (metrics.Series, metrics.SketchSeriesList) {
	// Compute a limit timestamp
	cutoffTime := s.calculateBucketStart(timestamp)

	series := s.flushSeries(cutoffTime)
	sketches := s.flushSketches(cutoffTime)

	// expiring contexts
	s.contextResolver.expireContexts(timestamp - config.Datadog.GetFloat64("dogstatsd_context_expiry_seconds"))
	s.lastCutOffTime = cutoffTime

	aggregatorDogstatsdContexts.Set(int64(s.contextResolver.length()))
	tlmDogstatsdContexts.Set(float64(s.contextResolver.length()))
	return series, sketches
}

// flushContextMetrics flushes the passed contextMetrics, handles its errors, and returns its series
func (s *TimeSampler) flushContextMetrics(timestamp int64, contextMetrics metrics.ContextMetrics) []*metrics.Serie {
	series, errors := contextMetrics.Flush(float64(timestamp))
	for ckey, err := range errors {
		context, ok := s.contextResolver.get(ckey)
		if !ok {
			log.Errorf("Can't resolve context of error '%s': inconsistent context resolver state: context with key '%v' is not tracked", err, ckey)
			continue
		}
		log.Infof("No value returned for dogstatsd metric '%s' on host '%s' and tags '%s': %s", context.Name, context.Host, context.Tags, err)
	}
	return series
}

func (s *TimeSampler) countersSampleZeroValue(timestamp int64, contextMetrics metrics.ContextMetrics, counterContextsToDelete map[ckey.ContextKey]struct{}) {
	expirySeconds := config.Datadog.GetFloat64("dogstatsd_expiry_seconds")
	for counterContext, lastSampled := range s.counterLastSampledByContext {
		if expirySeconds+lastSampled > float64(timestamp) {
			sample := &metrics.MetricSample{
				Name:       "",
				Value:      0.0,
				RawValue:   "0.0",
				Mtype:      metrics.CounterType,
				Tags:       []string{},
				Host:       "",
				SampleRate: 1,
				Timestamp:  float64(timestamp),
			}
			// Add a zero value sample to the counter
			// It is ok to add a 0 sample to a counter that was already sampled in the bucket, it won't change its value
			contextMetrics.AddSample(counterContext, sample, float64(timestamp), s.interval, nil) //nolint:errcheck

			// Update the tracked context so that the contextResolver doesn't expire counter contexts too early
			// i.e. while we are still sending zeros for them
			err := s.contextResolver.updateTrackedContext(counterContext, float64(timestamp))
			if err != nil {
				log.Errorf("Error updating context: %s", err)
			}
		} else {
			// Register the context to be deleted
			counterContextsToDelete[counterContext] = struct{}{}
		}
	}
}
