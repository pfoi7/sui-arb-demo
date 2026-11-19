// Command demo tails Sui mainnet checkpoints through the gap-filling processor.
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"time"

	"github.com/example/sui-arb-demo/feed"
)

// dropSource wraps a Source and drops every Nth streamed checkpoint to show
// the processor repairing gaps.
type dropSource struct {
	feed.Source
	every int
}

type dropSub struct {
	feed.Subscription
	every, n int
}

func (d dropSource) Stream(ctx context.Context, from uint64) (feed.Subscription, error) {
	s, err := d.Source.Stream(ctx, from)
	if err != nil {
		return nil, err
	}
	return &dropSub{Subscription: s, every: d.every}, nil
}

func (d *dropSub) Recv(ctx context.Context) (feed.Checkpoint, error) {
	for {
		cp, err := d.Subscription.Recv(ctx)
		if err != nil {
			return cp, err
		}
		d.n++
		if d.every > 0 && d.n%d.every == 0 {
			log.Printf("[inject] dropping streamed checkpoint %d", cp.Seq)
			continue
		}
		return cp, nil
	}
}

func main() {
	url := flag.String("url", os.Getenv("SUI_URL"), "endpoint URL (env SUI_URL); default depends on -api")
	api := flag.String("api", "graphql", "graphql (public endpoints) or jsonrpc (self-hosted node)")
	back := flag.Uint64("back", 5, "start this many checkpoints behind the tip")
	count := flag.Int("n", 20, "stop after N checkpoints (0 = run forever)")
	drop := flag.Int("drop-every", 0, "drop every Nth streamed checkpoint to demonstrate backfill")
	poll := flag.Duration("poll", 500*time.Millisecond, "tip polling interval")
	flag.Parse()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	var rpc interface {
		feed.Source
		LatestSeq(context.Context) (uint64, error)
	}
	switch *api {
	case "graphql":
		s := feed.NewGraphQLSource(*url)
		s.PollInterval = *poll
		rpc = s
	case "jsonrpc":
		s := feed.NewJSONRPCSource(*url)
		s.PollInterval = *poll
		rpc = s
	default:
		log.Fatalf("unknown -api %q", *api)
	}
	tip, err := rpc.LatestSeq(ctx)
	if err != nil {
		log.Fatalf("latest checkpoint: %v", err)
	}
	from := tip - min(*back, tip)
	log.Printf("api=%s tip=%d, starting at %d", *api, tip, from)

	var src feed.Source = rpc
	if *drop > 0 {
		src = dropSource{Source: rpc, every: *drop}
	}
	p := feed.NewProcessor(src, from, feed.Config{OnEvent: func(m string) { log.Print(m) }})

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	seen := 0
	err = p.Run(ctx, func(_ context.Context, cp feed.Checkpoint) error {
		age := time.Since(time.UnixMilli(int64(cp.TimestampMs))).Round(time.Millisecond)
		fmt.Printf("seq=%d age=%s digest=%s\n", cp.Seq, age, cp.Digest)
		seen++
		if *count > 0 && seen >= *count {
			cancel()
		}
		return nil
	})
	st := p.Stats()
	log.Printf("done: emitted=%d backfilled=%d duplicates=%d reconnects=%d (%v)", st.Emitted, st.GapsFilled, st.Duplicates, st.Reconnects, err)
}
