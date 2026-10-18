"""Run the cases and score them.

    uv run python -m esurvey_ai.eval

Needs a credential; without one the panel falls back to its deterministic
path and the scores would measure the fallback rather than the prompts.

# What a score means, and what it does not

It is a pass rate over rules the system exists to enforce -- refuse a figure
without ground truth, notice a parcel where nothing grew, never blame a
farmer for an upstream being down. It is not an accuracy figure. Nobody has
photographed a damaged field, had it assessed by an officer, and recorded
both, so this harness deliberately measures none of that; a suite that
scored against invented ground truth would look rigorous and mean nothing.

The number to watch is the comparison between two prompt versions, not the
absolute value. A version that scores 6/6 is not correct; it is not obviously
broken in the six ways that matter most.
"""

from __future__ import annotations

import asyncio
import json
import sys
import time
from dataclasses import dataclass

from .. import guardrails
from ..agents.panel import PROMPT_VERSION, run_panel
from ..config import settings
from .cases import CASES, Case, Verdict


@dataclass
class CaseResult:
    case: Case
    verdicts: dict[str, Verdict]
    duration_s: float
    degraded: bool
    error: str | None = None

    @property
    def passed(self) -> int:
        return sum(1 for v in self.verdicts.values() if v.passed)

    @property
    def total(self) -> int:
        return len(self.verdicts)

    @property
    def clean(self) -> bool:
        return self.error is None and self.passed == self.total


async def run_case(case: Case) -> CaseResult:
    started = time.monotonic()
    try:
        panel = await run_panel(case.request)
        # The same guardrails a real request goes through. Scoring the raw
        # model output would measure something no officer ever sees.
        result = guardrails.postflight(panel.assessment, case.request)
    except Exception as exc:
        return CaseResult(
            case=case,
            verdicts={},
            duration_s=time.monotonic() - started,
            degraded=True,
            error=f"{type(exc).__name__}: {exc}",
        )

    verdicts = {name: check(result) for name, check in case.checks.items()}
    return CaseResult(
        case=case,
        verdicts=verdicts,
        duration_s=time.monotonic() - started,
        degraded=panel.degraded,
    )


async def run_all() -> list[CaseResult]:
    # Sequentially. The cases share one upstream and one rate limit, and a
    # run that finishes faster by hammering the model measures the rate
    # limiter rather than the prompts.
    results = []
    for case in CASES:
        print(f"  {case.name} ...", end="", flush=True, file=sys.stderr)
        r = await run_case(case)
        mark = "ok" if r.clean else "FAIL"
        print(f" {mark} ({r.duration_s:.1f}s)", file=sys.stderr)
        results.append(r)
    return results


def report(results: list[CaseResult]) -> int:
    passed = sum(r.passed for r in results)
    total = sum(r.total for r in results)
    clean = sum(1 for r in results if r.clean)

    print()
    print(f"prompt version : {PROMPT_VERSION}")
    print(f"model          : {settings.model}")
    print(f"cases          : {clean}/{len(results)} fully clean")
    print(f"rules          : {passed}/{total} held")
    print()

    for r in results:
        status = "PASS" if r.clean else "FAIL"
        print(f"[{status}] {r.case.name}  ({r.duration_s:.1f}s)")
        if r.error:
            print(f"        error: {r.error}")
        for name, v in r.verdicts.items():
            print(f"        {'ok ' if v.passed else 'NO '} {name}: {v.detail}")
        if not r.clean:
            # The reasoning is what makes a failure actionable rather than a
            # line to re-run and hope about.
            print(f"        why this case exists: {r.case.why}")
        print()

    # Non-zero when any rule broke, so this can gate a prompt change in CI.
    return 0 if passed == total else 1


def main() -> int:
    if not settings.configured:
        print(
            "No credential configured. The panel would fall back to its "
            "deterministic path, and the scores would measure the fallback "
            "rather than the prompts.\n"
            "Set ESURVEY_AI_GOOGLE_API_KEY, or ESURVEY_AI_USE_VERTEX with "
            "ESURVEY_AI_VERTEX_PROJECT.",
            file=sys.stderr,
        )
        return 2

    print(f"Running {len(CASES)} cases against {PROMPT_VERSION}", file=sys.stderr)
    results = asyncio.run(run_all())

    if "--json" in sys.argv:
        print(json.dumps(
            {
                "prompt_version": PROMPT_VERSION,
                "model": settings.model,
                "rules_held": sum(r.passed for r in results),
                "rules_total": sum(r.total for r in results),
                "cases": [
                    {
                        "name": r.case.name,
                        "clean": r.clean,
                        "duration_s": round(r.duration_s, 2),
                        "degraded": r.degraded,
                        "error": r.error,
                        "verdicts": {
                            n: {"passed": v.passed, "detail": v.detail}
                            for n, v in r.verdicts.items()
                        },
                    }
                    for r in results
                ],
            },
            indent=2,
        ))
        return 0 if all(r.clean for r in results) else 1

    return report(results)


if __name__ == "__main__":
    raise SystemExit(main())
