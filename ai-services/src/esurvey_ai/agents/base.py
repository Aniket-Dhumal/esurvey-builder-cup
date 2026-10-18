"""Running one specialist.

# Why orchestration is explicit rather than ADK agent transfer

ADK can hand control between agents and let a coordinator decide who runs.
That is the wrong shape here. This panel has to be *reliable*: every specialist
runs, every one is timed, and one that fails must not take the assessment with
it. Explicit orchestration gives per-agent timeouts, per-agent degradation and
a deterministic order of operations; agent transfer gives the model discretion
over control flow, which is discretion this system should not have.

The agents are still ADK ``LlmAgent``s. What is not delegated is *whether they
run*.
"""

from __future__ import annotations

import asyncio
import base64
import contextlib
import json
import logging
import os
import re
import time
from typing import Any, TypeVar

from google.genai import types as genai_types
from pydantic import BaseModel, ValidationError

from ..config import settings
from ..failures import describe
from ..schemas import Image
from .findings import SpecialistReport

log = logging.getLogger(__name__)

_DATA_URI = re.compile(r"^data:(?P<mime>[\w/+.-]+);base64,(?P<payload>.+)$", re.S)

F = TypeVar("F", bound=BaseModel)


class PanelUnavailableError(RuntimeError):
    """No credential path is configured, so no specialist can run."""


def export_sdk_credentials() -> None:
    """Publish our prefixed settings under the names google-genai expects."""
    if settings.use_vertex:
        os.environ.setdefault("GOOGLE_GENAI_USE_VERTEXAI", "true")
        if settings.vertex_project:
            os.environ.setdefault("GOOGLE_CLOUD_PROJECT", settings.vertex_project)
        if settings.vertex_location:
            os.environ.setdefault("GOOGLE_CLOUD_LOCATION", settings.vertex_location)
    elif settings.google_api_key:
        os.environ.setdefault("GOOGLE_API_KEY", settings.google_api_key)


def decode_image(img: Image) -> genai_types.Part | None:
    """data:image/png;base64,... -> an inline Part."""
    m = _DATA_URI.match(img.data_uri.strip())
    if not m:
        log.warning("skipping image %s: not a data URI", img.kind)
        return None
    try:
        raw = base64.b64decode(m.group("payload"), validate=True)
    except (ValueError, TypeError) as exc:
        log.warning("skipping image %s: bad base64 (%s)", img.kind, exc)
        return None
    return genai_types.Part.from_bytes(data=raw, mime_type=m.group("mime"))


class Specialist:
    """One narrow agent with one job and one output schema."""

    def __init__(
        self,
        name: str,
        instruction: str,
        output_schema: type[BaseModel],
        *,
        tools: list[Any] | None = None,
        timeout_s: float = 90.0,
    ) -> None:
        self.name = name
        self.instruction = instruction
        self.output_schema = output_schema
        self.tools = tools or []
        self.timeout_s = timeout_s
        self._runner: Any = None

    def _ensure(self) -> None:
        if self._runner is not None:
            return
        if not settings.configured:
            raise PanelUnavailableError(
                "No Gemini credential configured. Set ESURVEY_AI_GOOGLE_API_KEY, "
                "or ESURVEY_AI_USE_VERTEX with ESURVEY_AI_VERTEX_PROJECT."
            )

        export_sdk_credentials()

        # Imported lazily so the service starts and answers /healthz even when
        # ADK is absent. A hard import at module scope turns a missing
        # credential into a crash loop.
        from google.adk.agents import LlmAgent
        from google.adk.runners import InMemoryRunner

        kwargs: dict[str, Any] = {
            "name": self.name,
            "model": settings.model,
            "instruction": self.instruction,
            "generate_content_config": genai_types.GenerateContentConfig(
                temperature=settings.temperature,
                max_output_tokens=settings.max_output_tokens,
            ),
        }

        # ADK will not accept output_schema alongside tools: a schema-locked
        # response has no room for a tool call. The specialists that fetch
        # their own evidence therefore return JSON by instruction and are
        # parsed here, which is why parse_finding is tolerant of a fenced
        # block.
        if self.tools:
            kwargs["tools"] = self.tools
        else:
            kwargs["output_schema"] = self.output_schema

        agent = LlmAgent(**kwargs)
        self._runner = InMemoryRunner(agent=agent, app_name="esurvey")

    async def run(
        self, prompt: str, images: list[Image] | None = None
    ) -> SpecialistReport:
        """Run this specialist, and never raise.

        A specialist that fails returns a report saying so. The panel needs
        every slot filled — "the weather source was unreachable" is a fact the
        synthesiser must weigh, and an exception here would instead lose the
        other three specialists' work.
        """
        started = time.monotonic()

        try:
            payload = await asyncio.wait_for(
                self._invoke(prompt, images or []), timeout=self.timeout_s
            )
            finding = self.parse_finding(payload)
            return SpecialistReport(
                agent=self.name,
                ran=True,
                duration_ms=int((time.monotonic() - started) * 1000),
                finding=finding,
            )
        except TimeoutError:
            log.warning("%s timed out after %.0fs", self.name, self.timeout_s)
            return SpecialistReport(
                agent=self.name,
                ran=False,
                duration_ms=int((time.monotonic() - started) * 1000),
                error=f"timed out after {self.timeout_s:.0f}s",
            )
        except Exception as exc:
            # The full exception goes to the log and nowhere else. What is
            # recorded travels into the assessment's reasoning, which is
            # printed and handed to farmers -- and str(exc) on a provider
            # error is the provider's whole response body. See failures.py.
            log.exception("%s failed", self.name)
            return SpecialistReport(
                agent=self.name,
                ran=False,
                duration_ms=int((time.monotonic() - started) * 1000),
                error=describe(exc),
            )

    async def _invoke(self, prompt: str, images: list[Image]) -> str:
        self._ensure()

        parts: list[genai_types.Part] = [genai_types.Part.from_text(text=prompt)]
        for img in images:
            part = decode_image(img)
            if part is not None:
                parts.append(part)

        session = await self._runner.session_service.create_session(
            app_name="esurvey", user_id="panel"
        )

        # ADK streams events, and the final response may itself arrive split
        # across parts — accumulate rather than keeping the last, which
        # truncates the JSON.
        final_chunks: list[str] = []
        all_chunks: list[str] = []

        async for event in self._runner.run_async(
            user_id="panel",
            session_id=session.id,
            new_message=genai_types.Content(role="user", parts=parts),
        ):
            if not (event.content and event.content.parts):
                continue
            is_final = False
            with contextlib.suppress(AttributeError):
                is_final = bool(event.is_final_response())
            for p in event.content.parts:
                text = getattr(p, "text", None)
                if not text:
                    continue
                all_chunks.append(text)
                if is_final:
                    final_chunks.append(text)

        payload = "".join(final_chunks) or "".join(all_chunks)
        if not payload.strip():
            raise RuntimeError("the model returned no content")
        return payload

    def parse_finding(self, payload: str) -> BaseModel:
        """Parse the specialist's JSON, tolerating a fenced block around it."""
        text = payload.strip()

        # A tool-using agent has no schema lock, so it sometimes wraps its JSON
        # in a code fence or prefaces it with a sentence.
        if "```" in text:
            fenced = re.search(r"```(?:json)?\s*(.+?)```", text, re.S)
            if fenced:
                text = fenced.group(1).strip()

        try:
            return self.output_schema.model_validate_json(text)
        except ValidationError:
            # One salvage attempt on the outermost object, then give up
            # honestly rather than inventing a finding.
            start, end = text.find("{"), text.rfind("}")
            if start >= 0 and end > start:
                return self.output_schema.model_validate_json(text[start : end + 1])
            raise


def json_block(value: Any) -> str:
    """Compact JSON for a prompt, without ASCII-escaping Devanagari."""
    return json.dumps(value, ensure_ascii=False, separators=(",", ": "), default=str)
