#!/usr/bin/env python3
"""Benchmark tools compression on a real or synthetic tools array.

Usage:
  # With a JSON file of tools (e.g. captured from an agent session):
  uv run benchmark_tools.py tools.json

  # Generate synthetic tools for quick testing:
  uv run benchmark_tools.py --synthetic 50

  # Capture real tools from a running LiteLLM proxy log, or export from
  # your agent's request payload as JSON.
"""
# /// script
# requires-python = ">=3.10"
# dependencies = []
# ///

from __future__ import annotations

import argparse
import copy
import hashlib
import json
import re
import sys
import time


# ---------------------------------------------------------------------------
# Inline the rule-based compression (no dependency on litellm)
# ---------------------------------------------------------------------------

_EXAMPLE_RE = re.compile(
    r"(?:^|\n)\s*(?:examples?|e\.g\.|for example|usage|tip|note|```)"
    r"[^\n]*(?:\n|$).*",
    re.IGNORECASE | re.DOTALL,
)


def _truncate_desc(desc: str, max_len: int) -> str:
    desc = _EXAMPLE_RE.sub("", desc).strip()
    if len(desc) <= max_len:
        return desc
    truncated = desc[:max_len]
    last_period = truncated.rfind(".")
    last_newline = truncated.rfind("\n")
    cut_at = max(last_period, last_newline)
    if cut_at > max_len // 2:
        return desc[: cut_at + 1].strip()
    return truncated.rstrip() + "..."


def _strip_param_descriptions(schema: dict) -> None:
    if not isinstance(schema, dict):
        return
    for prop in schema.get("properties", {}).values():
        if isinstance(prop, dict):
            prop.pop("description", None)
            _strip_param_descriptions(prop)
    items = schema.get("items")
    if isinstance(items, dict):
        items.pop("description", None)
        _strip_param_descriptions(items)
    for key in ("anyOf", "oneOf", "allOf"):
        for variant in schema.get(key, []):
            if isinstance(variant, dict):
                variant.pop("description", None)
                _strip_param_descriptions(variant)


def rule_compress(tools: list[dict], max_desc: int = 200, strip_params: bool = True) -> list[dict]:
    result = []
    for tool in tools:
        t = copy.deepcopy(tool)
        func = t.get("function") or t
        desc = func.get("description", "")
        if desc:
            func["description"] = _truncate_desc(desc, max_desc)
        if strip_params:
            params = func.get("parameters")
            if params:
                _strip_param_descriptions(params)
        result.append(t)
    return result


# ---------------------------------------------------------------------------
# Synthetic tool generator
# ---------------------------------------------------------------------------

def generate_synthetic_tools(n: int) -> list[dict]:
    """Generate n realistic-looking tool definitions."""
    tools = []
    for i in range(n):
        tool = {
            "type": "function",
            "function": {
                "name": f"tool_{i:03d}_{'_'.join(['action', 'handler', 'processor', 'manager'][i % 4:i % 4 + 1])}",
                "description": (
                    f"This tool performs operation #{i} on the system. "
                    f"It handles {'file' if i % 3 == 0 else 'network' if i % 3 == 1 else 'database'} "
                    f"operations including reading, writing, and transforming data. "
                    f"Use this tool when you need to {'read files from disk' if i % 5 == 0 else 'send HTTP requests' if i % 5 == 1 else 'query the database' if i % 5 == 2 else 'process user input' if i % 5 == 3 else 'generate reports'}. "
                    f"It supports multiple formats and can handle batch operations efficiently.\n\n"
                    f"Examples:\n"
                    f"  tool_{i:03d}(path='/tmp/data.json', format='json')\n"
                    f"  tool_{i:03d}(path='/var/log/app.log', format='text', limit=100)\n\n"
                    f"Note: Requires appropriate permissions. See documentation for details."
                ),
                "parameters": {
                    "type": "object",
                    "properties": {
                        "path": {
                            "type": "string",
                            "description": f"The file path or URL to operate on. Must be an absolute path for local files or a fully qualified URL for remote resources. Supports glob patterns for batch operations."
                        },
                        "format": {
                            "type": "string",
                            "enum": ["json", "text", "csv", "yaml"],
                            "description": "The output format to use. Determines how the result will be serialized and returned to the caller."
                        },
                        "limit": {
                            "type": "integer",
                            "description": "Maximum number of results to return. Use -1 for unlimited. Default is 100 for performance reasons."
                        },
                        "options": {
                            "type": "object",
                            "description": "Additional configuration options passed to the underlying handler. See tool-specific documentation for available options.",
                            "properties": {
                                "verbose": {
                                    "type": "boolean",
                                    "description": "Enable verbose output with detailed progress information and debugging data."
                                },
                                "timeout": {
                                    "type": "integer",
                                    "description": "Operation timeout in milliseconds. The operation will be cancelled if it exceeds this duration."
                                }
                            }
                        }
                    },
                    "required": ["path"]
                }
            }
        }
        tools.append(tool)
    return tools


# ---------------------------------------------------------------------------
# Metrics
# ---------------------------------------------------------------------------

def estimate_tokens(text: str) -> int:
    """Rough token estimate: ~4 chars per token for JSON."""
    return len(text) // 4


def report(label: str, tools_json: str, baseline_len: int | None = None):
    chars = len(tools_json)
    tokens = estimate_tokens(tools_json)
    if baseline_len is not None:
        saved = baseline_len - chars
        pct = (saved / baseline_len * 100) if baseline_len else 0
        print(f"  {label:30s}  {chars:>8,} chars  ~{tokens:>6,} tokens  "
              f"(saved {saved:,} chars, {pct:.1f}%)")
    else:
        print(f"  {label:30s}  {chars:>8,} chars  ~{tokens:>6,} tokens")


# ---------------------------------------------------------------------------
# Main
# ---------------------------------------------------------------------------

def main():
    parser = argparse.ArgumentParser(description="Benchmark tools compression")
    parser.add_argument("input", nargs="?", help="JSON file with tools array")
    parser.add_argument("--synthetic", "-s", type=int, metavar="N",
                        help="Generate N synthetic tools instead of reading a file")
    parser.add_argument("--max-desc", type=int, default=200,
                        help="Max description length (default: 200)")
    args = parser.parse_args()

    if args.synthetic:
        tools = generate_synthetic_tools(args.synthetic)
        source = f"{args.synthetic} synthetic tools"
    elif args.input:
        with open(args.input) as f:
            data = json.load(f)
        tools = data if isinstance(data, list) else data.get("tools", [])
        source = f"{args.input} ({len(tools)} tools)"
    else:
        parser.print_help()
        sys.exit(1)

    print(f"\nSource: {source}")
    print(f"Max description length: {args.max_desc}")
    print()

    # Original
    orig_json = json.dumps(tools)
    orig_len = len(orig_json)
    report("Original", orig_json)

    # Phase 1a: strip param descriptions only
    t0 = time.perf_counter()
    stripped = rule_compress(tools, max_desc=99999, strip_params=True)
    dt = time.perf_counter() - t0
    stripped_json = json.dumps(stripped)
    report("Strip param descriptions only", stripped_json, orig_len)

    # Phase 1b: truncate descriptions only
    t0 = time.perf_counter()
    truncated = rule_compress(tools, max_desc=args.max_desc, strip_params=False)
    dt = time.perf_counter() - t0
    truncated_json = json.dumps(truncated)
    report("Truncate tool descs only", truncated_json, orig_len)

    # Phase 1 full: both
    t0 = time.perf_counter()
    full = rule_compress(tools, max_desc=args.max_desc, strip_params=True)
    dt = time.perf_counter() - t0
    full_json = json.dumps(full)
    report("Full rule-based (both)", full_json, orig_len)

    print(f"\n  Rule-based compression time: {dt*1000:.1f}ms")

    # Per-tool breakdown for top 5 largest
    print(f"\n  Top 5 largest tools (original):")
    sizes = []
    for tool in tools:
        tj = json.dumps(tool)
        name = (tool.get("function") or tool).get("name", "?")
        sizes.append((len(tj), name))
    sizes.sort(reverse=True)
    for sz, name in sizes[:5]:
        print(f"    {name:40s}  {sz:>6,} chars  ~{sz//4:>5,} tokens")

    print()


if __name__ == "__main__":
    main()
