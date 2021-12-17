// +build !windows

package encoding

import (
	"math"
	"sync"

	model "github.com/DataDog/agent-payload/v5/process"
	"github.com/DataDog/datadog-agent/pkg/network"
	"github.com/DataDog/datadog-agent/pkg/network/http"
	"github.com/DataDog/datadog-agent/pkg/process/util"
	"github.com/gogo/protobuf/proto"
)

// Build the key for the http map based on whether the local or remote side is http.
func httpKeyFromConn(c network.ConnectionStats) http.Key {
	// Retrieve translated addresses
	laddr, lport := network.GetNATLocalAddress(c)
	raddr, rport := network.GetNATRemoteAddress(c)

	// HTTP data is always indexed as (client, server), so we flip
	// the lookup key if necessary using the port range heuristic
	if network.IsEphemeralPort(int(lport)) {
		return http.NewKey(laddr, raddr, lport, rport, "", http.MethodUnknown)
	}

	return http.NewKey(raddr, laddr, rport, lport, "", http.MethodUnknown)
}
