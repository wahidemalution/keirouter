package connectors

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/mydisha/keirouter/backend/internal/core"
)

func TestGenerateImageForwardsGrokParams(t *testing.T) {
	var got map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		got = map[string]any{}
		_ = json.Unmarshal(b, &got)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"created":1,"data":[{"b64_json":"AAAA"}]}`))
	}))
	defer srv.Close()

	c := NewOpenAICompatible("wegoo", srv.URL)
	creds := core.Credentials{APIKey: "sk-test"}

	_, err := c.GenerateImage(context.Background(), &core.ImageRequest{
		Model: "grok-imagine", Prompt: "cat", AspectRatio: "16:9", Resolution: "2k", ResponseFormat: "b64_json",
	}, creds)
	require.NoError(t, err)
	require.Equal(t, "16:9", got["aspect_ratio"])
	require.Equal(t, "2k", got["resolution"])
	require.Equal(t, "b64_json", got["response_format"])

	_, err = c.GenerateImage(context.Background(), &core.ImageRequest{Model: "grok-imagine", Prompt: "cat"}, creds)
	require.NoError(t, err)
	require.NotContains(t, got, "aspect_ratio")
	require.NotContains(t, got, "resolution")
}
