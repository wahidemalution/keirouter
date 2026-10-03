package gateway

import (
	"errors"
	"math"
	"math/big"
)

// idrToMicros converts a whole-IDR amount to credit in micro-USD using the
// given IDR-per-USD rate. It never uses float64 for the money math: the rate is
// first quantized to integer micro-IDR (rate x 1e6) and all division is integer
// with half-up rounding at the micro boundary.
//
//	creditMicros = round(amountIDR * 1e6 * 1e6 / rateMicros)
func idrToMicros(amountIDR int64, rate float64) (int64, int64, error) {
	if amountIDR <= 0 {
		return 0, 0, errors.New("amount_idr must be positive")
	}
	if math.IsNaN(rate) || math.IsInf(rate, 0) || rate <= 0 {
		return 0, 0, errors.New("currency rate is not available")
	}
	// Quantize rate to integer micro-IDR, half-up.
	rateRat := new(big.Rat).SetFloat64(rate)
	rateRat.Mul(rateRat, big.NewRat(1_000_000, 1))
	rateMicrosBig := roundHalfUpRat(rateRat)
	if !rateMicrosBig.IsInt64() || rateMicrosBig.Int64() <= 0 {
		return 0, 0, errors.New("currency rate is out of range")
	}
	rateMicros := rateMicrosBig.Int64()

	// credit = amountIDR * 1e12 / rateMicros, half-up.
	num := new(big.Int).Mul(big.NewInt(amountIDR), big.NewInt(1_000_000_000_000))
	credit := roundHalfUpDiv(num, big.NewInt(rateMicros))
	if !credit.IsInt64() || credit.Int64() <= 0 {
		return 0, 0, errors.New("converted credit is out of range")
	}
	return credit.Int64(), rateMicros, nil
}

// roundHalfUpRat rounds a rational to the nearest integer, halves up.
func roundHalfUpRat(r *big.Rat) *big.Int {
	num := new(big.Int).Mul(r.Num(), big.NewInt(2))
	den := new(big.Int).Mul(r.Denom(), big.NewInt(2))
	// floor((2*num + den) / (2*den)) with sign awareness via Div rounding toward zero.
	adj := new(big.Int).Add(num, r.Denom())
	return adj.Div(adj, den)
}

// roundHalfUpDiv returns round(num/den) with halves away from zero. num >= 0,
// den > 0 in this feature.
func roundHalfUpDiv(num, den *big.Int) *big.Int {
	q, rem := new(big.Int).QuoRem(num, den, new(big.Int))
	twice := new(big.Int).Lsh(rem.Abs(rem), 1)
	if twice.Cmp(den) >= 0 {
		if q.Sign() < 0 {
			q.Sub(q, big.NewInt(1))
		} else {
			q.Add(q, big.NewInt(1))
		}
	}
	return q
}
