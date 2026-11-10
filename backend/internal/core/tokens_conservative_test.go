package core

import (
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func promptWith(parts ...ContentPart) *ChatRequest {
	return &ChatRequest{Messages: []Message{{Role: RoleUser, Content: append([]ContentPart{{Type: PartText, Text: "look"}}, parts...)}}}
}

func TestTokenPackageEstimateImages(t *testing.T) {
	base := EstimateTokenPackagePromptTokens(promptWith())
	paygBase := EstimatePromptTokens(promptWith())
	require.Equal(t, paygBase, base, "plain text is estimated the same")

	cases := []struct {
		name        string
		media       *MediaPayload
		token, payg int
	}{
		// 32x32 patches: 1024 patches at the highest OpenAI patch multiplier, rounded up.
		{"known_size", &MediaPayload{MIMEType: "image/png", Data: pngBase64(1024, 1024)}, 2520, 765},
		{"small_known_size", &MediaPayload{MIMEType: "image/png", Data: pngBase64(200, 200)}, 255, 255},
		{"url", &MediaPayload{URL: "https://example.com/a.png"}, imageCeilingTokens, defaultImageTokens},
		{"undecodable", &MediaPayload{MIMEType: "image/webp", Data: base64.StdEncoding.EncodeToString([]byte("RIFF....WEBPVP8 "))}, imageCeilingTokens, defaultImageTokens},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			part := ContentPart{Type: PartImage, Media: c.media}
			require.Equal(t, base+c.token, EstimateTokenPackagePromptTokens(promptWith(part)))
			require.Equal(t, paygBase+c.payg, EstimatePromptTokens(promptWith(part)), "PAYG unchanged")
		})
	}
}

func TestTokenPackageEstimateDocuments(t *testing.T) {
	pdf := "%PDF-1.7\n1 0 obj <</Type /Pages /Count 3>> endobj\n" +
		"2 0 obj <</Type /Page>> endobj 3 0 obj <</Type/Page>> endobj 4 0 obj <</Type /Page /Parent 1 0 R>> endobj\n" +
		strings.Repeat("x", 4000)
	part := ContentPart{Type: PartDocument, Media: &MediaPayload{MIMEType: "application/pdf", Data: base64.StdEncoding.EncodeToString([]byte(pdf))}}
	base := EstimateTokenPackagePromptTokens(promptWith())
	paygBase := EstimatePromptTokens(promptWith())

	require.Equal(t, base+len(pdf)/2+3*documentPageTokens, EstimateTokenPackagePromptTokens(promptWith(part)))
	require.Equal(t, paygBase+len(pdf)/documentBytesPerToken, EstimatePromptTokens(promptWith(part)), "PAYG unchanged")
}

func TestTokenPackageEstimateRequestFields(t *testing.T) {
	schema := json.RawMessage(`{"type":"json_schema","json_schema":{"name":"answer","schema":{"type":"object","properties":{"title":{"type":"string","description":"A long description of the title field"},"items":{"type":"array"}}}}}`)
	rawTool := json.RawMessage(`{"type":"custom","name":"grammar","format":{"type":"grammar","syntax":"lark","definition":"start: expr\nexpr: term (\"+\" term)*\nterm: NUMBER"}}`)
	cases := map[string]func(r *ChatRequest){
		"response_format": func(r *ChatRequest) { r.ResponseFormat = schema },
		"raw_tool":        func(r *ChatRequest) { r.Tools = []Tool{{Name: "grammar", Raw: rawTool}} },
		"tool_choice": func(r *ChatRequest) {
			r.ToolChoice = map[string]any{"type": "function", "function": map[string]any{"name": "lookup_customer_record"}}
		},
		"extra": func(r *ChatRequest) {
			r.Extra = map[string]json.RawMessage{"metadata": json.RawMessage(`{"note":"some caller supplied text here"}`)}
		},
	}
	for name, set := range cases {
		t.Run(name, func(t *testing.T) {
			plain := promptWith()
			plain.Tools = []Tool{{Name: "grammar"}}
			with := promptWith()
			with.Tools = []Tool{{Name: "grammar"}}
			set(with)
			require.Greater(t, EstimateTokenPackagePromptTokens(with), EstimateTokenPackagePromptTokens(plain))
			require.Equal(t, EstimatePromptTokens(plain), EstimatePromptTokens(with), "PAYG unchanged")
		})
	}
}

// Page markers are counted across the decoder's read boundaries.
func TestTokenPackageEstimateCountsEveryPage(t *testing.T) {
	var b strings.Builder
	for b.Len() < 200_000 {
		b.WriteString(strings.Repeat("y", 991))
		b.WriteString("<</Type /Page>>")
	}
	pdf := b.String()
	want := strings.Count(pdf, "/Type /Page>")
	got := conservativeDocumentTokens(&MediaPayload{Data: base64.StdEncoding.EncodeToString([]byte(pdf))})
	require.Equal(t, len(pdf)/documentTokenBytes+want*documentPageTokens, got)
}

// A document sent by URL has no bytes to size: it gets a fixed allowance.
func TestTokenPackageEstimateURLDocument(t *testing.T) {
	part := ContentPart{Type: PartDocument, Media: &MediaPayload{MIMEType: "application/pdf", URL: "https://example.com/a.pdf"}}
	require.Equal(t, EstimateTokenPackagePromptTokens(promptWith())+documentURLPages*documentPageTokens,
		EstimateTokenPackagePromptTokens(promptWith(part)))
	require.Equal(t, EstimatePromptTokens(promptWith()), EstimatePromptTokens(promptWith(part)), "PAYG unchanged")
}
