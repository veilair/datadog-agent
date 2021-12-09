// +build windows,npm

package http

import (
	"sync"
	"time"

	"github.com/DataDog/datadog-agent/pkg/network/config"
	"github.com/DataDog/datadog-agent/pkg/network/driver"
	"github.com/DataDog/datadog-agent/pkg/util/log"
)

const (
	defaultMaxTrackedConnections = 65536
)

// Monitor is the interface to HTTP monitoring
type Monitor interface {
	Start()
	GetHTTPStats() map[Key]RequestStats
	Stop() error
}

// DriverMonitor is responsible for aggregating and emitting metrics based on
// batches of HTTP transactions received from the driver interface
type DriverMonitor struct {
	di         *httpDriverInterface
	telemetry  *telemetry
	statkeeper *httpStatKeeper

	mux         sync.Mutex
	eventLoopWG sync.WaitGroup
}

// NewDriverMonitor returns a new DriverMonitor instance
func NewDriverMonitor(c *config.Config) (Monitor, error) {
	di, err := newDriverInterface()
	if err != nil {
		return nil, err
	}

	if uint64(c.MaxTrackedConnections) != defaultMaxTrackedConnections {
		maxFlows := uint64(c.MaxTrackedConnections)
		err := di.setMaxFlows(maxFlows)
		if err != nil {
			log.Warnf("Failed to set max number of flows in driver http filter to %v %v", maxFlows, err)
		}
	}

	telemetry := newTelemetry()

	return &DriverMonitor{
		di:         di,
		telemetry:  telemetry,
		statkeeper: newHTTPStatkeeper(c.MaxHTTPStatsBuffered, telemetry),
	}, nil
}

// Start consuming HTTP events
func (m *DriverMonitor) Start() {
	m.di.startReadingBuffers()

	m.eventLoopWG.Add(1)
	go func() {
		defer m.eventLoopWG.Done()
		report := time.NewTicker(30 * time.Second)
		defer report.Stop()
		for {
			select {
			case transactionBatch, ok := <-m.di.dataChannel:
				if !ok {
					return
				}
				m.process(transactionBatch)
			case <-report.C:
				err := m.di.flushPendingTransactions()
				if err != nil {
					log.Warnf("Failed to flush pending http transactions: %v", err)
				}
			}
		}
	}()

	return
}

func (m *DriverMonitor) process(transactionBatch []driver.HttpTransactionType) {
	transactions := make([]httpTX, len(transactionBatch))
	for i := range transactionBatch {
		transactions[i] = httpTX(transactionBatch[i])
	}

	m.mux.Lock()
	defer m.mux.Unlock()

	m.telemetry.aggregate(transactions, nil)

	m.statkeeper.Process(transactions)
}

// GetHTTPStats returns a map of HTTP stats stored in the following format:
// [source, dest tuple, request path] -> RequestStats object
func (m *DriverMonitor) GetHTTPStats() map[Key]RequestStats {
	m.mux.Lock()
	defer m.mux.Unlock()

	delta := m.telemetry.reset()
	delta.report()

	return m.statkeeper.GetAndResetAllStats()
}

// Stop HTTP monitoring
func (m *DriverMonitor) Stop() error {
	err := m.di.close()
	m.eventLoopWG.Wait()
	return err
}
