// +build windows,npm

package http

import (
	"encoding/binary"
	"errors"
	"time"
	"fmt"

	"github.com/DataDog/datadog-agent/pkg/network/driver"
	"github.com/DataDog/datadog-agent/pkg/process/util"
)

const HTTPBufferSize = driver.HttpBufferSize
const HTTPBatchSize = driver.HttpBatchSize

type httpTX driver.HttpTransactionType

// errLostBatch isn't a valid error in windows
var errLostBatch = errors.New("invalid error")

// ReqFragment returns a byte slice containing the first HTTPBufferSize bytes of the request
func (tx *httpTX) ReqFragment() []byte {
	return tx.RequestFragment[:]
}

// StatusClass returns an integer representing the status code class
// Example: a 404 would return 400
func (tx *httpTX) StatusClass() int {
	return (int(tx.ResponseStatusCode) / 100) * 100
}

// RequestLatency returns the latency of the request in nanoseconds
func (tx *httpTX) RequestLatency() float64 {
	return nsTimestampToFloat(uint64(tx.ResponseLastSeen - tx.RequestStarted))
}

// Incomplete returns true if the transaction contains only the request or response information
// This happens in the context of localhost with NAT, in which case we join the two parts in userspace
func (tx *httpTX) Incomplete() bool {
	return tx.RequestStarted == 0 || tx.ResponseStatusCode == 0
}

func (tx *httpTX) SrcIPLow() uint64 {
	return binary.BigEndian.Uint64(tx.Tup.Saddr[:8])
}

func (tx *httpTX) SrcIPHigh() uint64 {
	return binary.BigEndian.Uint64(tx.Tup.Saddr[8:16])
}

func (tx *httpTX) SrcPort() uint16 {
	return tx.Tup.Sport
}

func (tx *httpTX) DstIPLow() uint64 {
	return binary.BigEndian.Uint64(tx.Tup.Daddr[:8])
}

func (tx *httpTX) DstIPHigh() uint64 {
	return binary.BigEndian.Uint64(tx.Tup.Daddr[8:16])
}

func (tx *httpTX) DstPort() uint16 {
	return tx.Tup.Dport
}

func (tx *httpTX) Method() Method {
	return Method(tx.RequestMethod)
}

func (tx *httpTX) StatusCode() uint16 {
	return tx.ResponseStatusCode
}

func (tx *httpTX) LastSeen() uint64 {
	return tx.ResponseLastSeen
}

func (tx *httpTX) SetStatusCode(sc uint16) {
	tx.ResponseStatusCode = sc
}

func (tx *httpTX) SetLastSeen(ts uint64) {
	tx.ResponseLastSeen = ts
}

// below is copied from pkg/trace/stats/statsraw.go
// 10 bits precision (any value will be +/- 1/1024)
const roundMask uint64 = 1 << 10

// nsTimestampToFloat converts a nanosec timestamp into a float nanosecond timestamp truncated to a fixed precision
func nsTimestampToFloat(ns uint64) float64 {
	var shift uint
	for ns > roundMask {
		ns = ns >> 1
		shift++
	}
	return float64(ns << shift)
}

// generateIPv4HTTPTransaction is a testing helper function required for the http_statkeeper tests
func generateIPv4HTTPTransaction(source util.Address, dest util.Address, sourcePort int, destPort int, path string, code int, latency time.Duration) httpTX {
	var tx httpTX

	reqFragment := fmt.Sprintf("GET %s HTTP/1.1\nHost: example.com\nUser-Agent: example-browser/1.0", path)
	latencyNS := uint64(uint64(latency))
	src := source.Bytes()
	dst := dest.Bytes()

	tx.RequestStarted = 1
	tx.ResponseLastSeen = tx.RequestStarted + latencyNS
	tx.ResponseStatusCode = uint16(code)
	for i := 0; i < len(tx.RequestFragment) && i < len(reqFragment); i++ {
		tx.RequestFragment[i] = uint8(reqFragment[i])
	}
	for i:= 0; i < len(tx.Tup.Saddr) && i < len(src); i++ {
		tx.Tup.Saddr[i] = src[i]
	}
	for i:= 0; i < len(tx.Tup.Daddr) && i < len(dst); i++ {
		tx.Tup.Daddr[i] = dst[i]
	}
	tx.Tup.Sport = uint16(sourcePort)
	tx.Tup.Dport = uint16(destPort)

	return tx
}
