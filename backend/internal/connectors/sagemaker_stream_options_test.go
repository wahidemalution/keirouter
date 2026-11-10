package connectors

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/mydisha/keirouter/backend/internal/core"
)

func TestSageMakerStreamOptionsOnceForTokenRequests(t *testing.T) {
	c := NewSageMaker("sagemaker", "")
	req := &core.ChatRequest{
		Model:         "m",
		UpstreamUsage: true, // set for token package requests
		Messages:      []core.Message{{Role: core.RoleUser, Content: []core.ContentPart{{Type: core.PartText, Text: "hi"}}}},
	}
	body, err := c.render(req, core.Credentials{}, true)
	require.NoError(t, err)
	require.Equal(t, 1, strings.Count(string(body), `"stream_options"`), string(body))
}
