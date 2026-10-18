package agent

import (
	"time"

	"github.com/watchfor-io/agent/internal/metric"
	"github.com/watchfor-io/agent/internal/push"
)

// What one request may carry. Ingest refuses a batch of more than 10 000
// samples or 512 KiB compressed (413); half of each leaves room, and a
// host with a thousand interfaces simply sends two requests.
const (
	maxBatchSamples = 5000
	maxBatchBytes   = 256 << 10
	// replayScan is how many spooled files one merged replay request
	// looks at, at most.
	replayScan = 256
)

// encodeChunks is the payload as one or more request bodies within the
// limits. Facts travel with the first only.
func encodeChunks(p *metric.Payload, maxSamples, maxBytes int) ([][]byte, error) {
	if n := len(p.Samples); n > maxSamples {
		parts := (n + maxSamples - 1) / maxSamples
		size := (n + parts - 1) / parts
		var out [][]byte
		for i := 0; i < n; i += size {
			part := *p
			part.Samples = p.Samples[i:min(i+size, n)]
			if i > 0 {
				part.Facts = nil
			}
			b, err := encodeChunks(&part, maxSamples, maxBytes)
			if err != nil {
				return nil, err
			}
			out = append(out, b...)
		}
		return out, nil
	}
	b, err := push.Encode(p)
	if err != nil {
		return nil, err
	}
	if len(b) <= maxBytes || len(p.Samples) <= 1 {
		return [][]byte{b}, nil
	}
	return encodeChunks(p, (len(p.Samples)+1)/2, maxBytes)
}

// splitBody halves an encoded batch (facts stay with the first half);
// false when it cannot be split further.
func splitBody(body []byte) ([][]byte, bool) {
	p, err := push.Decode(body)
	if err != nil || len(p.Samples) < 2 {
		return nil, false
	}
	mid := len(p.Samples) / 2
	a, b := *p, *p
	a.Samples, b.Samples, b.Facts = p.Samples[:mid], p.Samples[mid:], nil
	ea, err := push.Encode(&a)
	if err != nil {
		return nil, false
	}
	eb, err := push.Encode(&b)
	if err != nil {
		return nil, false
	}
	return [][]byte{ea, eb}, true
}

// replayBudget is how many replay requests one tick may make. Ingest
// allows a host 3×(batches per minute at its interval)+2 requests a minute
// (and never more than 60); the regular push takes one per tick, replays
// share the rest. The agent's interval is never shorter than the plan's,
// so this stays inside what the server allows.
func (a *Agent) replayBudget() int {
	sec := max(int(a.interval/time.Second), 1)
	perMin := min(60, 3*((60+sec-1)/sec)+2)
	perTick := min(perMin*sec/60, perMin) - 1
	return min(max(perTick, 1), 8)
}
