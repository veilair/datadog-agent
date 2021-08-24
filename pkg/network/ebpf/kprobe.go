//+build linux

package ebpf

import (
	"fmt"
	"net"
	"strconv"
	"unsafe"

	"github.com/DataDog/datadog-agent/pkg/process/util"
)

func (t NamespacedConnTuple) Family() ConnFamily {
	if t.Metadata&uint32(IPv6) != 0 {
		return IPv6
	}
	return IPv4
}

func (t NamespacedConnTuple) Type() ConnType {
	if t.Metadata&uint32(TCP) != 0 {
		return TCP
	}
	return UDP
}

func (t NamespacedConnTuple) SourceAddress() util.Address {
	if t.Family() == IPv4 {
		return util.V4Address(uint32(t.Saddr_l))
	}
	return util.V6Address(t.Saddr_l, t.Saddr_h)
}

func (t NamespacedConnTuple) SourceEndpoint() string {
	return net.JoinHostPort(t.SourceAddress().String(), strconv.Itoa(int(t.Sport)))
}

func (t NamespacedConnTuple) DestAddress() util.Address {
	if t.Family() == IPv4 {
		return util.V4Address(uint32(t.Daddr_l))
	}
	return util.V6Address(t.Daddr_l, t.Daddr_h)
}

func (t NamespacedConnTuple) DestEndpoint() string {
	return net.JoinHostPort(t.DestAddress().String(), strconv.Itoa(int(t.Dport)))
}

func (t NamespacedConnTuple) String() string {
	return fmt.Sprintf(
		"[%s%s] [PID: %d] [%s ⇄ %s] (ns: %d)",
		t.Type(),
		t.Family(),
		t.Pid,
		t.SourceEndpoint(),
		t.DestEndpoint(),
		t.Netns,
	)
}

func (cs ConnStats) ConnectionDirection() ConnDirection {
	return ConnDirection(cs.Direction)
}

func (cs ConnStats) IsAssured() bool {
	return cs.Flags&uint32(Assured) != 0
}

func ToBatch(data []byte) *Batch {
	return (*Batch)(unsafe.Pointer(&data[0]))
}
