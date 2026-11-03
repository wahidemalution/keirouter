package market

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestSlugSkipsResetOnReplace(t *testing.T) {
	c := NewSnapshotCache()
	slug := "surplus-trusted@openrouter:m"
	require.False(t, c.Skips().Has(slug))
	c.Skips().Add(slug)
	require.True(t, c.Skips().Has(slug))
	require.False(t, c.Skips().Has("surplus-trusted@inferhub:m"))
	c.Replace(nil)
	require.False(t, c.Skips().Has(slug))
}

func TestSlugSkipsNilSafe(t *testing.T) {
	var s *SlugSkips
	require.False(t, s.Has("surplus-trusted:m"))
	require.NotPanics(t, func() { s.Add("surplus-trusted:m"); s.Reset() })
	var c *SnapshotCache
	require.Nil(t, c.Skips())
}
