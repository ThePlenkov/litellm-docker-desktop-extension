"""
LiteLLM Tools Compressor Callback

Reduces token usage from tool definitions sent with every chat/completions
request.  Agent sessions with 50-100+ tools can spend 10-40K tokens per call
just on tool definitions — before any conversation starts.

Two compression phases:
  1. Rule-based (always, zero cost):
     - Strip ``description`` from every parameter recursively.
     - Remove examples, markdown fences, and trailing noise from tool descriptions.
     - Tool descriptions are NEVER truncated — truncation loses critical usage
       details and can cause incorrect tool calls.
  2. LLM-based (optional, cached):
     - Rewrite remaining long descriptions as concise one-liners.
     - Uses a cheap model; result is cached per unique tool set.

Configuration — add to config.yaml:
  callback_settings:
    tools_compressor:
      min_tools: 5             # minimum tool count to trigger (default 5)
      strip_param_desc: true   # strip parameter descriptions (default true)
      use_llm: false           # enable LLM phase 2 (default false)
      llm_model: gpt-4o-mini   # model for LLM rewriting (default gpt-4o-mini)
      llm_max_desc: 80         # max chars after LLM rewrite (default 80)

The callback is safe:
 - Recursive calls (the LLM compressor itself) are detected and skipped.
 - Compressed tool sets are cached by content hash; tools that do not change
   between requests are never reprocessed.
 - Any failure falls back to the original, unmodified tools.
"""

from __future__ import annotations

import copy
import hashlib
import json
import logging
import re
from typing import Any, Literal, Optional, Union

import litellm
from litellm.integrations.custom_logger import CustomLogger
from litellm.proxy._types import UserAPIKeyAuth
from litellm.caching.dual_cache import DualCache

logger = logging.getLogger("tools_compressor")

# Optional OTEL — instrument if available, otherwise no-op.
try:
    from opentelemetry import trace
    _tracer = trace.get_tracer("tools_compressor")
except ImportError:
    _tracer = None

# Max cache entries to prevent unbounded growth.
_MAX_CACHE = 8

# Regex: strips example blocks, markdown fences, and everything after them.
_EXAMPLE_RE = re.compile(
    r"(?:^|\n)\s*(?:examples?|e\.g\.|for example|usage|tip|note|```)"
    r"[^\n]*(?:\n|$).*",
    re.IGNORECASE | re.DOTALL,
)


# ---------------------------------------------------------------------------
# Settings (lazy-loaded from callback_settings in config.yaml)
# ---------------------------------------------------------------------------

_DEFAULTS = {
    "min_tools": 5,
    "strip_param_desc": True,
    "use_llm": False,
    "llm_model": "gpt-4o-mini",
    "llm_max_desc": 80,
}


def _get_settings() -> dict | None:
    """Read from litellm.callback_settings (populated from config.yaml).

    Returns None if ``callback_settings.tools_compressor`` is absent,
    meaning the callback is listed but not configured — treated as a no-op.
    """
    raw = getattr(litellm, "callback_settings", {}).get("tools_compressor")
    if raw is None:
        return None
    return {
        "min_tools": int(raw.get("min_tools", _DEFAULTS["min_tools"])),
        "strip_param_desc": _to_bool(raw.get("strip_param_desc", _DEFAULTS["strip_param_desc"])),
        "use_llm": _to_bool(raw.get("use_llm", _DEFAULTS["use_llm"])),
        "llm_model": str(raw.get("llm_model", _DEFAULTS["llm_model"])),
        "llm_max_desc": int(raw.get("llm_max_desc", _DEFAULTS["llm_max_desc"])),
    }


def _to_bool(v: Any) -> bool:
    if isinstance(v, bool):
        return v
    return str(v).lower() in ("true", "1", "yes")


def _log(msg: str) -> None:
    """Print to stdout so it appears in Docker container logs."""
    print(f"[tools_compressor] {msg}", flush=True)


# ---------------------------------------------------------------------------
# Callback
# ---------------------------------------------------------------------------

class ToolsCompressorHandler(CustomLogger):
    """Compresses tool definitions to save context-window tokens."""

    def __init__(self, **kwargs):
        super().__init__(**kwargs)
        self._cache: dict[str, list[dict]] = {}
        _log("loaded (reads config from callback_settings)")

    async def async_pre_call_hook(
        self,
        user_api_key_dict: UserAPIKeyAuth,
        cache: DualCache,
        data: dict,
        call_type: Literal[
            "completion",
            "text_completion",
            "embeddings",
            "image_generation",
            "moderation",
            "audio_transcription",
            "pass_through_endpoint",
            "rerank",
            "mcp_call",
            "anthropic_messages",
        ],
    ) -> Optional[Union[Exception, str, dict]]:
        if call_type not in ("completion", "acompletion"):
            return data

        tools = data.get("tools")
        if not tools:
            return data

        cfg = _get_settings()
        if cfg is None:
            return data  # not configured — no-op

        if len(tools) < cfg["min_tools"]:
            return data

        # Recursion guard — skip our own LLM compression calls.
        metadata = data.get("metadata") or {}
        if metadata.get("_tools_compressor_internal"):
            return data

        # Cache lookup by content hash.
        tools_key = _tools_hash(tools)
        cached = tools_key in self._cache

        span = _tracer.start_span("tools_compressor") if _tracer else None
        try:
            if span:
                span.set_attribute("tools_compressor.tool_count", len(tools))
                span.set_attribute("tools_compressor.cache_hit", cached)

            if cached:
                logger.debug("tools_compressor: cache hit (%d tools)", len(tools))
                data["tools"] = self._cache[tools_key]
                return data

            try:
                compressed = await self._compress(tools, cfg)
            except Exception as exc:
                logger.warning(
                    "tools_compressor: compression failed, using originals: %s", exc
                )
                if span:
                    span.set_attribute("tools_compressor.error", str(exc))
                return data

            # Log savings.
            orig_len = len(json.dumps(tools))
            comp_len = len(json.dumps(compressed))
            savings = orig_len - comp_len
            pct = (savings / orig_len * 100) if orig_len else 0
            _log(
                f"{len(tools)} tools, {orig_len} -> {comp_len} chars "
                f"(saved {savings}, {pct:.0f}%)"
            )

            if span:
                span.set_attribute("tools_compressor.original_chars", orig_len)
                span.set_attribute("tools_compressor.compressed_chars", comp_len)
                span.set_attribute("tools_compressor.chars_saved", savings)
                span.set_attribute("tools_compressor.savings_pct", round(pct, 1))
                span.set_attribute("tools_compressor.used_llm", cfg["use_llm"])

            # Cache (bounded).
            if len(self._cache) >= _MAX_CACHE:
                self._cache.pop(next(iter(self._cache)))
            self._cache[tools_key] = compressed

            data["tools"] = compressed
            return data
        finally:
            if span:
                span.end()

    async def _compress(self, tools: list[dict], cfg: dict) -> list[dict]:
        compressed = _rule_compress(tools, cfg)
        if cfg["use_llm"]:
            compressed = await _llm_compress(compressed, cfg)
        return compressed


# ---------------------------------------------------------------------------
# Phase 1 — Rule-based compression (zero cost, deterministic)
# ---------------------------------------------------------------------------

def _rule_compress(tools: list[dict], cfg: dict) -> list[dict]:
    """Strip parameter descriptions and clean noise from tool descriptions."""
    strip = cfg["strip_param_desc"]
    result = []
    for tool in tools:
        t = copy.deepcopy(tool)
        func = t.get("function") or t

        desc = func.get("description", "")
        if desc:
            # Only strip examples/noise — never truncate the description.
            func["description"] = _clean_desc(desc)

        if strip:
            params = func.get("parameters")
            if params:
                _strip_param_descriptions(params)

        result.append(t)
    return result


def _clean_desc(desc: str) -> str:
    """Remove examples, markdown fences, and trailing noise. Never truncates."""
    return _EXAMPLE_RE.sub("", desc).strip()


def _strip_param_descriptions(schema: dict) -> None:
    """Recursively remove ``description`` from parameter schemas."""
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


# ---------------------------------------------------------------------------
# Phase 2 — LLM-based description rewriting (optional, cached)
# ---------------------------------------------------------------------------

async def _llm_compress(tools: list[dict], cfg: dict) -> list[dict]:
    """Rewrite long tool descriptions as concise one-liners."""
    llm_max = cfg["llm_max_desc"]
    to_compress: list[tuple[int, str, str]] = []
    for i, tool in enumerate(tools):
        func = tool.get("function") or tool
        desc = func.get("description", "")
        if len(desc) > llm_max:
            to_compress.append((i, func.get("name", f"tool_{i}"), desc))

    if not to_compress:
        return tools

    lines = [f"[{idx}] {name}: {desc}" for idx, name, desc in to_compress]
    prompt = (
        f"Rewrite each tool description below to at most {llm_max} "
        "characters.  Keep the essential meaning — what the tool does and "
        "when to use it.  Return ONLY a JSON array: "
        '[{"i": <index>, "d": "<new description>"}]\n\n'
        + "\n".join(lines)
    )

    try:
        # Use the proxy's router if available (has api keys and routing).
        try:
            from litellm.proxy.proxy_server import llm_router
        except ImportError:
            llm_router = None

        kwargs = dict(
            model=cfg["llm_model"],
            messages=[{"role": "user", "content": prompt}],
            max_tokens=min(len(to_compress) * 60, 4000),
            temperature=0,
            metadata={"_tools_compressor_internal": True},
        )

        if llm_router is not None:
            resp = await llm_router.acompletion(**kwargs)
        else:
            resp = await litellm.acompletion(**kwargs)

        content = resp.choices[0].message.content or ""
        match = re.search(r"\[.*\]", content, re.DOTALL)
        if match:
            rewrites = json.loads(match.group())
            for rw in rewrites:
                idx = rw.get("i")
                new_desc = rw.get("d", "")
                if idx is not None and 0 <= idx < len(tools) and new_desc:
                    func = tools[idx].get("function") or tools[idx]
                    func["description"] = new_desc[:llm_max]
    except Exception as exc:
        logger.warning("tools_compressor: LLM rewrite failed: %s", exc)

    return tools


# ---------------------------------------------------------------------------
# Helpers
# ---------------------------------------------------------------------------

def _tools_hash(tools: list[dict]) -> str:
    raw = json.dumps(tools, sort_keys=True, separators=(",", ":"))
    return hashlib.sha256(raw.encode()).hexdigest()[:16]


# ---------------------------------------------------------------------------
# LiteLLM discovery
# ---------------------------------------------------------------------------

proxy_handler_instance = ToolsCompressorHandler()
