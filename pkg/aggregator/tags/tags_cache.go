// Unless explicitly stated otherwise all files in this repository are licensed
// under the Apache License Version 2.0.
// This product includes software developed at Datadog (https://www.datadoghq.com/).
// Copyright 2021-present Datadog, Inc.

package tags

import (
	"encoding/binary"
	"fmt"
	"math/bits"
	"sync/atomic"

	"github.com/DataDog/datadog-agent/pkg/aggregator/ckey"
	"github.com/DataDog/datadog-agent/pkg/tagset"
	"github.com/DataDog/datadog-agent/pkg/telemetry"
)

// Entry is used to keep track of tag slices shared by the contexts.
type Entry struct {
	tags []string
	refs uint64

	tagsIndexed []byte
	parentCache *Cache
}

// Tags returns tags stored in the Entry as a new slice of strings.
func (e *Entry) Tags() []string {
	if e.parentCache == nil {
		return e.tags
	}
	return e.parentCache.readTags(e)
}

// Cache is a reference counted cache of the tags slices, to be
// shared between contexts.
type Cache struct {
	tagsByKey map[ckey.TagsKey]*Entry
	cap       int
	enabled   bool
	telemetry cacheTelemetry

	index    map[string]int
	strings  atomic.Value
	free     []int
	indexbuf []int
}

// NewCache returns new empty Cache.
func NewCache(enabled bool, name string) *Cache {
	c := &Cache{
		tagsByKey: map[ckey.TagsKey]*Entry{},
		enabled:   enabled,
		telemetry: *newCacheTelemetry(name),
		index:     make(map[string]int),
	}
	c.strings.Store([]string{})

	return c
}

// Insert returns a cache Entry that corresponds to the key. If the
// key is not in the cache, a new entry is stored in the cache with
// the tags retrieved from the tagsBuffer.
//
// Insert is not reentrant and should not be called concurrently.
// Insert can be called concurrently with Entry.Tags.
// Insert must not be called concurrently with Cache.Shrink.
func (tc *Cache) Insert(key ckey.TagsKey, tagsBuffer *tagset.HashingTagsAccumulator) *Entry {
	if !tc.enabled {
		return &Entry{
			tags: tagsBuffer.Copy(),
			refs: 1,
		}
	}

	entry := tc.tagsByKey[key]
	if entry != nil {
		entry.refs++
		tc.telemetry.hits.Inc()
	} else {
		entry = &Entry{
			refs: 1,

			tagsIndexed: tc.internTags(tagsBuffer.Get()),
			parentCache: tc,
		}
		tc.tagsByKey[key] = entry
		tc.cap++
		tc.telemetry.miss.Inc()
	}

	return entry
}

func (tc *Cache) internTags(tags []string) []byte {
	strings := tc.strings.Load().([]string)

	for _, tag := range tags {
		idx, ok := tc.index[tag]
		if !ok {
			if len(tc.free) > 0 {
				l := len(tc.free)
				idx, tc.free = tc.free[l], tc.free[:l]
				strings[idx] = tag
			} else {
				idx = len(strings)
				strings = append(strings, tag)
			}
			tc.index[tag] = idx
		}
		tc.indexbuf = append(tc.indexbuf, idx)
	}

	tc.strings.Store(strings)

	totb := 0
	for _, idx := range tc.indexbuf {
		numb := bits.Len(uint(idx))
		totb += numb / 7
		if numb == 0 || numb%7 > 0 {
			totb++
		}
	}

	tagi := make([]byte, totb)
	pos := 0
	for _, idx := range tc.indexbuf {
		pos += binary.PutUvarint(tagi[pos:], uint64(idx))
	}

	tc.indexbuf = tc.indexbuf[:0]

	return tagi
}

func (tc *Cache) readTags(e *Entry) []string {
	pos := 0
	tags := make([]string, 0, varLen(e.tagsIndexed))
	strings := tc.strings.Load().([]string)
	for pos < len(e.tagsIndexed) {
		idx, n := binary.Uvarint(e.tagsIndexed[pos:])
		pos += n
		tags = append(tags, strings[idx])
	}

	return tags
}

// Acquire increments reference count on an Entry.
//
// It should be called when a copy of an Entry is required to outlive
// Shrink.
func (e *Entry) Acquire() *Entry {
	atomic.AddUint64(&e.refs, 1)
	return e
}

// Release decrements Entry's reference count. Once reference count
// reaches zero, Entry will be removed from the cache during next
// Shrink operation.
//
// Each Release call should be paired to an Insert call.
func (e *Entry) Release() {
	atomic.AddUint64(&e.refs, ^uint64(0))
}

// Shrink removes unused entries and will reduce the internal map
// size.
//
// Inserts can not happen concurrently while Shrink is running.
func (tc *Cache) Shrink() {
	for k, e := range tc.tagsByKey {
		if atomic.LoadUint64(&e.refs) == 0 {
			delete(tc.tagsByKey, k)
		}
	}

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

		r := atomic.LoadUint64(&e.refs)
		if r <= 3 {
			refFreq[r-1]++
		} else if r <= 32 {
			refFreq[bits.Len64(r)]++ // Len(4) = 3, Len(32) = 6
		} else {
			refFreq[7]++
		}

		n := varLen(e.tagsIndexed)
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

	tlmTotalStrings.Set(float64(len(tc.index)), t.name)
	tlmTotalSlots.Set(float64(len(tc.strings.Load().([]string))), t.name)
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

	tlmTotalStrings = newGauge("count_strings", "number of unique strings tracked by the cache")
	tlmTotalSlots   = newGauge("count_slots", "total number of slots allocated for strings")
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

func varLen(b []byte) int {
	n := 0
	for _, v := range b {
		if v&0x80 == 0 {
			n++
		}
	}
	return n
}
