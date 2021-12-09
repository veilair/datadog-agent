// +build linux_bpf windows,npm

package http

import (
	"sync/atomic"

	"github.com/DataDog/datadog-agent/pkg/network/config"
)

type httpStatKeeper struct {
	stats      map[Key]RequestStats
	incomplete map[Key]httpTX
	maxEntries int
	telemetry  *telemetry

	// replace rules for HTTP path
	replaceRules []*config.ReplaceRule

	// http path buffer
	buffer []byte

	// map containing interned path strings
	// this is rotated  with the stats map
	interned map[string]string
}

func newHTTPStatkeeper(c *config.Config, telemetry *telemetry) *httpStatKeeper {
	return &httpStatKeeper{
		stats:        make(map[Key]RequestStats),
		incomplete:   make(map[Key]httpTX),
		maxEntries:   c.MaxHTTPStatsBuffered,
		replaceRules: c.HTTPReplaceRules,
		buffer:       make([]byte, HTTPBufferSize),
		interned:     make(map[string]string),
		telemetry:    telemetry,
	}
}

func (h *httpStatKeeper) Process(transactions []httpTX) {
	for _, tx := range transactions {
		if tx.Incomplete() {
			h.handleIncomplete(tx)
			continue
		}

		h.add(tx)
	}

	atomic.StoreInt64(&h.telemetry.aggregations, int64(len(h.stats)))
}

func (h *httpStatKeeper) GetAndResetAllStats() map[Key]RequestStats {
	ret := h.stats // No deep copy needed since `h.stats` gets reset
	h.stats = make(map[Key]RequestStats)
	h.incomplete = make(map[Key]httpTX)
	h.interned = make(map[string]string)
	return ret
}

func (h *httpStatKeeper) add(tx httpTX) {
	path, rejected := h.processHTTPPath(tx)
	if rejected {
		atomic.AddInt64(&h.telemetry.rejected, 1)
		return
	}

	key := h.newKey(tx, path)
	stats, ok := h.stats[key]
	if !ok && len(h.stats) >= h.maxEntries {
		atomic.AddInt64(&h.telemetry.dropped, 1)
		return
	}

	stats.AddRequest(tx.StatusClass(), tx.RequestLatency())
	h.stats[key] = stats
}

// handleIncomplete is responsible for handling incomplete transactions
// (eg. httpTX objects that have either only the request or response information)
// this happens only in the context of localhost traffic with NAT and these disjoint
// parts of the transactions are joined here by src port
func (h *httpStatKeeper) handleIncomplete(tx httpTX) {
	key := Key{
		SrcIPHigh: tx.SrcIPHigh(),
		SrcIPLow:  tx.SrcIPLow(),
		SrcPort:   tx.SrcPort(),
	}

	otherHalf, ok := h.incomplete[key]
	if !ok {
		if len(h.incomplete) >= h.maxEntries {
			atomic.AddInt64(&h.telemetry.dropped, 1)
		} else {
			h.incomplete[key] = tx
		}

		return
	}

	request, response := tx, otherHalf
	if response.StatusClass() == 0 {
		request, response = response, request
	}

	if request.request_started == 0 || response.response_status_code == 0 || request.request_started > response.response_last_seen {
		// This means we can't join these parts as they don't belong to the same transaction.
		// In this case, as a best-effort we override the incomplete entry with the latest one
		// we got from eBPF, so it can be joined by it's other half at a later moment.
		// This can happen because we can get out-of-order half transactions from eBPF
		atomic.AddInt64(&h.telemetry.dropped, 1)
		h.incomplete[key] = tx
		return
	}

	// Merge response into request
	request.SetStatusCode(response.StatusCode())
	request.SetLastSeen(response.LastSeen())
	h.add(request)
	delete(h.incomplete, key)
}

func (h *httpStatKeeper) newKey(tx httpTX, path string) Key {
	return Key{
		SrcIPHigh: tx.SrcIPHigh(),
		SrcIPLow:  tx.SrcIPLow(),
		SrcPort:   tx.SrcPort(),
		DstIPHigh: tx.DstIPHigh(),
		DstIPLow:  tx.DstIPLow(),
		DstPort:   tx.DstPort(),
		Path:      path,
		Method:    tx.Method(),
	}
}

func (h *httpStatKeeper) processHTTPPath(tx httpTX) (pathStr string, rejected bool) {
	path := getPath(tx.ReqFragment(), h.buffer)

	for _, r := range h.replaceRules {
		if r.Re.Match(path) {
			if r.Repl == "" {
				// this is a "drop" rule
				return "", true
			}

			path = r.Re.ReplaceAll(path, []byte(r.Repl))
		}
	}

	return h.intern(path), false
}

func (h *httpStatKeeper) intern(b []byte) string {
	v, ok := h.interned[string(b)]
	if !ok {
		v = string(b)
		h.interned[v] = v
	}
	return v
}

// getPath returns the URL from a request fragment with GET variables excluded.
// Example:
// For a request fragment "GET /foo?var=bar HTTP/1.1", this method will return "/foo"
func getPath(reqFragment, buffer []byte) []byte {
	// reqLen might contain a null terminator in the middle
	reqLen := len(reqFragment)
	for i := 0; i < reqLen; i++ {
		if reqFragment[i] == 0 {
			reqLen = i
			break
		}
	}

	var i, j int
	for i = 0; i < reqLen && reqFragment[i] != ' '; i++ {
	}

	i++

	for j = i; j < reqLen && reqFragment[j] != ' ' && reqFragment[j] != '?'; j++ {
	}

	if i < j && j <= reqLen {
		n := copy(buffer, reqFragment[i:j])
		return buffer[:n]
	}

	return nil
}
