package ebpf

import (
	"fmt"
	"net"
	"strconv"

	"github.com/DataDog/datadog-agent/pkg/process/util"
)

func (t ConntrackTuple) Family() ConnFamily {
	if t.Metadata&uint32(IPv6) != 0 {
		return IPv6
	}
	return IPv4
}

func (t ConntrackTuple) Type() ConnType {
	if t.Metadata&uint32(TCP) != 0 {
		return TCP
	}
	return UDP
}

func (t ConntrackTuple) SourceAddress() util.Address {
	if t.Metadata&uint32(IPv6) != 0 {
		return util.V6Address(t.Saddr_l, t.Saddr_h)

	}
	return util.V4Address(uint32(t.Saddr_l))
}

func (t ConntrackTuple) SourceEndpoint() string {
	return net.JoinHostPort(t.SourceAddress().String(), strconv.Itoa(int(t.Sport)))
}

func (t ConntrackTuple) DestAddress() util.Address {
	if t.Metadata&uint32(IPv6) != 0 {
		return util.V6Address(t.Daddr_l, t.Daddr_h)

	}
	return util.V4Address(uint32(t.Daddr_l))
}

func (t ConntrackTuple) DestEndpoint() string {
	return net.JoinHostPort(t.DestAddress().String(), strconv.Itoa(int(t.Dport)))
}

func (t ConntrackTuple) String() string {
	return fmt.Sprintf(
		"[%s%s] [%s ⇄ %s] (ns: %d)",
		t.Type(),
		t.Family(),
		t.SourceEndpoint(),
		t.DestEndpoint(),
		t.Netns,
	)
}
