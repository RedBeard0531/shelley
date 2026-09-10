---
name: transcribing-audio
description: Use when a task requires STT/ASR.
when: exe.dev
---

## Steps

1. By default, save the transcript beside the original audio with the extension replaced by `.transcript.txt` (`notes/review.m4a` → `notes/review.transcript.txt`).

2. Check the input. The gpt-transcribe endpoint accepts mp3, mp4, mpeg, mpga, m4a, wav, webm, flac, and ogg, up to 25 MB. Transcode unsupported or larger inputs first using ffmpeg. Split and transcribe piecemeal if necessary; for better results, slightly overlap the chunks and then manually stitch together the overlapped outputs.

3. Transcribe. Let `$base` be the attached integration's URL (normally `https://llm.int.exe.xyz`). `response_format=json` is required. Optional `prompt`, `keywords[]`, and `languages[]` fields can supply known context, names, and language codes.
   ```
   curl -sS --fail-with-body "$base/v1/audio/transcriptions" \
     -F model=gpt-transcribe \
     -F response_format=json \
     -F "file=@$upload" \
     -o "$tmpdir/response.json"
   jq -er '.text | select(type == "string")' "$tmpdir/response.json" > "$out"
   ```

## Errors

- `402`: LLM credits exhausted; https://exe.dev/user/shelley.
- Transcription requires managed OpenAI or OpenAI BYOK; ChatGPT subscriptions don't support it. A separate integration can provide transcription without changing the existing chat source.
- To find or connect a suitable integration, use `reflection-integration` and `request-integration`.
