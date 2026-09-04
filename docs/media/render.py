# SPDX-License-Identifier: Apache-2.0
# Code authors: Vijay Erramilli and Codex
"""Assemble the captured images and caption manifest with FFmpeg."""

import argparse
import json
import subprocess
import tempfile
from pathlib import Path

parser = argparse.ArgumentParser()
parser.add_argument("manifest", type=Path)
parser.add_argument("--ffmpeg", required=True)
args = parser.parse_args()
manifest = json.loads(args.manifest.read_text())
base = args.manifest.parent
with tempfile.TemporaryDirectory() as temporary:
    temp = Path(temporary)
    parts = []
    for index, scene in enumerate(manifest["scenes"]):
        caption = temp / f"caption-{index}.txt"
        caption.write_text(scene["caption"])
        part = temp / f"part-{index}.mp4"
        filters = (
            "scale=1280:600:force_original_aspect_ratio=decrease,"
            "pad=1280:720:(ow-iw)/2:(600-ih)/2:white,setsar=1,"
            "drawbox=x=0:y=600:w=1280:h=120:color=0x171b22:t=fill,"
            f"drawtext=textfile={caption}:expansion=none:fontcolor=white:"
            "fontsize=25:line_spacing=9:x=(w-text_w)/2:y=625"
        )
        subprocess.run(
            [
                args.ffmpeg,
                "-hide_banner",
                "-loglevel",
                "error",
                "-y",
                "-loop",
                "1",
                "-framerate",
                "2",
                "-i",
                str(base / scene["image"]),
                "-t",
                str(scene["seconds"]),
                "-vf",
                filters,
                "-c:v",
                "libx264",
                "-tune",
                "stillimage",
                "-pix_fmt",
                "yuv420p",
                "-crf",
                "20",
                "-an",
                str(part),
            ],
            check=True,
        )
        parts.append(part)
    listing = temp / "parts.txt"
    listing.write_text("".join(f"file '{part}'\n" for part in parts))
    subprocess.run(
        [
            args.ffmpeg,
            "-hide_banner",
            "-loglevel",
            "error",
            "-y",
            "-f",
            "concat",
            "-safe",
            "0",
            "-i",
            str(listing),
            "-c",
            "copy",
            "-movflags",
            "+faststart",
            str(base / "walkthrough.mp4"),
        ],
        check=True,
    )
