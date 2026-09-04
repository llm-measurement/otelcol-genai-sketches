# Collector Walkthrough

[Watch or download the 90-second video](walkthrough.mp4).

This silent, captioned video uses a real Grafana screenshot and plots of live
reconciliation results captured on 2026-09-04. It is an edited walkthrough of
synthetic output, not a real-time screen recording or a performance benchmark.

## Transcript

- **0:00-0:30:** The collector turns OTLP spans into bounded metrics. Compare model
  request rate, reported token rate, and missing usage. Slice labels are cleartext;
  prompt hashes are not metric labels.
- **0:30-1:00:** Increasing tool calls changes 400 spans into 1,100 spans while the
  model-request count stays at 100. [Run this investigation](../investigations/TOOL_SPANS.md).
- **1:00-1:30:** Omitting usage on half the requests halves reported tokens but not
  known synthetic consumption. Missing usage rises to 50 of 100 requests.
  [Run this investigation](../investigations/MISSING_USAGE.md).

## Reproduce It

Start the demo with `sh examples/demo.sh up`. Capture the dashboard at
`http://localhost:3000/d/genai-sketches`. Its continually generated traffic is random,
so rates and estimates will differ from the screenshot.

The controlled investigations are deterministic for their accounting results:

```sh
# From the repository root, with the demo already running:
GENAI_SKETCH_SECRET=unused-for-compose-inspection-only \
  docker compose -f examples/compose.yaml run --rm -T --no-deps \
  app python /app/investigate.py > docs/investigations/results.json

# Optional: regenerate the documentation plots and video.
python -m pip install matplotlib
python docs/investigations/plot.py
python docs/media/render.py docs/media/scenes.json --ffmpeg ffmpeg
```

The temporary Compose interpolation value above is not applied to the running
collector and is not a real hashing secret. The demo's original random secret
remains in effect. The video renderer uses Python's standard library and an installed
FFmpeg with `drawtext` and H.264 support; it was checked with FFmpeg 7.1.
Caption text, image sources, and timing are recorded in [scenes.json](scenes.json).
