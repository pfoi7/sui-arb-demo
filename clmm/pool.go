package clmm

import (
	"errors"
	"math/big"
	"sort"
)

// Pool is an in-memory snapshot of a CLMM pool sufficient for quoting.
type Pool struct {
	SqrtPrice   *big.Int
	CurrentTick int32
	Liquidity   *big.Int
	FeeRate     uint64 // in FeeRateDenominator units
	TickSpacing int32
	// LiquidityNet maps initialized tick index -> signed net liquidity added
	// when the tick is crossed left-to-right (price increasing).
	LiquidityNet map[int32]*big.Int
}

// Quote is the result of an exact-input swap simulation.
type Quote struct {
	AmountIn     *big.Int // total consumed including fees
	AmountOut    *big.Int
	FeeAmount    *big.Int
	EndSqrtPrice *big.Int
	EndTick      int32
	EndLiquidity *big.Int
	TicksCrossed int
	IsExceeded   bool // input not fully consumed (price limit / range exhausted)
}

// ErrInvalidLimit is returned when the price limit is on the wrong side of the current price.
var ErrInvalidLimit = errors.New("clmm: invalid sqrt price limit")

func (p *Pool) sortedTicks() []int32 {
	ts := make([]int32, 0, len(p.LiquidityNet))
	for t := range p.LiquidityNet {
		ts = append(ts, t)
	}
	sort.Slice(ts, func(i, j int) bool { return ts[i] < ts[j] })
	return ts
}

// QuoteExactIn simulates swapping amountIn through the pool without mutating it.
// a2b sells token A for token B (price decreases). A nil limit means no limit.
func (p *Pool) QuoteExactIn(amountIn *big.Int, a2b bool, limit *big.Int) (Quote, error) {
	if limit == nil {
		if a2b {
			limit = MinSqrtPrice
		} else {
			limit = MaxSqrtPrice
		}
	}
	if a2b && limit.Cmp(p.SqrtPrice) >= 0 || !a2b && limit.Cmp(p.SqrtPrice) <= 0 {
		return Quote{}, ErrInvalidLimit
	}
	ticks := p.sortedTicks()
	price := new(big.Int).Set(p.SqrtPrice)
	tick := p.CurrentTick
	liq := new(big.Int).Set(p.Liquidity)
	remaining := new(big.Int).Set(amountIn)
	q := Quote{AmountIn: new(big.Int), AmountOut: new(big.Int), FeeAmount: new(big.Int)}

	for remaining.Sign() > 0 && price.Cmp(limit) != 0 {
		next, ok := nextInitialized(ticks, tick, a2b)
		var target *big.Int
		if ok {
			target, _ = SqrtPriceAtTick(next)
		} else {
			target = limit
		}
		if a2b && target.Cmp(limit) < 0 || !a2b && target.Cmp(limit) > 0 {
			target = limit
		}
		step, err := ComputeSwapStep(price, target, liq, remaining, p.FeeRate, a2b)
		if err != nil {
			return Quote{}, err
		}
		used := new(big.Int).Add(step.AmountIn, step.FeeAmount)
		remaining.Sub(remaining, used)
		q.AmountIn.Add(q.AmountIn, used)
		q.AmountOut.Add(q.AmountOut, step.AmountOut)
		q.FeeAmount.Add(q.FeeAmount, step.FeeAmount)
		price = step.NextSqrtPrice

		if ok && targetIsTick(price, next) {
			net := p.LiquidityNet[next]
			if a2b {
				liq.Sub(liq, net)
				tick = next - 1
			} else {
				liq.Add(liq, net)
				tick = next
			}
			if liq.Sign() < 0 {
				return Quote{}, errors.New("clmm: negative liquidity after crossing")
			}
			q.TicksCrossed++
		} else {
			tick, _ = TickAtSqrtPrice(price)
		}
	}
	q.EndSqrtPrice = price
	q.EndTick = tick
	q.EndLiquidity = liq
	q.IsExceeded = remaining.Sign() > 0
	return q, nil
}

func targetIsTick(target *big.Int, t int32) bool {
	p, err := SqrtPriceAtTick(t)
	return err == nil && p.Cmp(target) == 0
}

// nextInitialized finds the next initialized tick in the swap direction.
// For a2b it is the greatest tick <= current; for b2a the smallest tick > current.
func nextInitialized(ticks []int32, current int32, a2b bool) (int32, bool) {
	if a2b {
		i := sort.Search(len(ticks), func(i int) bool { return ticks[i] > current })
		if i == 0 {
			return 0, false
		}
		return ticks[i-1], true
	}
	i := sort.Search(len(ticks), func(i int) bool { return ticks[i] > current })
	if i == len(ticks) {
		return 0, false
	}
	return ticks[i], true
}
