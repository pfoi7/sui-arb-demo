package clmm

import (
	"errors"
	"math/big"
)

// FeeRateDenominator is the Cetus fee-rate scale: fee 2500 = 0.25%.
const FeeRateDenominator = 1_000_000

var (
	// ErrZeroLiquidity is returned when a computation needs non-zero liquidity.
	ErrZeroLiquidity = errors.New("clmm: zero liquidity")
	bigFeeDen        = big.NewInt(FeeRateDenominator)
)

func divRound(n, d *big.Int, up bool) *big.Int {
	q, r := new(big.Int).QuoRem(n, d, new(big.Int))
	if up && r.Sign() != 0 {
		q.Add(q, big.NewInt(1))
	}
	return q
}

func order(a, b *big.Int) (*big.Int, *big.Int) {
	if a.Cmp(b) > 0 {
		return b, a
	}
	return a, b
}

// DeltaA is the amount of token A between two sqrt prices for liquidity L:
// L * (sqrtHi - sqrtLo) * 2^64 / (sqrtLo * sqrtHi).
func DeltaA(p0, p1, liquidity *big.Int, roundUp bool) *big.Int {
	lo, hi := order(p0, p1)
	if liquidity.Sign() == 0 || lo.Cmp(hi) == 0 {
		return new(big.Int)
	}
	num := new(big.Int).Sub(hi, lo)
	num.Mul(num, liquidity).Lsh(num, 64)
	den := new(big.Int).Mul(lo, hi)
	return divRound(num, den, roundUp)
}

// DeltaB is the amount of token B between two sqrt prices for liquidity L:
// L * (sqrtHi - sqrtLo) / 2^64.
func DeltaB(p0, p1, liquidity *big.Int, roundUp bool) *big.Int {
	lo, hi := order(p0, p1)
	if liquidity.Sign() == 0 || lo.Cmp(hi) == 0 {
		return new(big.Int)
	}
	num := new(big.Int).Sub(hi, lo)
	num.Mul(num, liquidity)
	return divRound(num, Q64, roundUp)
}

// NextSqrtPriceFromInput returns the sqrt price after adding amountIn.
// a2b (token A in) moves the price down, rounding up; b2a moves it up, rounding down.
func NextSqrtPriceFromInput(sqrtPrice, liquidity, amountIn *big.Int, a2b bool) (*big.Int, error) {
	if liquidity.Sign() == 0 {
		return nil, ErrZeroLiquidity
	}
	if amountIn.Sign() == 0 {
		return new(big.Int).Set(sqrtPrice), nil
	}
	if a2b {
		// L * P * 2^64 / (L * 2^64 + amount * P)
		num := new(big.Int).Mul(liquidity, sqrtPrice)
		num.Lsh(num, 64)
		den := new(big.Int).Lsh(liquidity, 64)
		den.Add(den, new(big.Int).Mul(amountIn, sqrtPrice))
		return divRound(num, den, true), nil
	}
	// P + amount * 2^64 / L
	d := divRound(new(big.Int).Lsh(amountIn, 64), liquidity, false)
	return d.Add(d, sqrtPrice), nil
}

// SwapStep is the result of a single swap step inside one liquidity range.
type SwapStep struct {
	NextSqrtPrice *big.Int
	AmountIn      *big.Int // excludes fee
	AmountOut     *big.Int
	FeeAmount     *big.Int
}

// ComputeSwapStep performs an exact-input swap step from current toward target
// with constant liquidity. feeRate is in units of FeeRateDenominator.
func ComputeSwapStep(current, target, liquidity, amountRemaining *big.Int, feeRate uint64, a2b bool) (SwapStep, error) {
	if feeRate >= FeeRateDenominator {
		return SwapStep{}, errors.New("clmm: fee rate too high")
	}
	if liquidity.Sign() == 0 {
		// Nothing to trade against: jump straight to target.
		return SwapStep{NextSqrtPrice: new(big.Int).Set(target), AmountIn: new(big.Int), AmountOut: new(big.Int), FeeAmount: new(big.Int)}, nil
	}
	fr := new(big.Int).SetUint64(feeRate)
	lessFee := new(big.Int).Mul(amountRemaining, new(big.Int).Sub(bigFeeDen, fr))
	lessFee.Quo(lessFee, bigFeeDen)

	var maxIn *big.Int
	if a2b {
		maxIn = DeltaA(target, current, liquidity, true)
	} else {
		maxIn = DeltaB(current, target, liquidity, true)
	}

	var s SwapStep
	if maxIn.Cmp(lessFee) > 0 {
		next, err := NextSqrtPriceFromInput(current, liquidity, lessFee, a2b)
		if err != nil {
			return SwapStep{}, err
		}
		s.NextSqrtPrice = next
		s.AmountIn = lessFee
		s.FeeAmount = new(big.Int).Sub(amountRemaining, lessFee)
	} else {
		s.NextSqrtPrice = new(big.Int).Set(target)
		s.AmountIn = maxIn
		s.FeeAmount = divRound(new(big.Int).Mul(maxIn, fr), new(big.Int).Sub(bigFeeDen, fr), true)
	}
	if a2b {
		s.AmountOut = DeltaB(s.NextSqrtPrice, current, liquidity, false)
	} else {
		s.AmountOut = DeltaA(current, s.NextSqrtPrice, liquidity, false)
	}
	return s, nil
}
