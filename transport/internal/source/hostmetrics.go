package source

import "os"

// HostMetricsAgent is the Adapter.Agent() value for a host's metric sample
// log. Like ExecutionsAgent it names no agent; the receiver lands the log at
// received/loom-host-metrics/<host>/samples.jsonl.
const HostMetricsAgent = "loom-host-metrics"

// hostMetricsSessionID is the one session the sample log ships as, whatever
// rotated file a delta was read from: staging stays one append-only file.
const hostMetricsSessionID = "samples"

// hostMetricsAdapter lists the sample log at a configured path. The log is
// rotated under the reader, so capture follows it by inode rather than by a
// plain byte cursor (transport/shipper/hostmetrics.go, docs/host-metrics.md).
type hostMetricsAdapter struct{ path string }

// HostMetrics returns the adapter for the sample log at path, the shipper
// config's host_metrics_log.
func HostMetrics(path string) Adapter { return hostMetricsAdapter{path: path} }

func (hostMetricsAdapter) Agent() string { return HostMetricsAgent }

// List returns the log as a single Session when the live file exists. Cwd is
// empty for the same reason as the execution registry's: no project.
func (a hostMetricsAdapter) List() ([]Session, error) {
	if _, err := os.Stat(a.path); err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	host, _ := os.Hostname()
	return []Session{{
		Project:   encodeProjectPath(host),
		SessionID: hostMetricsSessionID,
		Path:      a.path,
	}}, nil
}
