// +build windows,npm

package http

/*
#include <stdlib.h>
#include <memory.h>
#include "../driver/ddnpmapi.h"

int size_of_http_transaction_type() {
    return sizeof(HTTP_TRANSACTION_TYPE);
}
*/
import "C"
import (
	"errors"
	"fmt"
	"net"
	"sync"
	"unsafe"

	"github.com/DataDog/datadog-agent/pkg/network/driver"
	"github.com/DataDog/datadog-agent/pkg/util/log"
	"golang.org/x/sys/windows"
)

const (
	httpReadBufferCount = 100
)

type httpDriverInterface struct {
	driverHTTPHandle *driver.Handle
	readBuffers      []*driver.ReadBuffer
	iocp             windows.Handle

	dataChannel chan []driver.HttpTransactionType
	eventLoopWG sync.WaitGroup
}

func newDriverInterface() (*httpDriverInterface, error) {
	d := &httpDriverInterface{}
	err := d.setupHTTPHandle()
	if err != nil {
		return nil, err
	}

	d.dataChannel = make(chan []driver.HttpTransactionType)
	return d, nil
}

func (di *httpDriverInterface) setupHTTPHandle() error {
	dh, err := driver.NewHandle(windows.FILE_FLAG_OVERLAPPED, driver.HTTPHandle)
	if err != nil {
		return err
	}

	filters, err := createHTTPFilters()
	if err != nil {
		return err
	}

	if err := dh.SetHTTPFilters(filters); err != nil {
		return err
	}

	iocp, buffers, err := driver.PrepareCompletionBuffers(dh.Handle, httpReadBufferCount)
	if err != nil {
		return err
	}

	di.driverHTTPHandle = dh
	di.iocp = iocp
	di.readBuffers = buffers
	return nil
}

func createHTTPFilters() ([]driver.FilterDefinition, error) {
	ifaces, err := net.Interfaces()
	if err != nil {
		return nil, err
	}

	var filters []driver.FilterDefinition
	for _, iface := range ifaces {
		// IPv4
		filters = append(filters, driver.FilterDefinition{
			FilterVersion:  driver.Signature,
			Size:           driver.FilterDefinitionSize,
			Direction:      driver.DirectionOutbound,
			FilterLayer:    driver.LayerTransport,
			InterfaceIndex: uint64(iface.Index),
			Af:             windows.AF_INET,
			Protocol:       windows.IPPROTO_TCP,
		}, driver.FilterDefinition{
			FilterVersion:  driver.Signature,
			Size:           driver.FilterDefinitionSize,
			Direction:      driver.DirectionInbound,
			FilterLayer:    driver.LayerTransport,
			InterfaceIndex: uint64(iface.Index),
			Af:             windows.AF_INET,
			Protocol:       windows.IPPROTO_TCP,
		})

		// IPv6
		filters = append(filters, driver.FilterDefinition{
			FilterVersion:  driver.Signature,
			Size:           driver.FilterDefinitionSize,
			Direction:      driver.DirectionOutbound,
			FilterLayer:    driver.LayerTransport,
			InterfaceIndex: uint64(iface.Index),
			Af:             windows.AF_INET6,
			Protocol:       windows.IPPROTO_TCP,
		}, driver.FilterDefinition{
			FilterVersion:  driver.Signature,
			Size:           driver.FilterDefinitionSize,
			Direction:      driver.DirectionInbound,
			FilterLayer:    driver.LayerTransport,
			InterfaceIndex: uint64(iface.Index),
			Af:             windows.AF_INET6,
			Protocol:       windows.IPPROTO_TCP,
		})
	}

	return filters, nil
}

func (di *httpDriverInterface) setMaxFlows(maxFlows uint64) error {
	log.Debugf("Setting max flows in driver http filter to %v", maxFlows)
	err := windows.DeviceIoControl(di.driverHTTPHandle.Handle,
		driver.SetMaxFlowsIOCTL,
		(*byte)(unsafe.Pointer(&maxFlows)),
		uint32(unsafe.Sizeof(maxFlows)),
		nil,
		uint32(0), nil, nil)
	return err
}

func (di *httpDriverInterface) startReadingBuffers() {
	di.eventLoopWG.Add(1)
	go func() {
		defer di.eventLoopWG.Done()

		transactionSize := uint32(C.size_of_http_transaction_type())
		for {
			buf, bytesRead, err := driver.GetReadBufferWhenReady(di.iocp)
			if iocpIsClosedError(err) {
				log.Debug("http io completion port is closed. stopping http monitoring")
				return
			}
			if err != nil {
				log.Warnf("Error reading http transaction buffer: %v", err)
				continue
			}

			batchSize := bytesRead / transactionSize
			transactionBatch := make([]driver.HttpTransactionType, batchSize)

			for i := uint32(0); i < batchSize; i++ {
				transactionBatch[i] = *(*driver.HttpTransactionType)(unsafe.Pointer(&buf.Data[i*transactionSize]))
			}

			di.dataChannel <- transactionBatch

			err = driver.StartNextRead(di.driverHTTPHandle.Handle, buf)
			if err != nil && err != windows.ERROR_IO_PENDING {
				log.Warnf("Error starting next http transaction read: %v")
			}
		}
	}()
}

func iocpIsClosedError(err error) bool {
	if err == nil {
		return false
	}
	// ERROR_OPERATION_ABORTED or ERROR_ABANDONED_WAIT_0 indicates that the iocp handle was closed
	// during a call to GetQueuedCompletionStatus.
	// ERROR_INVALID_HANDLE indicates that the handle was closed prior to the call being made.
	return errors.Is(err, windows.ERROR_OPERATION_ABORTED) ||
			errors.Is(err, windows.ERROR_ABANDONED_WAIT_0) ||
			errors.Is(err, windows.ERROR_INVALID_HANDLE)
}

func (di *httpDriverInterface) flushPendingTransactions() error {
	err := windows.DeviceIoControl(di.driverHTTPHandle.Handle,
		driver.FlushPendingHttpTxnsIOCTL,
		nil, uint32(0), nil, uint32(0), nil, nil)
	return err
}

func (di *httpDriverInterface) close() error {
	err := di.closeDriverHandles()
	di.eventLoopWG.Wait()
	close(di.dataChannel)

	for _, buf := range di.readBuffers {
		C.free(unsafe.Pointer(buf))
	}
	di.readBuffers = nil
	return err
}

func (di *httpDriverInterface) closeDriverHandles() error {
	err := windows.CancelIoEx(di.driverHTTPHandle.Handle, nil)
	if err != nil && err != windows.ERROR_NOT_FOUND {
		return fmt.Errorf("error cancelling outstanding HTTP io requests: %w", err)
	}
	err = windows.CloseHandle(di.iocp)
	if err != nil {
		return fmt.Errorf("error closing HTTP io completion handle: %w", err)
	}
	err = di.driverHTTPHandle.Close()
	if err != nil {
		return fmt.Errorf("error closing driver HTTP file handle: %w", err)
	}
	return nil
}
