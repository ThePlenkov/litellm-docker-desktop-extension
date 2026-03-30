"""
LiteLLM Prompt Compressor Callback

Summarizes old conversation history when context grows beyond a configurable
threshold.  Keeps the system prompt and the most recent messages intact, and
replaces everything in between with a dense summary produced by a cheap/fast
model.

Configuration — add to config.yaml:
  callback_settings:
    prompt_compressor:
      model: gpt-4o-mini       # model for summarisation (default gpt-4o-mini)
      threshold: 0.6           # fraction of context window (default 0.6 = 60%)
      min_tokens: 10000        # absolute token floor — skip if below (default 10000)
      min_messages: 10         # minimum non-system messages before triggering (default 10)
      keep_recent: 5           # recent messages to preserve (default 5)
      summary_ratio: 0.7       # summary target as fraction of old messages tokens (default 0.7)

The callback is safe:
 - Recursive calls (the summarizer itself) are detected and skipped.
 - Tool-call / tool-response chains are never split.
 - Thinking/reasoning tokens (Claude thinking_blocks, reasoning_content) are
   included in the token budget estimate but stripped before summarization —
   the model's internal scratchpad is not useful context.
 - Any failure falls back to the original, unmodified messages.
"""

import logging
from typing import Any, List, Literal, Optional, Union

import litellm
from litellm.integrations.custom_logger import CustomLogger
from litellm.proxy._types import UserAPIKeyAuth
from litellm.caching.dual_cache import DualCache

logger = logging.getLogger("prompt_compressor")

# Optional OTEL — instrument if available, otherwise no-op.
try:
    from opentelemetry import trace
    _tracer = trace.get_tracer("prompt_compressor")
except ImportError:
    _tracer = None


# ---------------------------------------------------------------------------
# Settings (lazy-loaded from callback_settings in config.yaml)
# ---------------------------------------------------------------------------

_DEFAULTS = {
    "model": "gpt-4o-mini",
    "threshold": 0.6,
    "min_tokens": 10_000,
    "min_messages": 10,
    "keep_recent": 5,
    "summary_ratio": 0.7,
}


def _get_settings() -> dict | None:
    """Read from litellm.callback_settings (populated from config.yaml).

    Returns None if ``callback_settings.prompt_compressor`` is absent,
    meaning the callback is listed but not configured — treated as a no-op.
    """
    raw = getattr(litellm, "callback_settings", {}).get("prompt_compressor")
    if raw is None:
        return None
    return {
        "model": str(raw.get("model", _DEFAULTS["model"])),
        "threshold": float(raw.get("threshold", _DEFAULTS["threshold"])),
        "min_tokens": int(raw.get("min_tokens", _DEFAULTS["min_tokens"])),
        "min_messages": int(raw.get("min_messages", _DEFAULTS["min_messages"])),
        "keep_recent": int(raw.get("keep_recent", _DEFAULTS["keep_recent"])),
        "summary_ratio": float(raw.get("summary_ratio", _DEFAULTS["summary_ratio"])),
    }


# ---------------------------------------------------------------------------
# Helpers
# ---------------------------------------------------------------------------

def _log(msg: str) -> None:
    """Print to stdout so it appears in Docker container logs."""
    print(f"[prompt_compressor] {msg}", flush=True)


def _estimate_thinking_tokens(messages: List[dict]) -> int:
    """Estimate token count for thinking/reasoning content that
    ``litellm.token_counter`` silently skips.

    LiteLLM's ``_count_messages`` counts string values but skips list-of-dict
    values like ``thinking_blocks``.  ``reasoning_content`` (str) *is* counted
    by the standard counter, so we only need to handle ``thinking_blocks`` here.
    """
    extra = 0
    for msg in messages:
        blocks = msg.get("thinking_blocks")
        if isinstance(blocks, list):
            for block in blocks:
                text = block.get("thinking", "")
                if text:
                    # ~4 chars per token is a safe rough estimate.
                    extra += len(text) // 4
    return extra


def _strip_reasoning(msg: dict) -> dict:
    """Return a shallow copy of *msg* without reasoning/thinking fields.

    These fields contain the model's internal scratchpad — not useful for
    summarisation and extremely expensive token-wise.
    """
    skip = {"reasoning_content", "thinking_blocks"}
    if not any(k in msg for k in skip):
        return msg
    return {k: v for k, v in msg.items() if k not in skip}


def _find_safe_split(messages: List[dict], keep_recent: int) -> int:
    """Return the index at which to split *non-system* messages.

    The split never breaks an assistant-tool_calls / tool-response group.
    Returns 0 if no safe split exists (meaning: nothing to summarize).
    """
    n = len(messages)
    if n <= keep_recent:
        return 0

    # Build groups of indices that must stay together.
    groups: list[list[int]] = []
    i = 0
    while i < n:
        group = [i]
        msg = messages[i]
        # An assistant message with tool_calls must be kept with all following
        # tool-role responses that belong to it.
        if msg.get("role") == "assistant" and msg.get("tool_calls"):
            j = i + 1
            while j < n and messages[j].get("role") == "tool":
                group.append(j)
                j += 1
            i = j
        else:
            i += 1
        groups.append(group)

    # Walk backwards through groups, accumulating messages until we have at
    # least `keep_recent`.
    kept = 0
    split_group_idx = len(groups)
    for g in range(len(groups) - 1, -1, -1):
        kept += len(groups[g])
        split_group_idx = g
        if kept >= keep_recent:
            break

    if split_group_idx <= 0:
        return 0  # nothing to summarize

    # The split index is the first message index of the split group.
    return groups[split_group_idx][0]


def _message_to_text(msg: dict) -> str:
    """Convert a single message to a plain-text line for summarization."""
    role = msg.get("role", "unknown")
    content = msg.get("content") or ""

    # Multi-modal: extract text parts only.
    if isinstance(content, list):
        content = " ".join(
            p.get("text", "")
            for p in content
            if isinstance(p, dict) and p.get("type") == "text"
        )

    # Tool calls: note the function names.
    if role == "assistant" and msg.get("tool_calls"):
        names = ", ".join(
            tc.get("function", {}).get("name", "?")
            for tc in msg["tool_calls"]
        )
        content = f"[called tools: {names}] {content}".strip()

    if role == "tool":
        name = msg.get("name", "tool")
        content = f"[tool result: {name}] {content}"

    # Truncate very long individual messages to save summarizer tokens.
    if len(content) > 2000:
        content = content[:800] + "\n...(truncated)...\n" + content[-800:]

    return f"{role}: {content}"


# ---------------------------------------------------------------------------
# Callback
# ---------------------------------------------------------------------------

class PromptCompressor(CustomLogger):
    """LiteLLM pre-call callback that compresses long conversations."""

    def __init__(self, **kwargs):
        super().__init__(**kwargs)
        _log("loaded (reads config from callback_settings)")

    # -- Pre-call hook -------------------------------------------------------

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

        # Recursion guard: skip our own summarisation requests.
        metadata = data.get("metadata") or {}
        if metadata.get("_prompt_compressor_internal"):
            return data

        messages: list = data.get("messages", [])
        if not messages:
            return data

        cfg = _get_settings()
        if cfg is None:
            return data  # not configured — no-op

        # Separate system and non-system messages.
        system_msgs = [m for m in messages if m.get("role") == "system"]
        non_system = [m for m in messages if m.get("role") != "system"]

        if len(non_system) < cfg["min_messages"]:
            return data  # not enough messages yet

        # Check token count against the model's context window.
        # litellm.token_counter skips thinking_blocks (list-of-dict), so we
        # add an estimate for those separately.
        model = data.get("model", "")
        try:
            token_count = litellm.token_counter(model=model, messages=messages)
            token_count += _estimate_thinking_tokens(messages)
        except Exception:
            return data

        try:
            model_info = litellm.get_model_info(model=model)
            max_input = (
                model_info.get("max_input_tokens")
                or model_info.get("max_tokens")
                or 128_000
            )
        except Exception:
            # Unknown model — fall back to a generous default context.
            max_input = 128_000

        if token_count < cfg["min_tokens"]:
            return data  # below absolute token floor

        if token_count < max_input * cfg["threshold"]:
            return data  # still within comfortable range

        # Find the split point.
        split_idx = _find_safe_split(non_system, cfg["keep_recent"])
        if split_idx == 0:
            return data  # nothing to summarise

        old_msgs = non_system[:split_idx]
        recent_msgs = non_system[split_idx:]

        # Strip reasoning/thinking content from old messages — it's the
        # model's internal scratchpad, not useful for summarisation and
        # can be the majority of the token cost.
        old_msgs_clean = [_strip_reasoning(m) for m in old_msgs]

        # Count old message tokens to compute proportional summary budget.
        # Include thinking tokens in the budget since they were part of the
        # context we're eliminating.
        old_tokens = litellm.token_counter(model=model, messages=old_msgs)
        old_tokens += _estimate_thinking_tokens(old_msgs)
        max_summary_tokens = max(100, int(old_tokens * cfg["summary_ratio"]))

        # -- OTEL span wrapping the actual compression --
        span = _tracer.start_span("prompt_compressor") if _tracer else None
        try:
            if span:
                span.set_attribute("prompt_compressor.model", model)
                span.set_attribute("prompt_compressor.total_tokens", token_count)
                span.set_attribute("prompt_compressor.max_input_tokens", max_input)
                span.set_attribute("prompt_compressor.threshold", cfg["threshold"])
                span.set_attribute("prompt_compressor.total_messages", len(messages))
                span.set_attribute("prompt_compressor.old_messages", len(old_msgs))
                span.set_attribute("prompt_compressor.old_tokens", old_tokens)
                span.set_attribute("prompt_compressor.recent_messages", len(recent_msgs))
                span.set_attribute("prompt_compressor.summary_budget_tokens", max_summary_tokens)

            # Summarise old messages (using cleaned versions without reasoning).
            try:
                summary = await self._summarize(old_msgs_clean, cfg, max_summary_tokens)
            except Exception as exc:
                _log(f"summarization failed, sending original: {exc}")
                if span:
                    span.set_attribute("prompt_compressor.error", str(exc))
                return data

            summary_tokens = litellm.token_counter(
                model=model,
                text=summary,
            )
            tokens_saved = old_tokens - summary_tokens

            _log(
                f"compressed {len(old_msgs)} old msgs (~{old_tokens} tok) "
                f"-> summary (~{summary_tokens} tok, saved ~{tokens_saved})"
            )

            if span:
                span.set_attribute("prompt_compressor.summary_tokens", summary_tokens)
                span.set_attribute("prompt_compressor.tokens_saved", tokens_saved)
                span.set_attribute(
                    "prompt_compressor.compression_ratio",
                    round(summary_tokens / old_tokens, 3) if old_tokens else 0,
                )

            data["messages"] = (
                system_msgs
                + [
                    {
                        "role": "user",
                        "content": (
                            "[Earlier conversation summary — refer to this for context]\n"
                            + summary
                        ),
                    }
                ]
                + recent_msgs
            )
            return data
        finally:
            if span:
                span.end()

    # -- Summariser ----------------------------------------------------------

    async def _summarize(self, messages: List[dict], cfg: dict, max_tokens: int) -> str:
        conversation = "\n".join(_message_to_text(m) for m in messages)

        # Truncate the conversation input itself if extremely long.
        max_chars = 30_000
        if len(conversation) > max_chars:
            conversation = (
                conversation[: max_chars // 2]
                + "\n...(middle truncated)...\n"
                + conversation[-max_chars // 2 :]
            )

        summarize_msgs = [
            {
                "role": "system",
                "content": (
                    "Summarize the following conversation history. Be dense "
                    "and factual. Preserve:\n"
                    "- Key decisions and conclusions\n"
                    "- Code snippets, file paths, and commands\n"
                    "- Error messages and their resolutions\n"
                    "- Open action items and pending questions\n"
                    "Drop greetings, acknowledgements, and redundant context."
                ),
            },
            {"role": "user", "content": conversation},
        ]

        kwargs = dict(
            model=cfg["model"],
            messages=summarize_msgs,
            max_tokens=max_tokens,
            metadata={"_prompt_compressor_internal": True},
        )

        # Use the proxy's router if available (has api keys and routing).
        # Fall back to direct litellm.acompletion for standalone usage.
        try:
            from litellm.proxy.proxy_server import llm_router
        except ImportError:
            llm_router = None

        if llm_router is not None:
            resp = await llm_router.acompletion(**kwargs)
        else:
            resp = await litellm.acompletion(**kwargs)

        return resp.choices[0].message.content


# LiteLLM discovers this variable when loading the callback module.
proxy_handler_instance = PromptCompressor()
