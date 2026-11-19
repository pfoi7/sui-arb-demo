package feed

import (
	"context"
	"time"
)

// tipReader is a source that can report its latest checkpoint.
type tipReader interface {
	LatestSeq(ctx context.Context) (uint64, error)
	Fetch(ctx context.Context, seq uint64) (Checkpoint, error)
}

// pollSub turns a request/response API into a Subscription by polling the tip.
// It only ever emits the newest checkpoint, so intermediate ones show up as
// gaps that the Processor fills through Fetch.
type pollSub struct {
	src      tipReader
	interval time.Duration
	last     uint64
	first    bool
}

func newPollSub(src tipReader, interval time.Duration, from uint64) *pollSub {
	if interval <= 0 {
		interval = time.Second
	}
	return &pollSub{src: src, interval: interval, last: from, first: true}
}

func (p *pollSub) Recv(ctx context.Context) (Checkpoint, error) {
	for {
		tip, err := p.src.LatestSeq(ctx)
		if err != nil {
			return Checkpoint{}, err
		}
		if (p.first && tip >= p.last) || tip > p.last {
			p.first = false
			p.last = tip
			return p.src.Fetch(ctx, tip)
		}
		select {
		case <-ctx.Done():
			return Checkpoint{}, ctx.Err()
		case <-time.After(p.interval):
		}
	}
}

func (p *pollSub) Close() error { return nil }
