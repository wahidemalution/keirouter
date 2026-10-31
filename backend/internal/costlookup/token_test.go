package costlookup

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/mydisha/keirouter/backend/internal/crypto"
)

func TestSealTokenRoundTrip(t *testing.T) {
	mk, err := crypto.GenerateMasterKey()
	require.NoError(t, err)
	s, err := crypto.NewSealer(mk)
	require.NoError(t, err)

	stored, err := SealToken(s, "sys-secret-token")
	require.NoError(t, err)
	require.NotContains(t, stored, "sys-secret-token")
	plain, err := OpenToken(s, stored)
	require.NoError(t, err)
	require.Equal(t, "sys-secret-token", plain)

	_, err = OpenToken(s, "not json")
	require.Error(t, err)
}
