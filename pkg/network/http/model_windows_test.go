// +build windows,npm

package http

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestLatency(t *testing.T) {
	tx := httpTX{
		ResponseLastSeen: 2e6,
		RequestStarted:   1e6,
	}
	// quantization brings it down
	assert.Equal(t, 999424.0, tx.RequestLatency())
}
