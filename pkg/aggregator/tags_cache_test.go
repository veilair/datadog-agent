// Unless explicitly stated otherwise all files in this repository are licensed
// under the Apache License Version 2.0.
// This product includes software developed at Datadog (https://www.datadoghq.com/).
// Copyright 2021-present Datadog, Inc.

package aggregator

import (
	"testing"

	"github.com/DataDog/datadog-agent/pkg/tagset"

	"github.com/stretchr/testify/require"
)

func TestCache(t *testing.T) {
	c := newTagsCache(true, "test")

	t1 := tagset.NewHashingTagsAccumulatorWithTags([]string{"1"})
	t2 := tagset.NewHashingTagsAccumulatorWithTags([]string{"2"})

	t1a := c.insert(1, t1)

	require.EqualValues(t, 1, len(c.tagsByKey))
	require.EqualValues(t, 1, c.cap)
	require.EqualValues(t, 1, c.tagsByKey[1].refs)

	t1b := c.insert(1, t1)
	require.EqualValues(t, 1, len(c.tagsByKey))
	require.EqualValues(t, 1, c.cap)
	require.EqualValues(t, 2, c.tagsByKey[1].refs)
	require.Same(t, t1a, t1b)

	t2a := c.insert(2, t2)
	require.EqualValues(t, 2, len(c.tagsByKey))
	require.EqualValues(t, 2, c.cap)
	require.EqualValues(t, 2, c.tagsByKey[1].refs)
	require.EqualValues(t, 1, c.tagsByKey[2].refs)
	require.NotSame(t, t1a, t2a)

	t2b := c.insert(2, t2)
	require.EqualValues(t, 2, len(c.tagsByKey))
	require.EqualValues(t, 2, c.cap)
	require.EqualValues(t, 2, c.tagsByKey[1].refs)
	require.EqualValues(t, 2, c.tagsByKey[2].refs)
	require.Same(t, t2a, t2b)

	c.release(1)
	require.EqualValues(t, 2, len(c.tagsByKey))
	require.EqualValues(t, 2, c.cap)
	require.EqualValues(t, 1, c.tagsByKey[1].refs)
	require.EqualValues(t, 2, c.tagsByKey[2].refs)

	c.shrink()
	require.EqualValues(t, 2, len(c.tagsByKey))
	require.EqualValues(t, 2, c.cap)

	c.release(2)
	require.EqualValues(t, 2, len(c.tagsByKey))
	require.EqualValues(t, 2, c.cap)
	require.EqualValues(t, 1, c.tagsByKey[1].refs)
	require.EqualValues(t, 1, c.tagsByKey[2].refs)

	c.release(1)
	require.EqualValues(t, 1, len(c.tagsByKey))
	require.EqualValues(t, 2, c.cap)
	require.EqualValues(t, 1, c.tagsByKey[2].refs)

	c.release(2)
	require.EqualValues(t, 0, len(c.tagsByKey))

	c.shrink()
	require.EqualValues(t, 0, c.cap)
}

func TestCacheDisabled(t *testing.T) {
	c := newTagsCache(false, "test")

	t1 := tagset.NewHashingTagsAccumulatorWithTags([]string{"1"})
	t2 := tagset.NewHashingTagsAccumulatorWithTags([]string{"2"})

	t1a := c.insert(1, t1)
	require.EqualValues(t, 0, len(c.tagsByKey))
	require.EqualValues(t, 0, c.cap)

	t1b := c.insert(1, t1)
	require.EqualValues(t, 0, len(c.tagsByKey))
	require.EqualValues(t, 0, c.cap)
	require.NotSame(t, t1a, t1b)
	require.Equal(t, t1a, t1b)

	t2a := c.insert(2, t2)
	require.EqualValues(t, 0, len(c.tagsByKey))
	require.EqualValues(t, 0, c.cap)
	require.NotSame(t, t1a, t2a)
	require.NotEqual(t, t1a, t2a)

	c.release(1)
	require.EqualValues(t, 0, len(c.tagsByKey))
	require.EqualValues(t, 0, c.cap)

	c.release(2)
	require.EqualValues(t, 0, len(c.tagsByKey))
	require.EqualValues(t, 0, c.cap)

	c.shrink()
	require.EqualValues(t, 0, len(c.tagsByKey))
	require.EqualValues(t, 0, c.cap)
}
