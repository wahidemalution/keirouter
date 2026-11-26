---
name: keirouter-chat
description: Chat / code generation via KeiRouter using OpenAI /v1/chat/completions or Anthropic /v1/messages format with streaming + auto-fallback combos. Use when the user wants to ask an LLM, generate code, summarize text, or run prompts through KeiRouter.
---

# KeiRouter — Chat

Requires `KEIROUTER_URL` (and `KEIROUTER_KEY` if auth enabled). See https://raw.githubusercontent.com/mydisha/keirouter/main/skills/keirouter/SKILL.md for setup.

## Endpoints

- `POST $KEIROUTER_URL/v1/chat/completions` — OpenAI format
- `POST $KEIROUTER_URL/v1/messages` — Anthropic format
- `POST $KEIROUTER_URL/v1/responses` — OpenAI Responses format

## Discover

```bash
curl $KEIROUTER_URL/v1/models | jq '.data[].id'
# Per-model metadata (contextWindow, params)
curl "$KEIROUTER_URL/v1/models/info?id=openai/gpt-4o"
```

Combos (e.g. `vip`, `mycodex`) auto-fallback through multiple providers.

## OpenAI format

```bash
curl -X POST $KEIROUTER_URL/v1/chat/completions \
  -H "Authorization: Bearer $KEIROUTER_KEY" \
  -H "Content-Type: application/json" \
  -d '{"model":"openai/gpt-5","messages":[{"role":"user","content":"Hi"}],"stream":false}'
```

JS (OpenAI SDK):

```js
import OpenAI from "openai";
const client = new OpenAI({ baseURL: `${process.env.KEIROUTER_URL}/v1`, apiKey: process.env.KEIROUTER_KEY });
const res = await client.chat.completions.create({
  model: "openai/gpt-5",
  messages: [{ role: "user", content: "Hi" }],
  stream: true,
});
for await (const chunk of res) process.stdout.write(chunk.choices[0]?.delta?.content || "");
```

## Anthropic format

```bash
curl -X POST $KEIROUTER_URL/v1/messages \
  -H "Authorization: Bearer $KEIROUTER_KEY" \
  -H "anthropic-version: 2023-06-01" \
  -H "Content-Type: application/json" \
  -d '{"model":"anthropic/claude-opus-4-7","max_tokens":1024,"messages":[{"role":"user","content":"Hi"}]}'
```

## Response shape

OpenAI (`/v1/chat/completions`):
```json
{ "id": "chatcmpl-...", "object": "chat.completion", "model": "openai/gpt-5",
  "choices": [{ "index": 0, "message": { "role": "assistant", "content": "Hello!" }, "finish_reason": "stop" }],
  "usage": { "prompt_tokens": 8, "completion_tokens": 2, "total_tokens": 10 } }
```

Streaming (`stream:true`) emits SSE: `data: {choices:[{delta:{content:"..."}}]}\n\n` ... `data: [DONE]\n\n`.

Anthropic (`/v1/messages`):
```json
{ "id": "msg_...", "type": "message", "role": "assistant", "model": "anthropic/claude-opus-4-7",
  "content": [{ "type": "text", "text": "Hello!" }],
  "stop_reason": "end_turn", "usage": { "input_tokens": 8, "output_tokens": 2 } }
```

## Billed cost (`usage.cost`)

A key billed in USD gets `usage.cost` in OpenAI, Anthropic and Responses replies: the amount KeiRouter charged for that request, in USD, after KeiRouter's own markup. Resellers can apply their own markup to this number. Token package keys never get the field.

```json
"usage": { "prompt_tokens": 1200, "completion_tokens": 85, "total_tokens": 1285, "cost": 0.001234 }
```

- The figure is exact, up to 9 decimals (nano-USD), and matches what was debited from the balance. Sum it as is; it is not rounded to 6 decimals.
- Streaming puts it on the last usage frame: the usage-only chunk before `data: [DONE]`, the final `message_delta`, or `response.completed`. On `/v1/chat/completions`, send `"stream_options": {"include_usage": true}` to always get that frame.
- A reply served from KeiRouter's response cache costs nothing, so it carries `"cost": 0`.
- For models routed through Subrouter the number is the ceiling price charged at request time. KeiRouter settles the final amount from Subrouter's usage log afterwards. It can differ from the reply's figure, usually downward, and the portal shows the settled one.
- There is no cost header. The figure lives only in the body.
- A few streams carry no `cost`: one that ends in an error, one that asks for `n` above 1 or for `logprobs` (those keep the raw upstream stream), and an OpenAI stream without `include_usage` whose provider sends no usage. The charge still appears in the portal.

## Provider quick reference

| Provider | `model` format | Examples |
|---|---|---|
| OpenAI | `openai/<model>` | `openai/gpt-5`, `openai/gpt-4o` |
| Anthropic | `anthropic/<model>` | `anthropic/claude-opus-4-7`, `anthropic/claude-sonnet-4-6` |
| Claude Code | `cc/<model>` | `cc/claude-opus-4-7` |
| Gemini | `gemini/<model>` | `gemini/gemini-2.5-pro`, `gemini/gemini-2.5-flash` |
| Groq | `groq/<model>` | `groq/llama-3.3-70b` |
| DeepSeek | `ds/<model>` | `ds/deepseek-chat`, `ds/deepseek-coder` |
| OpenRouter | `openrouter/<model>` | `openrouter/anthropic/claude-opus-4-7` |
| Mistral | `mistral/<model>` | `mistral/mistral-large-latest` |
| xAI | `xai/<model>` | `xai/grok-3` |
| Ollama Local | `ollama-local/<model>` | `ollama-local/llama3.2` |
| Custom OpenAI | `custom-openai/<model>` | Any model on your custom endpoint |
