// Command quote simulates an exact-input swap on a synthetic CLMM pool.
package main

import (
	"flag"
	"fmt"
	"log"
	"math/big"

	"github.com/pfoi7/sui-arb-demo/clmm"
)

// syntheticPool is a made-up pool around price 1.0 with three overlapping positions.
func syntheticPool() *clmm.Pool {
	net := map[int32]*big.Int{}
	add := func(lo, hi int32, l int64) {
		for t, s := range map[int32]int64{lo: l, hi: -l} {
			if net[t] == nil {
				net[t] = new(big.Int)
			}
			net[t].Add(net[t], big.NewInt(s))
		}
	}
	add(-600, 600, 5_000_000_000)
	add(-200, 200, 3_000_000_000)
	add(-50, 100, 2_000_000_000)
	return &clmm.Pool{
		SqrtPrice:    new(big.Int).Set(clmm.Q64),
		CurrentTick:  0,
		Liquidity:    big.NewInt(10_000_000_000),
		FeeRate:      2500, // 0.25%
		TickSpacing:  10,
		LiquidityNet: net,
	}
}

func price(sqrt *big.Int) string {
	f := new(big.Float).SetPrec(128).SetInt(sqrt)
	f.Quo(f, new(big.Float).SetInt(clmm.Q64))
	return f.Mul(f, f).Text('f', 8)
}

func main() {
	amt := flag.String("in", "50000000", "exact input amount (raw units)")
	a2b := flag.Bool("a2b", true, "swap token A for token B (price moves down)")
	flag.Parse()

	in, ok := new(big.Int).SetString(*amt, 10)
	if !ok || in.Sign() <= 0 {
		log.Fatalf("invalid -in %q", *amt)
	}
	p := syntheticPool()
	q, err := p.QuoteExactIn(in, *a2b, nil)
	if err != nil {
		log.Fatal(err)
	}
	fmt.Printf("direction      a2b=%v\n", *a2b)
	fmt.Printf("amount in      %s (fee %s)\n", q.AmountIn, q.FeeAmount)
	fmt.Printf("amount out     %s\n", q.AmountOut)
	fmt.Printf("price          %s -> %s\n", price(p.SqrtPrice), price(q.EndSqrtPrice))
	fmt.Printf("tick           %d -> %d (crossed %d)\n", p.CurrentTick, q.EndTick, q.TicksCrossed)
	fmt.Printf("liquidity      %s -> %s\n", p.Liquidity, q.EndLiquidity)
	if q.IsExceeded {
		fmt.Println("warning: input not fully consumed (liquidity exhausted)")
	}
}
