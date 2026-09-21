// Package metric is the wire model: what a batch of samples looks like on the
// way to the ingest endpoint.
package metric

import (
	"math"
	"time"

	"github.com/watchfor-io/agent/internal/text"
)

// Labels qualify a sample: which mount, interface, process or watch it
// is about.
type Labels map[string]string

// Sample is one measurement on the wire: when (T), which metric (M), the
// value (V) and its labels (L). The keys are one letter because a batch
// carries hundreds of them.
type Sample struct {
	T int64   `json:"t"`
	M string  `json:"m"`
	V float64 `json:"v"`
	L Labels  `json:"l,omitempty"`
}

// Host identifies the machine on every batch: the stable ID the server
// keys hosts on, the name and tags a person sees, and what runs there.
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
	// The machine as the firmware describes it: cloud instance type on EC2
	// ("Amazon EC2 t4g.small"), server model on metal ("Dell Inc. PowerEdge R640").
	Hardware string `json:"hardware,omitempty"`
	BootTime int64  `json:"boot_time"`
	// Routable addresses per interface; loopback and link-local left out.
	Addresses map[string][]string `json:"addresses,omitempty"`
	// The address the host uses to reach the server (its default route's
	// source address) — the one to call "the host's IP". IPv4 and IPv6
	// separately when both exist; the interface that carries them.
	PrimaryAddress  string `json:"primary_address,omitempty"`
	PrimaryAddress6 string `json:"primary_address6,omitempty"`
	PrimaryIface    string `json:"primary_iface,omitempty"`
}

// Payload is the body of one push: the wire version, the agent's release,
// the host, the facts when they are due, and the tick's samples.
type Payload struct {
	Version int      `json:"v"`
	Agent   string   `json:"agent"`
	Host    Host     `json:"host"`
	Facts   *Facts   `json:"facts,omitempty"`
	Samples []Sample `json:"samples"`
}

// PayloadVersion is the wire format number sent as "v". Ingest accepts
// only the version it knows, so a change in shape means a new number.
const PayloadVersion = 1

// Batch collects one tick's samples. All samples share the tick timestamp so
// a batch reads as one point in time regardless of how long collection took.
type Batch struct {
	at      int64
	samples []Sample
}

// NewBatch starts a batch stamped at `at`; every sample added carries
// that one time.
func NewBatch(at time.Time) *Batch {
	return &Batch{at: at.Unix(), samples: make([]Sample, 0, 128)}
}

// maxLabelBytes is what ingest keeps of a label value; longer is dropped
// there, so it is cut here instead, where it can still be seen.
const maxLabelBytes = 256

// Add records a sample. NaN and ±Inf are dropped: JSON has no encoding for
// them and a division by a zero delta is the usual source, not a real value.
func (b *Batch) Add(name string, v float64, labels ...Labels) {
	if math.IsNaN(v) || math.IsInf(v, 0) {
		return
	}
	s := Sample{T: b.at, M: name, V: v}
	if len(labels) > 0 && len(labels[0]) > 0 {
		for k, v := range labels[0] { // a label names a mount, a device, a process: not ours to trust
			if c := text.Clip(text.Clean(v), maxLabelBytes); c != v {
				labels[0][k] = c
			}
		}
		s.L = labels[0]
	}
	b.samples = append(b.samples, s)
}

// Len is how many samples the batch holds.
func (b *Batch) Len() int { return len(b.samples) }

// Samples is the batch's contents in the order they were added.
func (b *Batch) Samples() []Sample { return b.samples }
