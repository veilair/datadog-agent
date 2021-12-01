// Unless explicitly stated otherwise all files in this repository are licensed
// under the Apache License Version 2.0.
// This product includes software developed at Datadog (https://www.datadoghq.com/).
// Copyright 2021-present Datadog, Inc.

package aggregator

import (
	"github.com/DataDog/datadog-agent/pkg/aggregator/ckey"
	"github.com/DataDog/datadog-agent/pkg/tagset"
	"github.com/DataDog/datadog-agent/pkg/telemetry"
)

// tags is used to keep track of tag slices shared by the contexts.
type tagsEntry struct {
	tags []string
	refs uint64
}

// tagsCache is a reference counted cache of the tags slices, to be
// shared between contexts.
type tagsCache struct {
	tagsByKey map[ckey.TagsKey]*tagsEntry
	cap       int
	enabled   bool
	telemetry tagsCacheTelemetry
}

func newTagsCache(enabled bool, name string) *tagsCache {
	return &tagsCache{
		tagsByKey: map[ckey.TagsKey]*tagsEntry{},
		enabled:   enabled,
		telemetry: *newTagsCacheTelemetry(name),
	}
}

// trackTags returns tag slice that corresponds to the key.  If key is
// already in the cache, we return existing slice. If the key is new,
// tags from the accumulator will be copied and associated with the
// key.
func (tc *tagsCache) insert(key ckey.TagsKey, tagsBuffer *tagset.HashingTagsAccumulator) []string {
	if !tc.enabled {
		copy := tagsBuffer.Copy()
		return copy
	}

	var tags []string
	if t := tc.tagsByKey[key]; t != nil {
		tags = t.tags
		t.refs++
		tc.telemetry.hits.Inc()
	} else {
		entry := &tagsEntry{
			tags: tagsBuffer.Copy(),
			refs: 1,
		}
		tags = entry.tags
		tc.tagsByKey[key] = entry
		tc.cap++
		tc.telemetry.miss.Inc()
	}

	return tags
}

// release is called when a context is removed, and its tags can be
// freed.
//
// Tags will be removed from the cache.
func (tc *tagsCache) release(key ckey.TagsKey) {
	if !tc.enabled {
		return
	}

	tags := tc.tagsByKey[key]

	tags.refs--
	if tags.refs == 0 {
		delete(tc.tagsByKey, key)
	}
}

// shrink will try release memory if cache usage drops low enough.
func (tc *tagsCache) shrink() {
	if len(tc.tagsByKey) < tc.cap/2 {
		new := make(map[ckey.TagsKey]*tagsEntry, len(tc.tagsByKey))
		for k, v := range tc.tagsByKey {
			new[k] = v
		}
		tc.cap = len(new)
		tc.tagsByKey = new
	}
}

func (tc *tagsCache) updateTelemetry() {
	t := &tc.telemetry

	tlmTagsCacheMaxSize.Set(float64(tc.cap), t.name)
	tlmTagsCacheSize.Set(float64(len(tc.tagsByKey)), t.name)

	minSize := 0
	maxSize := 0
	sumSize := 0
	for _, e := range tc.tagsByKey {
		tlmTagsCacheTagsetRefs.Observe(float64(e.refs), t.name)
		n := len(e.tags)
		if n < minSize {
			minSize = n
		}
		if n > maxSize {
			maxSize = n
		}
		sumSize += n
	}

	tlmTagsCacheTagsetSizeMin.Set(float64(minSize), t.name)
	tlmTagsCacheTagsetSizeMax.Set(float64(maxSize), t.name)
	tlmTagsCacheTagsetSizeSum.Set(float64(sumSize), t.name)
}

const tlmSubsystem = "aggregator_tags_cache"

var (
	tlmTagsCacheHits = telemetry.NewCounter(tlmSubsystem, "hits_total",
		[]string{"cache_instance_name"},
		"number of times cache already contained the tags")
	tlmTagsCacheMiss = telemetry.NewCounter(tlmSubsystem, "miss_total",
		[]string{"cache_instance_name"},
		"number of times cache did not contain the tags")

	tlmTagsCacheSize = telemetry.NewGauge(tlmSubsystem, "current_entries",
		[]string{"cache_instance_name"},
		"number of entries in the tags cache")
	tlmTagsCacheMaxSize = telemetry.NewGauge(tlmSubsystem, "max_entries",
		[]string{"cache_instance_name"},
		"maximum number of entries since last shrink")

	tlmTagsCacheTagsetSizeMin = telemetry.NewGauge(tlmSubsystem, "tagset_tags_size_min", []string{"cache_instance_name"}, "minimum number of tags in a tagset")
	tlmTagsCacheTagsetSizeMax = telemetry.NewGauge(tlmSubsystem, "tagset_tags_size_max", []string{"cache_instance_name"}, "maximum number of tags in a tagset")
	tlmTagsCacheTagsetSizeSum = telemetry.NewGauge(tlmSubsystem, "tagset_tags_size_sum", []string{"cache_instance_name"}, "total number of tags stored by the cache")

	tlmTagsCacheTagsetRefs = telemetry.NewHistogram(tlmSubsystem, "tagset_refs_count",
		[]string{"cache_instance_name"},
		"distribution of usage count of tagsets in the cache",
		[]float64{1, 2, 3, 4, 8, 16})
)

type tagsCacheTelemetry struct {
	hits telemetry.SimpleCounter
	miss telemetry.SimpleCounter
	name string
}

func newTagsCacheTelemetry(name string) *tagsCacheTelemetry {
	return &tagsCacheTelemetry{
		hits: tlmTagsCacheHits.WithValues(name),
		miss: tlmTagsCacheMiss.WithValues(name),
		name: name,
	}
}
