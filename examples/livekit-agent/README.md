# livekit-agent: the example LiveKit agent (text and audio)

A tiny LiveKit agent for the Mochi Shoes support desk, built with the LiveKit Go SDK and Genkit.
It behaves like a LiveKit Agents worker: it joins every room whose name starts with `tamesu-`,
publishes its state in the `lk.agent.state` attribute, reads user turns from the `lk.chat` text
stream and answers on `lk.transcription`. With `--audio` it also subscribes to the caller's
microphone, detects utterances with the energy VAD and speaks each reply as a tone on its own
Opus track (needs libopus: `brew install opus` / `apt install libopus-dev`), stopping when the
caller talks over it. Two suites test it: `suite.yaml` in text mode and `suite-audio.yaml` in
audio mode (with `transcript_source: stt` on one target so tamesu's STT path runs too).

```sh
# A LiveKit server with the dev keys (devkey / secret):
livekit-server --dev                       # brew install livekit, or docker run livekit/livekit-server --dev

# The agent, without a model, listening and speaking too:
go run . --scripted --audio &

# The suite, with the fake model provider (a tamesu binary built with -tags llmfake):
export LIVEKIT_API_KEY=devkey LIVEKIT_API_SECRET=secret
tamesu run --config tamesu.llmfake.yaml
```

With a model: `GEMINI_API_KEY=... go run .` and a `tamesu.yaml` with real models (see
`examples/rest-agent/tamesu.yaml`).
