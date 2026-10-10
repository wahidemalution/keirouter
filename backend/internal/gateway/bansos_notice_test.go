package gateway

import (
	"bytes"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/mydisha/keirouter/backend/internal/core"
	"github.com/mydisha/keirouter/backend/internal/transform"
)

// The bansos notice must appear as trailing text for a bansos response while
// leaving the model's own content intact, and never touch a regular key.
func TestAppendNoticePartOpenAIAppendsToString(t *testing.T) {
	resp := &core.ChatResponse{
		Model: "gpt-x",
		Message: core.Message{
			Role:    core.RoleAssistant,
			Content: []core.ContentPart{{Type: core.PartText, Text: "hello"}},
		},
	}
	appendNoticePart(resp, "ini bansos dari tokenizer.id")

	out, err := transform.OpenAICodec{}.RenderResponse(resp)
	require.NoError(t, err)

	var body struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	require.NoError(t, json.Unmarshal(out, &body))
	require.Len(t, body.Choices, 1)
	require.Contains(t, body.Choices[0].Message.Content, "hello")
	require.Contains(t, body.Choices[0].Message.Content, "ini bansos dari tokenizer.id")
	require.Less(t, indexOf(body.Choices[0].Message.Content, "hello"), indexOf(body.Choices[0].Message.Content, "ini bansos dari tokenizer.id"), "model text must come first")
}

func TestAppendNoticePartAnthropicSeparateBlock(t *testing.T) {
	resp := &core.ChatResponse{
		Model: "claude-x",
		Message: core.Message{
			Role:    core.RoleAssistant,
			Content: []core.ContentPart{{Type: core.PartText, Text: "hello"}},
		},
	}
	appendNoticePart(resp, "ini bansos dari tokenizer.id")

	out, err := transform.AnthropicCodec{}.RenderResponse(resp)
	require.NoError(t, err)

	var body struct {
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
	}
	require.NoError(t, json.Unmarshal(out, &body))
	require.Len(t, body.Content, 2, "notice must be a separate text block")
	require.Equal(t, "hello", body.Content[0].Text)
	require.Contains(t, body.Content[1].Text, "ini bansos dari tokenizer.id")
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}

// The direct-stream injector must emit the notice only for the bansos key and
// leave the upstream frames byte-for-byte intact.
func TestNoticeInjectingWriterOpenAI(t *testing.T) {
	var dst bytes.Buffer
	w := &noticeInjectingWriter{dst: &dst, dialect: core.DialectOpenAI, notice: "NOTICE"}
	upstream := "data: {\"id\":\"x\",\"model\":\"m\",\"choices\":[{\"delta\":{\"content\":\"hi\"},\"finish_reason\":null}]}\n\n" +
		"data: {\"id\":\"x\",\"model\":\"m\",\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n" +
		"data: [DONE]\n\n"
	n, err := w.Write([]byte(upstream))
	require.NoError(t, err)
	require.Equal(t, len(upstream), n)
	require.NoError(t, w.FlushFrame())

	out := dst.String()
	noticeIdx := indexOf(out, "NOTICE")
	stopIdx := indexOf(out, `"finish_reason":"stop"`)
	doneIdx := indexOf(out, "[DONE]")
	require.Greater(t, noticeIdx, -1, "notice must be emitted")
	require.Less(t, noticeIdx, stopIdx, "notice must precede finish_reason")
	require.Less(t, stopIdx, doneIdx, "upstream order preserved")
	require.Contains(t, out, `"id":"x"`, "notice chunk must reuse upstream id")
	require.Contains(t, out, `"model":"m"`, "notice chunk must reuse upstream model")
}

func TestNoticeInjectingWriterAnthropicBlockStructure(t *testing.T) {
	var dst bytes.Buffer
	w := &noticeInjectingWriter{dst: &dst, dialect: core.DialectAnthropic, notice: "NOTICE"}
	upstream := "event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"hi\"}}\n\n" +
		"event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n"
	require.NoError(t, func() error { _, err := w.Write([]byte(upstream)); return err }())
	require.NoError(t, w.FlushFrame())

	out := dst.String()
	require.Contains(t, out, "event: content_block_start")
	require.Contains(t, out, "event: content_block_stop")
	require.Contains(t, out, "\"index\":1", "notice block opens at the next free index after 0")
	require.Less(t, indexOf(out, "NOTICE"), indexOf(out, "message_stop"), "notice must precede message_stop")
}

