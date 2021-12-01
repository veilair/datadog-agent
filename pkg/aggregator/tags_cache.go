// Unless explicitly stated otherwise all files in this repository are licensed
// under the Apache License Version 2.0.
// This product includes software developed at Datadog (https://www.datadoghq.com/).
// Copyright 2021-present Datadog, Inc.

package aggregator

import (
	"github.com/DataDog/datadog-agent/pkg/aggregator/ckey"
	"github.com/DataDog/datadog-agent/pkg/tagset"
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
}

func newTagsCache(enabled bool) tagsCache {
	return tagsCache{
		tagsByKey: map[ckey.TagsKey]*tagsEntry{},
		enabled:   enabled,
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
	} else {
		entry := &tagsEntry{
			tags: tagsBuffer.Copy(),
			refs: 1,
		}
		tags = entry.tags
		tc.tagsByKey[key] = entry
		tc.cap++
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
