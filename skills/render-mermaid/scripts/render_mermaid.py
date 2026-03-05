#!/usr/bin/env python3
import argparse
import base64
import json
import os
import pathlib
import sys
import time
import urllib.request
import zlib


def build_pako(diagram: str) -> str:
    payload = {
        "code": diagram,
        "mermaid": {"theme": "default"},
    }
    raw = json.dumps(payload, separators=(",", ":")).encode("utf-8")
    compressed = zlib.compress(raw, level=9)
    encoded = base64.urlsafe_b64encode(compressed).decode("ascii")
    return f"pako:{encoded}"


def fetch_bytes(url: str) -> bytes:
    req = urllib.request.Request(
        url,
        headers={
            "User-Agent": (
                "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) "
                "AppleWebKit/537.36 (KHTML, like Gecko) "
                "Chrome/120.0.0.0 Safari/537.36"
            )
        },
    )
    with urllib.request.urlopen(req, timeout=15) as resp:
        if resp.status != 200:
            raise RuntimeError(f"HTTP {resp.status} while fetching mermaid image")
        return resp.read()


def ensure_non_empty(text: str) -> str:
    stripped = text.strip()
    if not stripped:
        raise ValueError("mermaid diagram is empty")
    return stripped


def load_diagram(args: argparse.Namespace) -> str:
    if args.input:
        expanded = os.path.expandvars(os.path.expanduser(args.input))
        return ensure_non_empty(pathlib.Path(expanded).read_text(encoding="utf-8"))
    if args.diagram:
        return ensure_non_empty(args.diagram)
    raise ValueError("either --input or --diagram is required")


def default_output_path() -> pathlib.Path:
    home = pathlib.Path(os.path.expanduser("~"))
    base = home / ".aevitas" / "workspace" / "var" / "render-mermaid"
    ts = int(time.time() * 1000)
    return base / f"aevitas-mermaid-{ts}.webp"


def main() -> int:
    parser = argparse.ArgumentParser(description="Render Mermaid diagram via mermaid.ink")
    parser.add_argument("--input", help="Path to .mmd/.txt file containing mermaid diagram")
    parser.add_argument("--diagram", help="Inline mermaid diagram text")
    args = parser.parse_args()

    try:
        diagram = load_diagram(args)
        pako = build_pako(diagram)
        image_url = f"https://mermaid.ink/img/{pako}?theme=default&width=500&scale=2&type=webp"
        live_url = f"https://mermaid.live/edit/#{pako}"
        image_data = fetch_bytes(image_url)
        if not image_data:
            raise RuntimeError("downloaded empty image data")

        out = default_output_path()
        out.parent.mkdir(parents=True, exist_ok=True)
        out.write_bytes(image_data)

        print(
            json.dumps(
                {
                    "image_path": str(out),
                    "image_url": image_url,
                    "live_url": live_url,
                    "bytes": len(image_data),
                },
                ensure_ascii=True,
            )
        )
        return 0
    except Exception as exc:
        print(json.dumps({"error": str(exc)}, ensure_ascii=True), file=sys.stderr)
        return 1


if __name__ == "__main__":
    raise SystemExit(main())
