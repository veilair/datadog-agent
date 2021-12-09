// +build linux_bpf

package http

type noOpMonitor struct{}

// NewNoOpMonitor creates a monitor which always returns empty information
func NewNoOpMonitor() Monitor {
	return &noOpMonitor{}
}

func (*noOpMonitor) Start() error {
	return nil
}

func (*noOpMonitor) GetHTTPStats() map[Key]RequestStats {
	return nil
}

func (*noOpMonitor) DumpMaps(maps ...string) (string, error) {
	return nil, nil
}

func (*noOpMonitor) Stop() {}
