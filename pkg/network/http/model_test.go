// +build linux_bpf

package http

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestLatency(t *testing.T) {
	tx := httpTX{
		response_last_seen: 2e6,
		request_started:    1e6,
	}
	// quantization brings it down
	assert.Equal(t, 999424.0, tx.RequestLatency())
}
