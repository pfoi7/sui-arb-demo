// Package clmm implements Cetus-style concentrated-liquidity math using
// Q64.64 fixed-point sqrt prices and arbitrary-precision integers.
package clmm

import (
	"errors"
	"math/big"
)

const (
	// MinTick and MaxTick bound the tick range used by Cetus CLMM pools.
	MinTick int32 = -443636
	MaxTick int32 = 443636
)

var (
	// Q64 is 2^64, the fixed-point scale of a Q64.64 sqrt price.
	Q64 = new(big.Int).Lsh(big.NewInt(1), 64)
	q96 = new(big.Int).Lsh(big.NewInt(1), 96)
	// MinSqrtPrice is the sqrt price at MinTick.
	MinSqrtPrice = mustBig("4295048016")
	// MaxSqrtPrice is the sqrt price at MaxTick.
	MaxSqrtPrice = mustBig("79226673515401279992447579055")

	// negRatios[i] = 2^64 / sqrt(1.0001)^(2^i)   (Q64.64)
	negRatios = mustBigs(
		"18445821805675392311", "18444899583751176498", "18443055278223354162",
		"18439367220385604838", "18431993317065449817", "18417254355718160513",
		"18387811781193591352", "18329067761203520168", "18212142134806087854",
		"17980523815641551639", "17526086738831147013", "16651378430235024244",
		"15030750278693429944", "12247334978882834399", "8131365268884726200",
		"3584323654723342297", "696457651847595233", "26294789957452057",
		"37481735321082",
	)
	// posRatios[i] = 2^96 * sqrt(1.0001)^(2^i)   (Q32.96)
	posRatios = mustBigs(
		"79232123823359799118286999567", "79236085330515764027303304731",
		"79244008939048815603706035061", "79259858533276714757314932305",
		"79291567232598584799939703904", "79355022692464371645785046466",
		"79482085999252804386437311141", "79736823300114093921829183326",
		"80248749790819932309965073892", "81282483887344747381513967011",
		"83390072131320151908154831281", "87770609709833776024991924138",
		"97234110755111693312479820773", "119332217159966728226237229890",
		"179736315981702064433883588727", "407748233172238350107850275304",
		"2098478828474011932436660412517", "55581415166113811149459800483533",
		"38992368544603139932233054999993551",
	)

	// ErrTickOutOfRange is returned for ticks outside [MinTick, MaxTick].
	ErrTickOutOfRange = errors.New("clmm: tick out of range")
	// ErrSqrtPriceOutOfRange is returned for sqrt prices outside [MinSqrtPrice, MaxSqrtPrice].
	ErrSqrtPriceOutOfRange = errors.New("clmm: sqrt price out of range")
)

func mustBig(s string) *big.Int {
	v, ok := new(big.Int).SetString(s, 10)
	if !ok {
		panic("clmm: bad constant " + s)
	}
	return v
}

func mustBigs(ss ...string) []*big.Int {
	out := make([]*big.Int, len(ss))
	for i, s := range ss {
		out[i] = mustBig(s)
	}
	return out
}

// SqrtPriceAtTick returns sqrt(1.0001^tick) as a Q64.64 value.
//
// It follows the Cetus on-chain algorithm bit for bit: negative ticks are
// accumulated in Q64.64, positive ticks in Q32.96 and then shifted down by 32.
// Each multiplication truncates, exactly like the Move u128/u256 mul_shr.
func SqrtPriceAtTick(tick int32) (*big.Int, error) {
	if tick < MinTick || tick > MaxTick {
		return nil, ErrTickOutOfRange
	}
	if tick < 0 {
		abs := uint32(-tick)
		r := new(big.Int).Set(Q64)
		if abs&1 != 0 {
			r.Set(negRatios[0])
		}
		for i := 1; i < len(negRatios); i++ {
			if abs&(1<<uint(i)) != 0 {
				r.Mul(r, negRatios[i]).Rsh(r, 64)
			}
		}
		return r, nil
	}
	abs := uint32(tick)
	r := new(big.Int).Set(q96)
	if abs&1 != 0 {
		r.Set(posRatios[0])
	}
	for i := 1; i < len(posRatios); i++ {
		if abs&(1<<uint(i)) != 0 {
			r.Mul(r, posRatios[i]).Rsh(r, 96)
		}
	}
	return r.Rsh(r, 32), nil
}

// TickAtSqrtPrice returns the greatest tick t with SqrtPriceAtTick(t) <= sqrtPrice.
// It binary-searches the monotonic forward function, so it is exact by construction.
func TickAtSqrtPrice(sqrtPrice *big.Int) (int32, error) {
	if sqrtPrice.Cmp(MinSqrtPrice) < 0 || sqrtPrice.Cmp(MaxSqrtPrice) > 0 {
		return 0, ErrSqrtPriceOutOfRange
	}
	lo, hi := MinTick, MaxTick
	for lo < hi {
		mid := lo + (hi-lo+1)/2
		p, _ := SqrtPriceAtTick(mid)
		if p.Cmp(sqrtPrice) <= 0 {
			lo = mid
		} else {
			hi = mid - 1
		}
	}
	return lo, nil
}
