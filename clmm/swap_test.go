package clmm

import (
	"math/big"
	"testing"
)

func bi(s string) *big.Int { return mustBig(s) }

func sqrtAt(t *testing.T, tk int32) *big.Int {
	t.Helper()
	p, err := SqrtPriceAtTick(tk)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func TestDeltasRounding(t *testing.T) {
	lo, hi := sqrtAt(t, -1000), sqrtAt(t, 1000)
	L := bi("1000000000000")
	for _, f := range []func(a, b, l *big.Int, up bool) *big.Int{DeltaA, DeltaB} {
		down, up := f(lo, hi, L, false), f(lo, hi, L, true)
		d := new(big.Int).Sub(up, down)
		if d.Sign() < 0 || d.Cmp(big.NewInt(1)) > 0 {
			t.Fatalf("round up/down differ by %s", d)
		}
		if f(hi, lo, L, true).Cmp(up) != 0 {
			t.Fatal("delta must be order independent")
		}
	}
}

func TestSwapStepPartialAndFull(t *testing.T) {
	cur, target := Q64, sqrtAt(t, -100)
	L := bi("10000000000")
	fee := uint64(2500)

	// Huge input reaches the target.
	full, err := ComputeSwapStep(cur, target, L, bi("1000000000000"), fee, true)
	if err != nil {
		t.Fatal(err)
	}
	if full.NextSqrtPrice.Cmp(target) != 0 {
		t.Fatal("expected to reach target")
	}
	if full.AmountIn.Cmp(DeltaA(target, cur, L, true)) != 0 {
		t.Fatal("amount in should equal max input for range")
	}

	// Small input stops inside the range and consumes everything.
	in := bi("1000000")
	part, err := ComputeSwapStep(cur, target, L, in, fee, true)
	if err != nil {
		t.Fatal(err)
	}
	if part.NextSqrtPrice.Cmp(target) <= 0 || part.NextSqrtPrice.Cmp(cur) >= 0 {
		t.Fatal("partial step price must be strictly between current and target")
	}
	if s := new(big.Int).Add(part.AmountIn, part.FeeAmount); s.Cmp(in) != 0 {
		t.Fatalf("partial step must consume all input, got %s", s)
	}
	// Fee is ~0.25%.
	if part.FeeAmount.Cmp(bi("2500")) != 0 {
		t.Fatalf("unexpected fee %s", part.FeeAmount)
	}
	// Near price 1 the output is slightly below input-less-fee.
	if part.AmountOut.Cmp(part.AmountIn) >= 0 || part.AmountOut.Sign() == 0 {
		t.Fatalf("unexpected out %s for in %s", part.AmountOut, part.AmountIn)
	}
}

// Synthetic pool: tick spacing 10, three overlapping positions.
func testPool(t *testing.T) *Pool {
	L1, L2, L3 := bi("5000000000"), bi("3000000000"), bi("2000000000")
	net := map[int32]*big.Int{}
	add := func(lo, hi int32, l *big.Int) {
		for _, x := range []struct {
			t int32
			s int
		}{{lo, 1}, {hi, -1}} {
			if net[x.t] == nil {
				net[x.t] = new(big.Int)
			}
			if x.s > 0 {
				net[x.t].Add(net[x.t], l)
			} else {
				net[x.t].Sub(net[x.t], l)
			}
		}
	}
	add(-600, 600, L1)
	add(-200, 200, L2)
	add(-50, 100, L3)
	return &Pool{
		SqrtPrice:    Q64,
		CurrentTick:  0,
		Liquidity:    new(big.Int).Add(new(big.Int).Add(L1, L2), L3),
		FeeRate:      2500,
		TickSpacing:  10,
		LiquidityNet: net,
	}
}

func TestQuoteMultiTickA2B(t *testing.T) {
	p := testPool(t)
	q, err := p.QuoteExactIn(bi("120000000"), true, nil)
	if err != nil {
		t.Fatal(err)
	}
	// Should cross -50 and -200 and stop inside [-600,-200).
	if q.TicksCrossed != 2 {
		t.Fatalf("crossed %d ticks, want 2 (end tick %d)", q.TicksCrossed, q.EndTick)
	}
	if q.EndTick >= -200 || q.EndTick < -600 {
		t.Fatalf("end tick %d outside expected range", q.EndTick)
	}
	if q.EndLiquidity.Cmp(bi("5000000000")) != 0 {
		t.Fatalf("end liquidity %s", q.EndLiquidity)
	}
	if q.IsExceeded || q.AmountIn.Cmp(bi("120000000")) != 0 {
		t.Fatal("input should be fully consumed")
	}
	// Pool snapshot must not be mutated.
	if p.SqrtPrice.Cmp(Q64) != 0 || p.Liquidity.Cmp(bi("10000000000")) != 0 {
		t.Fatal("quote mutated pool")
	}
	checkConservation(t, q)
}

func TestQuoteMultiTickB2A(t *testing.T) {
	p := testPool(t)
	q, err := p.QuoteExactIn(bi("120000000"), false, nil)
	if err != nil {
		t.Fatal(err)
	}
	if q.TicksCrossed != 2 || q.EndTick < 200 || q.EndTick >= 600 {
		t.Fatalf("crossed=%d end tick=%d", q.TicksCrossed, q.EndTick)
	}
	checkConservation(t, q)
}

func TestQuoteRangeExhausted(t *testing.T) {
	p := testPool(t)
	q, err := p.QuoteExactIn(bi("100000000000000"), true, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !q.IsExceeded || q.EndLiquidity.Sign() != 0 || q.TicksCrossed != 3 {
		t.Fatalf("expected exhausted liquidity: %+v", q)
	}
}

func TestQuotePriceLimit(t *testing.T) {
	p := testPool(t)
	limit := sqrtAt(t, -100)
	q, err := p.QuoteExactIn(bi("100000000000000"), true, limit)
	if err != nil {
		t.Fatal(err)
	}
	if q.EndSqrtPrice.Cmp(limit) != 0 || !q.IsExceeded || q.TicksCrossed != 1 {
		t.Fatalf("limit not respected: %+v", q)
	}
	if _, err := p.QuoteExactIn(bi("1"), true, sqrtAt(t, 10)); err != ErrInvalidLimit {
		t.Fatal("expected invalid limit error")
	}
}

func TestQuoteMonotonicInSize(t *testing.T) {
	p := testPool(t)
	prev := new(big.Int)
	for _, s := range []string{"1000", "1000000", "50000000", "100000000", "150000000"} {
		q, err := p.QuoteExactIn(bi(s), true, nil)
		if err != nil {
			t.Fatal(err)
		}
		if q.AmountOut.Cmp(prev) <= 0 {
			t.Fatalf("output not increasing at size %s", s)
		}
		prev = q.AmountOut
	}
}

func checkConservation(t *testing.T, q Quote) {
	t.Helper()
	if q.AmountOut.Sign() <= 0 || q.FeeAmount.Sign() <= 0 {
		t.Fatalf("bad quote %+v", q)
	}
	// Average execution price must be worse than fee-free price 1.
	if q.AmountOut.Cmp(new(big.Int).Sub(q.AmountIn, q.FeeAmount)) >= 0 {
		t.Fatal("output must be less than net input around price 1")
	}
}
