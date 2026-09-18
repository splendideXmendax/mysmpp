package dispatch

import (
	"errors"
	"fmt"

	"github.com/splendideXmendax/mysmpp/internal/config"
	"github.com/splendideXmendax/mysmpp/internal/message"
	"github.com/splendideXmendax/mysmpp/internal/smpp"
)

var ErrInvalidMessageLength = errors.New("invalid message length")

func (d *Dispatcher) ReloadLimits(cfg config.DispatcherConfig) {
	n := cfg.MaxMessageSegments
	if n <= 0 {
		n = 20
	}
	d.maxSegments.Store(int64(n))
}

// validateMessageLength separates a concatenation's declared total from this
// submission's quota debit. An already split PDU consumes exactly one segment.
func (d *Dispatcher) validateMessageLength(env Envelope) ([]message.Segment, error) {
	limit := int(d.maxSegments.Load())
	if limit <= 0 {
		limit = 20
	}
	fail := func() ([]message.Segment, error) {
		return nil, fmt.Errorf("%w: maximum %d SMS segments", ErrInvalidMessageLength, limit)
	}
	info, concat := smpp.ParseConcat(env.UDH)
	if env.SARSet {
		if concat || len(env.SARTotalSegments) != 1 || len(env.SARSegmentSeqnum) != 1 ||
			(len(env.SARRefNum) != 1 && len(env.SARRefNum) != 2) {
			return fail()
		}
		info.Total, info.Part = env.SARTotalSegments[0], env.SARSegmentSeqnum[0]
		concat = true
	}
	if concat && (info.Total == 0 || info.Part == 0 || info.Part > info.Total || int(info.Total) > limit) {
		return fail()
	}
	encoding := env.Encoding
	if env.DataCoding == 8 {
		encoding = "ucs2"
	}
	if env.DataCoding == 3 {
		encoding = "8bit"
	}
	if encoding == "" {
		encoding = message.DetectEncoding(env.Text)
	}
	if env.RawPayloadSet {
		// DCS 0 can be packed or unpacked GSM7. The octet bound admits both
		// representations without decoding and re-encoding customer data.
		single, part := 140, 134
		if env.DataCoding == 0 {
			single, part = 160, 153
		}
		n := len(env.RawPayload)
		if env.DataCoding == 8 && n%2 != 0 {
			return fail()
		}
		if concat || len(env.UDH) > 0 {
			capacity := single - len(env.UDH)
			if env.DataCoding == 0 {
				capacity = single - (len(env.UDH)*8+6)/7
			}
			if env.SARSet {
				capacity = part
			}
			if capacity < 0 || n > capacity {
				return fail()
			}
			return []message.Segment{{Part: 1, Total: 1, Text: env.Text}}, nil
		}
		count := 1
		if n > single {
			count = (n + part - 1) / part
		}
		if count > limit || n > 65535 {
			return fail()
		}
		segments := make([]message.Segment, count)
		for i := range segments {
			segments[i] = message.Segment{Part: i + 1, Total: count}
		}
		return segments, nil
	}
	segments := message.Split(env.Text, message.SplitOptions{ForceEncoding: encoding})
	if len(segments) > limit {
		return fail()
	}
	return segments, nil
}
