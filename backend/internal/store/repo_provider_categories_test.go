package store

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestProviderCategoriesRoundTrip(t *testing.T) {
	ctx := context.Background()
	db := newTestDB(t)
	repo := db.ProviderCategories()

	require.NoError(t, repo.Create(ctx, ProviderCategory{
		ID: "deepseek", TenantID: "default", Label: "DeepSeek",
		CreatedAt: time.Now(), UpdatedAt: time.Now(),
	}))

	list, err := repo.List(ctx, "default")
	require.NoError(t, err)
	require.Len(t, list, 1)
	require.Equal(t, "deepseek", list[0].ID)
	require.Equal(t, "DeepSeek", list[0].Label)

	require.NoError(t, repo.Delete(ctx, "deepseek"))
	list, err = repo.List(ctx, "default")
	require.NoError(t, err)
	require.Empty(t, list)
}

func TestProviderCategoriesTenantScoped(t *testing.T) {
	ctx := context.Background()
	db := newTestDB(t)
	repo := db.ProviderCategories()

	require.NoError(t, repo.Create(ctx, ProviderCategory{
		ID: "openai", TenantID: "default", Label: "OpenAI",
		CreatedAt: time.Now(), UpdatedAt: time.Now(),
	}))

	other, err := repo.List(ctx, "someone-else")
	require.NoError(t, err)
	require.Empty(t, other)
}

func TestChainDisplayProviderRoundTrip(t *testing.T) {
	ctx := context.Background()
	db := newTestDB(t)
	repo := db.Chains()

	c := Chain{
		ID: "c1", TenantID: "default", Name: "my-chain", Strategy: "priority",
		DisplayProvider: "deepseek",
		Steps:           []ChainStep{{ID: "s1", ChainID: "c1", Position: 0, Provider: "custom-openai-x", Model: "m"}},
		CreatedAt:       time.Now(), UpdatedAt: time.Now(),
	}
	require.NoError(t, repo.Create(ctx, c))

	got, err := repo.Get(ctx, "c1")
	require.NoError(t, err)
	require.Equal(t, "deepseek", got.DisplayProvider)

	c.DisplayProvider = "openai"
	require.NoError(t, repo.Update(ctx, c))
	got, err = repo.Get(ctx, "c1")
	require.NoError(t, err)
	require.Equal(t, "openai", got.DisplayProvider)
}
