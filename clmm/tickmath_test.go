package clmm

import (
	"math/big"
	"math/rand"
	"testing"
)

func TestKnownVectors(t *testing.T) {
	cases := []struct {
		tick int32
		want *big.Int
	}{
		{0, Q64},
		{MinTick, MinSqrtPrice},
		{MaxTick, MaxSqrtPrice},
	}
	for _, c := range cases {
		got, err := SqrtPriceAtTick(c.tick)
		if err != nil {
			t.Fatal(err)
		}
		if got.Cmp(c.want) != 0 {
			t.Errorf("tick %d: got %s want %s", c.tick, got, c.want)
		}
	}
}

func TestOutOfRange(t *testing.T) {
	if _, err := SqrtPriceAtTick(MinTick - 1); err != ErrTickOutOfRange {
		t.Fatal("expected error below MinTick")
	}
	if _, err := SqrtPriceAtTick(MaxTick + 1); err != ErrTickOutOfRange {
		t.Fatal("expected error above MaxTick")
	}
	if _, err := TickAtSqrtPrice(new(big.Int).Sub(MinSqrtPrice, big.NewInt(1))); err != ErrSqrtPriceOutOfRange {
		t.Fatal("expected error below MinSqrtPrice")
	}
}

// Independent reference: sqrt(1.0001^tick) * 2^64 via big.Float at high precision.
func refSqrtPrice(tick int32) *big.Float {
	const prec = 512
	base, _ := new(big.Float).SetPrec(prec).SetString("1.0001")
	base.Sqrt(base)
	abs := tick
	if abs < 0 {
		abs = -abs
	}
	r := new(big.Float).SetPrec(prec).SetInt64(1)
	b := new(big.Float).SetPrec(prec).Set(base)
	for e := abs; e > 0; e >>= 1 {
		if e&1 == 1 {
			r.Mul(r, b)
		}
		b.Mul(b, b)
	}
	if tick < 0 {
		r.Quo(new(big.Float).SetPrec(prec).SetInt64(1), r)
	}
	return r.Mul(r, new(big.Float).SetPrec(prec).SetInt(Q64))
}

func TestAgainstHighPrecisionReference(t *testing.T) {
	ticks := []int32{1, -1, 2, -2, 10, -10, 100, -100, 12345, -12345, 200000, -200000, MinTick, MaxTick}
	for _, tk := range ticks {
		got, _ := SqrtPriceAtTick(tk)
		ref := refSqrtPrice(tk)
		diff := new(big.Float).Sub(new(big.Float).SetInt(got), ref)
		diff.Abs(diff)
		abs, _ := diff.Float64()
		rel, _ := new(big.Float).Quo(diff, ref).Float64()
		// Truncating fixed-point steps lose a few ulps; allow <2 units absolute or 1e-14 relative.
		if f := rel; abs >= 2 && f > 1e-14 {
			t.Errorf("tick %d: relative error %g too large (got %s, ref %s)", tk, f, got, ref.Text('f', 2))
		}
	}
}

func TestMonotonic(t *testing.T) {
	prev, _ := SqrtPriceAtTick(MinTick)
	for tk := MinTick + 1; tk <= MaxTick; tk += 97 {
		p, _ := SqrtPriceAtTick(tk)
		if p.Cmp(prev) <= 0 {
			t.Fatalf("not strictly increasing at tick %d", tk)
		}
		prev = p
	}
	// adjacent ticks around zero
	for tk := int32(-50); tk < 50; tk++ {
		a, _ := SqrtPriceAtTick(tk)
		b, _ := SqrtPriceAtTick(tk + 1)
		if a.Cmp(b) >= 0 {
			t.Fatalf("not increasing between %d and %d", tk, tk+1)
		}
	}
}

func TestRoundTrip(t *testing.T) {
	rng := rand.New(rand.NewSource(1))
	for i := 0; i < 2000; i++ {
		tk := MinTick + int32(rng.Intn(int(MaxTick-MinTick+1)))
		p, _ := SqrtPriceAtTick(tk)
		got, err := TickAtSqrtPrice(p)
		if err != nil || got != tk {
			t.Fatalf("round trip tick %d -> %s -> %d (%v)", tk, p, got, err)
		}
		if tk < MaxTick {
			next, _ := SqrtPriceAtTick(tk + 1)
			mid := new(big.Int).Sub(next, big.NewInt(1))
			if got, _ := TickAtSqrtPrice(mid); got != tk {
				t.Fatalf("price just below tick %d maps to %d", tk+1, got)
			}
		}
	}
}
