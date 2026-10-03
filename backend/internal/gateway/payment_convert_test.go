package gateway

import (
	"math"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestIDRToMicros(t *testing.T) {
	// 1 USD = 16000 IDR. Rp 16000 -> 1 USD -> 1_000_000 micros.
	credit, rateMicros, err := idrToMicros(16000, 16000)
	require.NoError(t, err)
	require.EqualValues(t, 1_000_000, credit)
	require.EqualValues(t, 16_000_000_000, rateMicros)

	// Rp 50000 -> 3.125 USD -> 3_125_000 micros exactly.
	credit, _, err = idrToMicros(50000, 16000)
	require.NoError(t, err)
	require.EqualValues(t, 3_125_000, credit)

	// half-up: 1 IDR at rate 3 IDR/USD = 0.333333 USD = 333333.33 micros -> 333333.
	credit, _, err = idrToMicros(1, 3)
	require.NoError(t, err)
	require.EqualValues(t, 333_333, credit)
}

func TestIDRToMicrosRejectsBadRate(t *testing.T) {
	for _, r := range []float64{0, -1, math.NaN(), math.Inf(1), 0.0000004} {
		_, _, err := idrToMicros(10000, r)
		require.Error(t, err, "rate %v", r)
	}
}