// Package metric is the wire model: what a batch of samples looks like on the
// way to the ingest endpoint.
package metric

import (
	"math"
	"time"
)

type Labels map[string]string

type Sample struct {
	T int64   `json:"t"`
	M string  `json:"m"`
	V float64 `json:"v"`
	L Labels  `json:"l,omitempty"`
}

type Host struct {
	ID     string            `json:"id"`
	Name   string            `json:"name"`
	Tags   map[string]string `json:"tags,omitempty"`
	OS     string            `json:"os,omitempty"`
	Kernel string            `json:"kernel,omitempty"`
	Arch   string            `json:"arch"`
}

// Facts change rarely and are sent on start and once an hour, not per tick.
type Facts struct {
	CPUModel       string `json:"cpu_model,omitempty"`
	CPUCores       int    `json:"cpu_cores"`
	MemTotal       uint64 `json:"mem_total"`
	Virtualization string `json:"virtualization,omitempty"`
	BootTime       int64  `json:"boot_time"`
	// Routable addresses per interface; loopback and link-local left out.
	Addresses map[string][]string `json:"addresses,omitempty"`
}

type Payload struct {
	Version int      `json:"v"`
	Agent   string   `json:"agent"`
	Host    Host     `json:"host"`
	Facts   *Facts   `json:"facts,omitempty"`
	Samples []Sample `json:"samples"`
}

const PayloadVersion = 1

// Batch collects one tick's samples. All samples share the tick timestamp so
// a batch reads as one point in time regardless of how long collection took.
type Batch struct {
	at      int64
	samples []Sample
}

func NewBatch(at time.Time) *Batch {
	return &Batch{at: at.Unix(), samples: make([]Sample, 0, 128)}
}

// Add records a sample. NaN and ±Inf are dropped: JSON has no encoding for
// them and a division by a zero delta is the usual source, not a real value.
func (b *Batch) Add(name string, v float64, labels ...Labels) {
	if math.IsNaN(v) || math.IsInf(v, 0) {
		return
	}
	s := Sample{T: b.at, M: name, V: v}
	if len(labels) > 0 && len(labels[0]) > 0 {
		s.L = labels[0]
	}
	b.samples = append(b.samples, s)
}

func (b *Batch) At() int64         { return b.at }
func (b *Batch) Len() int          { return len(b.samples) }
func (b *Batch) Samples() []Sample { return b.samples }
