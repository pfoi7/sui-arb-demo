// Package feed consumes an ordered stream of Sui checkpoints and guarantees
// gap-free, duplicate-free, in-order delivery to a handler, even when the
// underlying transport drops messages, repeats them, or disconnects.
package feed

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// Checkpoint is the minimal checkpoint view this package needs.
type Checkpoint struct {
	Seq         uint64
	Digest      string
	TimestampMs uint64
	TxCount     int
}

// Subscription is a live stream of checkpoints. Recv blocks until the next
// checkpoint arrives, the stream fails, or ctx is done.
type Subscription interface {
	Recv(ctx context.Context) (Checkpoint, error)
	Close() error
}

// Source is a checkpoint provider: a best-effort live stream plus point reads.
// Stream may skip, repeat, or reorder checkpoints; Fetch must be authoritative.
type Source interface {
	Stream(ctx context.Context, fromSeq uint64) (Subscription, error)
	Fetch(ctx context.Context, seq uint64) (Checkpoint, error)
}

// Handler receives checkpoints strictly in sequence order, each exactly once.
// Returning an error stops the processor.
type Handler func(ctx context.Context, cp Checkpoint) error

// Stats counts what the processor had to repair.
type Stats struct {
	Emitted    uint64
	Duplicates uint64 // checkpoints at or below the last emitted seq, dropped
	GapsFilled uint64 // checkpoints recovered via Fetch
	Reconnects uint64
}

// Config controls retry behaviour. Zero values get sensible defaults.
type Config struct {
	MaxBackoff     time.Duration // cap for reconnect backoff (default 5s)
	InitialBackoff time.Duration // first reconnect delay (default 100ms)
	FetchRetries   int           // attempts per missing checkpoint (default 5)
	// MaxGap bounds a single backfill to protect against bogus sequence numbers.
	// If exceeded the processor returns ErrGapTooLarge (default 10_000).
	MaxGap uint64
	// OnEvent, if set, receives human-readable diagnostics (gaps, reconnects).
	OnEvent func(msg string)
}

// ErrGapTooLarge is returned when a stream jumps further than Config.MaxGap.
var ErrGapTooLarge = errors.New("feed: gap too large")

// Processor turns an unreliable Source into an ordered, gap-free feed.
type Processor struct {
	src   Source
	cfg   Config
	next  uint64
	stats Stats
}

// NewProcessor creates a processor that will start delivering at fromSeq.
func NewProcessor(src Source, fromSeq uint64, cfg Config) *Processor {
	if cfg.MaxBackoff == 0 {
		cfg.MaxBackoff = 5 * time.Second
	}
	if cfg.InitialBackoff == 0 {
		cfg.InitialBackoff = 100 * time.Millisecond
	}
	if cfg.FetchRetries == 0 {
		cfg.FetchRetries = 5
	}
	if cfg.MaxGap == 0 {
		cfg.MaxGap = 10_000
	}
	return &Processor{src: src, cfg: cfg, next: fromSeq}
}

// Next is the sequence number the processor expects to emit next.
// After a crash it is the correct resume point.
func (p *Processor) Next() uint64 { return p.next }

// Stats returns a snapshot of repair counters.
func (p *Processor) Stats() Stats { return p.stats }

func (p *Processor) event(format string, args ...any) {
	if p.cfg.OnEvent != nil {
		p.cfg.OnEvent(fmt.Sprintf(format, args...))
	}
}

// Run consumes the source until ctx is cancelled or the handler fails.
// Transport errors trigger a reconnect from the last unprocessed seq.
func (p *Processor) Run(ctx context.Context, h Handler) error {
	backoff := p.cfg.InitialBackoff
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		sub, err := p.src.Stream(ctx, p.next)
		if err == nil {
			progressed := p.next
			err = p.consume(ctx, sub, h)
			_ = sub.Close()
			var fatal *fatalError
			if errors.As(err, &fatal) {
				return fatal.err
			}
			if p.next > progressed {
				backoff = p.cfg.InitialBackoff // made progress: reset backoff
			}
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		p.stats.Reconnects++
		p.event("stream error: %v; reconnecting from seq %d in %s", err, p.next, backoff)
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(backoff):
		}
		backoff *= 2
		if backoff > p.cfg.MaxBackoff {
			backoff = p.cfg.MaxBackoff
		}
	}
}

type fatalError struct{ err error }

func (f *fatalError) Error() string { return f.err.Error() }
func (f *fatalError) Unwrap() error { return f.err }

func (p *Processor) consume(ctx context.Context, sub Subscription, h Handler) error {
	for {
		cp, err := sub.Recv(ctx)
		if err != nil {
			return err
		}
		if cp.Seq < p.next {
			p.stats.Duplicates++
			continue
		}
		if gap := cp.Seq - p.next; gap > 0 {
			if gap > p.cfg.MaxGap {
				return &fatalError{fmt.Errorf("%w: expected %d, got %d", ErrGapTooLarge, p.next, cp.Seq)}
			}
			p.event("gap detected: expected %d, got %d; backfilling %d checkpoint(s)", p.next, cp.Seq, gap)
			for p.next < cp.Seq {
				missing, err := p.fetch(ctx, p.next)
				if err != nil {
					return err // transport problem: reconnect and retry from p.next
				}
				p.stats.GapsFilled++
				if err := p.emit(ctx, h, missing); err != nil {
					return err
				}
			}
		}
		if err := p.emit(ctx, h, cp); err != nil {
			return err
		}
	}
}

func (p *Processor) emit(ctx context.Context, h Handler, cp Checkpoint) error {
	if cp.Seq != p.next {
		return &fatalError{fmt.Errorf("feed: source returned seq %d for %d", cp.Seq, p.next)}
	}
	if err := h(ctx, cp); err != nil {
		return &fatalError{err}
	}
	p.next++
	p.stats.Emitted++
	return nil
}

func (p *Processor) fetch(ctx context.Context, seq uint64) (Checkpoint, error) {
	var lastErr error
	delay := p.cfg.InitialBackoff
	for i := 0; i < p.cfg.FetchRetries; i++ {
		cp, err := p.src.Fetch(ctx, seq)
		if err == nil {
			return cp, nil
		}
		lastErr = err
		select {
		case <-ctx.Done():
			return Checkpoint{}, ctx.Err()
		case <-time.After(delay):
		}
		delay *= 2
	}
	return Checkpoint{}, fmt.Errorf("feed: fetch %d failed after %d attempts: %w", seq, p.cfg.FetchRetries, lastErr)
}
