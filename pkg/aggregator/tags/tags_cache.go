// Unless explicitly stated otherwise all files in this repository are licensed
// under the Apache License Version 2.0.
// This product includes software developed at Datadog (https://www.datadoghq.com/).
// Copyright 2021-present Datadog, Inc.

package tags

import (
	"fmt"
	"math/bits"

	"github.com/DataDog/datadog-agent/pkg/aggregator/ckey"
	"github.com/DataDog/datadog-agent/pkg/tagset"
	"github.com/DataDog/datadog-agent/pkg/telemetry"
)

// Entry is used to keep track of tag slices shared by the contexts.
type Entry struct {
	tags []string
	refs uint64
	key  ckey.TagsKey
}

// Tags returns the strings stored in the Entry.
func (e *Entry) Tags() []string {
	return e.tags
}

// Cache is a reference counted cache of the tags slices, to be
// shared between contexts.
type Cache struct {
	tagsByKey map[ckey.TagsKey]*Entry
	cap       int
	enabled   bool
	telemetry cacheTelemetry
}

// NewCache returns new empty Cache.
func NewCache(enabled bool, name string) *Cache {
	return &Cache{
		tagsByKey: map[ckey.TagsKey]*Entry{},
		enabled:   enabled,
		telemetry: *newCacheTelemetry(name),
	}
}

// Insert returns a cache Entry that corresponds to the key. If the
// key is not in the cache, a new entry is stored in the cache with
// the tags retrieved from the tagsBuffer.
func (tc *Cache) Insert(key ckey.TagsKey, tagsBuffer *tagset.HashingTagsAccumulator) *Entry {
	if !tc.enabled {
		return &Entry{
			tags: tagsBuffer.Copy(),
			refs: 1,
			key:  key,
		}
	}

	entry := tc.tagsByKey[key]
	if entry != nil {
		entry.refs++
		tc.telemetry.hits.Inc()
	} else {
		entry = &Entry{
			tags: tagsBuffer.Copy(),
			refs: 1,
			key:  key,
		}
		tc.tagsByKey[key] = entry
		tc.cap++
		tc.telemetry.miss.Inc()
	}

	return entry
}

// Release is called when a context is removed, and its tags can be
// freed.
//
// Tags will be removed from the cache.
func (tc *Cache) Release(e *Entry) {
	if !tc.enabled {
		return
	}

	key := e.key
	tags := tc.tagsByKey[key]

	tags.refs--
	if tags.refs == 0 {
		delete(tc.tagsByKey, key)
	}
}

// Shrink will try release memory if cache usage drops low enough.
func (tc *Cache) Shrink() {
	if len(tc.tagsByKey) < tc.cap/2 {
		new := make(map[ckey.TagsKey]*Entry, len(tc.tagsByKey))
		for k, v := range tc.tagsByKey {
			new[k] = v
		}
		tc.cap = len(new)
		tc.tagsByKey = new
	}
}

// UpdateTelemetry updates telemetry counters exported by the cache.
func (tc *Cache) UpdateTelemetry() {
	t := &tc.telemetry

	tlmMaxEntries.Set(float64(tc.cap), t.name)
	tlmEntries.Set(float64(len(tc.tagsByKey)), t.name)

	minSize := 0
	maxSize := 0
	sumSize := 0

	// 1, 2, 3, 4+, 8+, 16+, 32+, 64+
	var refFreq [8]uint64
	for _, e := range tc.tagsByKey {
		// refs is always positive

		r := e.refs
		if r <= 3 {
			refFreq[r-1]++
		} else if r <= 32 {
			refFreq[bits.Len64(r)]++ // Len(4) = 3, Len(32) = 6
		} else {
			refFreq[7]++
		}

		n := len(e.tags)
		if n < minSize {
			minSize = n
		}
		if n > maxSize {
			maxSize = n
		}
		sumSize += n
	}

	for i := 0; i < 3; i++ {
		tlmTagsetRefsCnt.Set(float64(refFreq[i]), t.name, fmt.Sprintf("%d", i+1))
	}
	for i := 3; i < 8; i++ {
		tlmTagsetRefsCnt.Set(float64(refFreq[i]), t.name, fmt.Sprintf("%d", 1<<(i-1)))
	}

	tlmTagsetMinTags.Set(float64(minSize), t.name)
	tlmTagsetMaxTags.Set(float64(maxSize), t.name)
	tlmTagsetSumTags.Set(float64(sumSize), t.name)
}

func newCounter(name string, help string, tags ...string) telemetry.Counter {
	return telemetry.NewCounter("aggregator_tags_cache", name,
		append([]string{"cache_instance_name"}, tags...), help)
}

func newGauge(name string, help string, tags ...string) telemetry.Gauge {
	return telemetry.NewGauge("aggregator_tags_cache", name,
		append([]string{"cache_instance_name"}, tags...), help)
}

var (
	tlmHits          = newCounter("hits_total", "number of times cache already contained the tags")
	tlmMiss          = newCounter("miss_total", "number of times cache did not contain the tags")
	tlmEntries       = newGauge("entries", "number of entries in the tags cache")
	tlmMaxEntries    = newGauge("max_entries", "maximum number of entries since last shrink")
	tlmTagsetMinTags = newGauge("tagset_min_tags", "minimum number of tags in a tagset")
	tlmTagsetMaxTags = newGauge("tagset_max_tags", "maximum number of tags in a tagset")
	tlmTagsetSumTags = newGauge("tagset_sum_tags", "total number of tags stored in all tagsets by the cache")
	tlmTagsetRefsCnt = newGauge("tagset_refs_count", "distribution of usage count of tagsets in the cache", "ge")
)

type cacheTelemetry struct {
	hits telemetry.SimpleCounter
	miss telemetry.SimpleCounter
	name string
}

func newCacheTelemetry(name string) *cacheTelemetry {
	return &cacheTelemetry{
		hits: tlmHits.WithValues(name),
		miss: tlmMiss.WithValues(name),
		name: name,
	}
}
