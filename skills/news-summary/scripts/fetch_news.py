#!/usr/bin/env python3
import argparse
import datetime as dt
import json
import re
import sys
import urllib.request
import xml.etree.ElementTree as ET

FEEDS = {
    "world": "https://feeds.bbci.co.uk/news/world/rss.xml",
    "top": "https://feeds.bbci.co.uk/news/rss.xml",
    "business": "https://feeds.bbci.co.uk/news/business/rss.xml",
    "tech": "https://feeds.bbci.co.uk/news/technology/rss.xml",
    "reuters": "https://www.reutersagency.com/feed/?best-regions=world&post_type=best",
    "npr": "https://feeds.npr.org/1001/rss.xml",
    "aljazeera": "https://www.aljazeera.com/xml/rss/all.xml",
}

GROUPS = {
    "brief": ["world", "business", "tech"],
    "all": ["world", "top", "business", "tech", "reuters", "npr", "aljazeera"],
}

CATEGORY_HINTS = {
    "world": "global affairs",
    "top": "broad top headlines",
    "business": "business and markets",
    "tech": "technology industry",
    "reuters": "Reuters world wire",
    "npr": "NPR top stories",
    "aljazeera": "Al Jazeera general feed",
}


def clean_text(raw: str) -> str:
    raw = re.sub(r"<[^>]+>", " ", raw or "")
    raw = re.sub(r"\s+", " ", raw).strip()
    return raw


def truncate(text: str, max_len: int) -> str:
    if len(text) <= max_len:
        return text
    return text[: max_len - 1].rstrip() + "…"


def fetch_xml(url: str, timeout: int) -> bytes:
    req = urllib.request.Request(url, headers={"User-Agent": "aevitas-news-summary/1.0"})
    with urllib.request.urlopen(req, timeout=timeout) as resp:
        return resp.read()


def parse_rss(feed_key: str, xml_bytes: bytes, limit: int, desc_len: int):
    root = ET.fromstring(xml_bytes)
    items = root.findall("./channel/item")
    out = []
    for it in items[:limit]:
        title = clean_text(it.findtext("title", default=""))
        desc = clean_text(it.findtext("description", default=""))
        link = clean_text(it.findtext("link", default=""))
        pub = clean_text(it.findtext("pubDate", default=""))
        if not title:
            continue
        out.append(
            {
                "feed": feed_key,
                "title": truncate(title, 160),
                "description": truncate(desc, desc_len),
                "link": link,
                "pubDate": pub,
            }
        )
    return out


def render_markdown(items_by_feed):
    today = dt.datetime.now().strftime("%Y-%m-%d")
    lines = [f"📰 News Summary ({today})", ""]
    for feed, items in items_by_feed.items():
        lines.append(f"## {feed.upper()}")
        if not items:
            lines.append("- (no items)")
            lines.append("")
            continue
        for i, item in enumerate(items, 1):
            lines.append(f"{i}. {item['title']}")
            if item["description"]:
                lines.append(f"   - {item['description']}")
            if item["link"]:
                lines.append(f"   - {item['link']}")
        lines.append("")
    return "\n".join(lines).strip() + "\n"


def main():
    p = argparse.ArgumentParser(description="Fetch and summarize RSS news feeds.")
    p.add_argument("--group", choices=sorted(GROUPS.keys()), default="brief")
    p.add_argument("--feeds", default="", help="Comma-separated feed keys; overrides --group.")
    p.add_argument("--limit", type=int, default=5, help="Items per feed.")
    p.add_argument("--desc-len", type=int, default=180, help="Max description chars.")
    p.add_argument("--timeout", type=int, default=15, help="HTTP timeout seconds.")
    p.add_argument("--json", action="store_true", help="Output JSON instead of markdown.")
    p.add_argument("--list-feeds", action="store_true", help="Print supported feed keys and groups, then exit.")
    args = p.parse_args()

    if args.list_feeds:
        payload = {
            "groups": GROUPS,
            "feeds": {k: {"url": FEEDS[k], "category": CATEGORY_HINTS.get(k, "")} for k in sorted(FEEDS.keys())},
        }
        print(json.dumps(payload, ensure_ascii=False, indent=2))
        return 0

    if args.feeds.strip():
        feed_keys = [x.strip().lower() for x in args.feeds.split(",") if x.strip()]
    else:
        feed_keys = GROUPS[args.group]
    invalid = [k for k in feed_keys if k not in FEEDS]
    if invalid:
        print(f"invalid feed keys: {', '.join(invalid)}", file=sys.stderr)
        return 2

    limit = max(1, min(args.limit, 20))
    desc_len = max(40, min(args.desc_len, 400))

    by_feed = {}
    for key in feed_keys:
        try:
            data = fetch_xml(FEEDS[key], args.timeout)
            by_feed[key] = parse_rss(key, data, limit, desc_len)
        except Exception as e:
            by_feed[key] = []
            by_feed[key].append({"feed": key, "title": f"fetch failed: {e}", "description": "", "link": "", "pubDate": ""})

    if args.json:
        print(json.dumps(by_feed, ensure_ascii=False, indent=2))
    else:
        print(render_markdown(by_feed))
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
