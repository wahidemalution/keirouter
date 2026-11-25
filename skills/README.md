# KeiRouter — Agent Skills

Drop-in skills for any AI agent (Claude, Cursor, ChatGPT, custom SDK). Just **copy a link** below and paste it to your AI — it will fetch the skill and use KeiRouter for you.

> Tip: start with the **keirouter** entry skill — it covers setup and links to all capability skills.

## Skills

| Capability | Copy link below and paste to your AI |
|---|---|
| **Entry / Setup** (start here) | https://raw.githubusercontent.com/mydisha/keirouter/main/skills/keirouter/SKILL.md |
| Chat / code-gen | https://raw.githubusercontent.com/mydisha/keirouter/main/skills/keirouter-chat/SKILL.md |
| Image generation (portal page "Buat Gambar" only, not the API) | https://raw.githubusercontent.com/mydisha/keirouter/main/skills/keirouter-image/SKILL.md |
| Text-to-speech | https://raw.githubusercontent.com/mydisha/keirouter/main/skills/keirouter-tts/SKILL.md |
| Speech-to-text | https://raw.githubusercontent.com/mydisha/keirouter/main/skills/keirouter-stt/SKILL.md |
| Embeddings | https://raw.githubusercontent.com/mydisha/keirouter/main/skills/keirouter-embeddings/SKILL.md |
| Web search | https://raw.githubusercontent.com/mydisha/keirouter/main/skills/keirouter-web-search/SKILL.md |
| Web fetch (URL → markdown) | https://raw.githubusercontent.com/mydisha/keirouter/main/skills/keirouter-web-fetch/SKILL.md |

## How to use

Paste to your AI (Claude, Cursor, ChatGPT, …):

```
Read this skill and use it: https://raw.githubusercontent.com/mydisha/keirouter/main/skills/keirouter/SKILL.md
```

Then ask normally — *"transcribe this URL"*, *"search the web for ..."*, etc.

## Configure your shell once

```bash
export KEIROUTER_URL="http://localhost:20180"   # local default, or your VPS / tunnel URL
export KEIROUTER_KEY="sk-..."                   # from Dashboard → Keys (only if auth enabled)
```

Verify: `curl $KEIROUTER_URL/healthz` → `{"ok":true}`.

## Links

- Source: https://github.com/mydisha/keirouter
- Dashboard: http://localhost:20180 (local)
