package feed

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync"
	"testing"
	"time"
)

var errDisconnect = errors.New("connection reset")

// fakeSource replays a scripted sequence per connection. Each script element is
// a seq to deliver, or -1 to simulate a disconnect.
type fakeSource struct {
	mu          sync.Mutex
	scripts     [][]int64
	conn        int
	fromSeqs    []uint64
	fetches     []uint64
	failFetches map[uint64]int // seq -> remaining failures
}

func cp(seq uint64) Checkpoint {
	return Checkpoint{Seq: seq, Digest: "d" + strconv.FormatUint(seq, 10)}
}

func (f *fakeSource) Stream(ctx context.Context, from uint64) (Subscription, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.fromSeqs = append(f.fromSeqs, from)
	if f.conn >= len(f.scripts) {
		return &fakeSub{}, nil // empty: Recv blocks until ctx done
	}
	s := f.scripts[f.conn]
	f.conn++
	return &fakeSub{items: s, blockAtEnd: f.conn == len(f.scripts)}, nil
}

func (f *fakeSource) Fetch(ctx context.Context, seq uint64) (Checkpoint, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.fetches = append(f.fetches, seq)
	if n := f.failFetches[seq]; n > 0 {
		f.failFetches[seq] = n - 1
		return Checkpoint{}, errors.New("transient fetch error")
	}
	return cp(seq), nil
}

type fakeSub struct {
	items      []int64
	i          int
	blockAtEnd bool
}

func (s *fakeSub) Recv(ctx context.Context) (Checkpoint, error) {
	if s.i < len(s.items) {
		v := s.items[s.i]
		s.i++
		if v < 0 {
			return Checkpoint{}, errDisconnect
		}
		return cp(uint64(v)), nil
	}
	if !s.blockAtEnd && len(s.items) > 0 {
		return Checkpoint{}, io.EOF
	}
	<-ctx.Done()
	return Checkpoint{}, ctx.Err()
}

func (s *fakeSub) Close() error { return nil }

func fastCfg() Config {
	return Config{InitialBackoff: time.Millisecond, MaxBackoff: 2 * time.Millisecond}
}

// runUntil runs the processor until `want` checkpoints are emitted.
func runUntil(t *testing.T, p *Processor, want int) []uint64 {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	var got []uint64
	err := p.Run(ctx, func(_ context.Context, c Checkpoint) error {
		got = append(got, c.Seq)
		if c.Digest != "d"+strconv.FormatUint(c.Seq, 10) {
			t.Errorf("wrong payload for %d", c.Seq)
		}
		if len(got) == want {
			cancel()
		}
		return nil
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("run ended with %v after %v", err, got)
	}
	return got
}

func assertSeq(t *testing.T, got []uint64, from uint64, n int) {
	t.Helper()
	if len(got) != n {
		t.Fatalf("got %d checkpoints %v, want %d", len(got), got, n)
	}
	for i, s := range got {
		if s != from+uint64(i) {
			t.Fatalf("out of order at %d: %v", i, got)
		}
	}
}

func TestInOrderPassThrough(t *testing.T) {
	src := &fakeSource{scripts: [][]int64{{100, 101, 102, 103}}}
	p := NewProcessor(src, 100, fastCfg())
	assertSeq(t, runUntil(t, p, 4), 100, 4)
	if len(src.fetches) != 0 {
		t.Fatal("no fetches expected")
	}
}

func TestGapBackfill(t *testing.T) {
	src := &fakeSource{scripts: [][]int64{{100, 101, 105, 106, 110}}}
	p := NewProcessor(src, 100, fastCfg())
	assertSeq(t, runUntil(t, p, 11), 100, 11)
	want := []uint64{102, 103, 104, 107, 108, 109}
	if len(src.fetches) != len(want) {
		t.Fatalf("fetches %v want %v", src.fetches, want)
	}
	for i := range want {
		if src.fetches[i] != want[i] {
			t.Fatalf("fetches %v want %v", src.fetches, want)
		}
	}
	if st := p.Stats(); st.GapsFilled != 6 || st.Emitted != 11 {
		t.Fatalf("stats %+v", st)
	}
}

func TestLeadingGap(t *testing.T) {
	// Stream starts ahead of the requested seq (e.g. node pruned the stream buffer).
	src := &fakeSource{scripts: [][]int64{{53, 54}}}
	p := NewProcessor(src, 50, fastCfg())
	assertSeq(t, runUntil(t, p, 5), 50, 5)
}

func TestDuplicatesAndReordering(t *testing.T) {
	src := &fakeSource{scripts: [][]int64{{10, 11, 11, 10, 12, 14, 13, 14, 15}}}
	p := NewProcessor(src, 10, fastCfg())
	assertSeq(t, runUntil(t, p, 6), 10, 6)
	// 13 is fetched during backfill; the late streamed 13 and the second 14 are duplicates.
	if st := p.Stats(); st.Duplicates != 4 || st.GapsFilled != 1 {
		t.Fatalf("stats %+v", st)
	}
}

func TestReconnectResumesFromLastProcessed(t *testing.T) {
	src := &fakeSource{scripts: [][]int64{
		{1, 2, 3, -1}, // disconnect after 3
		{2, 3, 4, 5},  // server replays old seqs on reconnect
		{-1},          // immediate failure
		{9, 10},       // reconnect lands ahead: gap 6..8 backfilled
	}}
	p := NewProcessor(src, 1, fastCfg())
	assertSeq(t, runUntil(t, p, 10), 1, 10)
	wantFrom := []uint64{1, 4, 6, 6}
	for i, w := range wantFrom {
		if src.fromSeqs[i] != w {
			t.Fatalf("reconnect from-seqs %v, want prefix %v", src.fromSeqs, wantFrom)
		}
	}
	if st := p.Stats(); st.Reconnects != 3 || st.Duplicates != 2 || st.GapsFilled != 3 {
		t.Fatalf("stats %+v", st)
	}
}

func TestFetchRetry(t *testing.T) {
	src := &fakeSource{
		scripts:     [][]int64{{1, 3}},
		failFetches: map[uint64]int{2: 2},
	}
	p := NewProcessor(src, 1, fastCfg())
	assertSeq(t, runUntil(t, p, 3), 1, 3)
}

func TestGapTooLarge(t *testing.T) {
	cfg := fastCfg()
	cfg.MaxGap = 5
	src := &fakeSource{scripts: [][]int64{{1, 100}}}
	p := NewProcessor(src, 1, cfg)
	err := p.Run(context.Background(), func(context.Context, Checkpoint) error { return nil })
	if !errors.Is(err, ErrGapTooLarge) {
		t.Fatalf("want ErrGapTooLarge, got %v", err)
	}
	if p.Next() != 2 {
		t.Fatalf("next = %d", p.Next())
	}
}

func TestHandlerErrorStops(t *testing.T) {
	boom := errors.New("boom")
	src := &fakeSource{scripts: [][]int64{{1, 2, 3}}}
	p := NewProcessor(src, 1, fastCfg())
	err := p.Run(context.Background(), func(_ context.Context, c Checkpoint) error {
		if c.Seq == 2 {
			return boom
		}
		return nil
	})
	if !errors.Is(err, boom) || p.Next() != 2 {
		t.Fatalf("err=%v next=%d", err, p.Next())
	}
}

// TestJSONRPCSource exercises the HTTP source against a local fake node.
func TestJSONRPCSource(t *testing.T) {
	var mu sync.Mutex
	tip := uint64(1000)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			ID     uint64 `json:"id"`
			Method string `json:"method"`
			Params []any  `json:"params"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		mu.Lock()
		defer mu.Unlock()
		var result any
		switch req.Method {
		case "sui_getLatestCheckpointSequenceNumber":
			tip += 3 // tip advances by 3 per poll: forces gaps
			result = strconv.FormatUint(tip, 10)
		case "sui_getCheckpoint":
			s := req.Params[0].(string)
			result = map[string]any{"sequenceNumber": s, "digest": "d" + s, "timestampMs": "1700000000000", "transactions": []string{"a", "b"}}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": req.ID, "result": result})
	}))
	defer srv.Close()

	src := NewJSONRPCSource(srv.URL)
	src.PollInterval = time.Millisecond
	c, err := src.Fetch(context.Background(), 42)
	if err != nil || c.Seq != 42 || c.TxCount != 2 || c.TimestampMs != 1700000000000 {
		t.Fatalf("fetch: %+v %v", c, err)
	}
	p := NewProcessor(src, 1003, fastCfg())
	got := runUntil(t, p, 10)
	assertSeq(t, got, 1003, 10)
	if p.Stats().GapsFilled == 0 {
		t.Fatal("expected backfilled gaps from tip-only polling")
	}
}
