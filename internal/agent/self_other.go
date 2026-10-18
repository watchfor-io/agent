//go:build !linux

package agent

import "github.com/watchfor-io/agent/internal/metric"

type selfUsage struct{}

func (a *Agent) footprint(*metric.Batch) {}
