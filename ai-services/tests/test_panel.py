"""Panel behaviour when things are missing or broken.

A multi-agent system is more parts and therefore more ways to fail. What is
tested here is not that the panel works when everything is healthy -- that
needs a live model -- but that every *failure* has a defined outcome, because
that is the property the assessment actually rests on.

The offline workflow is the same code path: a report filed without a network
syncs later and is assessed then, against whatever upstreams happen to be
reachable at that moment. So "the weather source is down" and "there is no
credential" are not edge cases here. They are Tuesday.
"""

from __future__ import annotations

from datetime import date, timedelta

import httpx
import pytest

from esurvey_ai.agents import panel as panel_mod
from esurvey_ai.agents.base import PanelUnavailableError
from esurvey_ai.agents.findings import SatelliteFinding, SpecialistReport
from esurvey_ai.agents.panel import run_panel
from esurvey_ai.schemas import Crop, CropPresence, ModelAssessment
from esurvey_ai.tools import weather as weather_mod

from .test_guardrails import ground_photo, make_request

HEALTHY = [("2025-08-01", 0.24), ("2025-09-10", 0.71), ("2025-10-11", 0.28)]
BARE = [("2025-08-01", 0.09), ("2025-09-10", 0.12), ("2025-10-11", 0.10)]

SPECIALISTS = ("weather", "phenology", "vision", "satellite")


def with_position(req):
    """Give the parcel a centroid, as the Go side now does."""
    req.parcel.centroid_lat = 17.655
    req.parcel.centroid_lon = 75.905
    return req


def stub(monkeypatch, name, fn):
    monkeypatch.setattr(getattr(panel_mod, f"{name}_specialist"), "run", fn)


class TestNoCredential:
    """The floor: no model at all."""

    @pytest.fixture(autouse=True)
    def _no_credential(self, monkeypatch):
        async def unavailable(*a, **k):
            raise PanelUnavailableError("no credential in this test")

        for name in SPECIALISTS:
            stub(monkeypatch, name, unavailable)

    @pytest.mark.asyncio
    async def test_falls_back_rather_than_failing(self):
        result = await run_panel(
            with_position(make_request(points=HEALTHY, images=[ground_photo()]))
        )

        assert result.degraded
        assert result.degraded_reason
        assert result.assessment.model == "deterministic-fallback"

    @pytest.mark.asyncio
    async def test_states_no_damage_figure(self):
        """Nothing in the fallback has looked at a photograph."""
        result = await run_panel(
            with_position(make_request(points=HEALTHY, images=[ground_photo()]))
        )

        assert result.assessment.damage_percent is None
        assert result.assessment.severity is None
        assert result.assessment.insufficient_evidence is True
        assert result.assessment.confidence == 0.0

    @pytest.mark.asyncio
    async def test_still_settles_crop_presence_from_the_series(self):
        """The one thing arithmetic can answer without a model."""
        result = await run_panel(with_position(make_request(points=HEALTHY)))

        assert result.assessment.crop_presence is CropPresence.CONFIRMED
        assert "0.71" in result.assessment.crop_presence_reasoning

    @pytest.mark.asyncio
    async def test_still_raises_the_strongest_fraud_signal(self):
        """A crop declared on land where nothing grew."""
        result = await run_panel(with_position(make_request(points=BARE)))

        assert result.assessment.crop_presence is CropPresence.ABSENT
        codes = [f.code for f in result.assessment.fraud_indicators]
        assert "no_vegetation_detected" in codes


class TestMissingEvidence:
    """A specialist with nothing to work on is not run, and that is recorded."""

    @pytest.fixture(autouse=True)
    def _never_reached(self, monkeypatch):
        async def boom(*a, **k):
            raise AssertionError("a specialist ran with no evidence")

        for name in SPECIALISTS:
            stub(monkeypatch, name, boom)

    @pytest.mark.asyncio
    async def test_every_gap_is_named(self):
        # No centroid, no crop, no photographs, no series: all four skipped.
        result = await run_panel(make_request())

        assert {r.agent for r in result.reports} == {
            "weather_specialist",
            "phenology_specialist",
            "vision_specialist",
            "satellite_specialist",
        }
        assert all(not r.ran for r in result.reports)
        # A skip without a reason is indistinguishable from a bug.
        assert all(r.error for r in result.reports)

    @pytest.mark.asyncio
    async def test_no_specialist_means_no_synthesis(self, monkeypatch):
        async def boom(*a, **k):
            raise AssertionError("the synthesiser ran with nothing to synthesise")

        monkeypatch.setattr(panel_mod.synthesiser, "run", boom)
        result = await run_panel(make_request())

        assert result.degraded
        assert result.assessment.crop_presence is CropPresence.INCONCLUSIVE


class TestOneSpecialistDown:
    """Three findings and a recorded gap beat losing the assessment."""

    @pytest.mark.asyncio
    async def test_a_failed_specialist_does_not_sink_the_panel(self, monkeypatch):
        async def failed(*a, **k):
            return SpecialistReport(
                agent="weather_specialist", ran=False, error="source unreachable"
            )

        def ok(agent):
            async def run(*a, **k):
                # The satellite carries a finding, because the panel reads
                # crop presence off it before deciding whether there is a
                # growth stage to place. A bare "it ran" says nothing about
                # whether a crop is there, and the phenology specialist is
                # rightly not asked in that case -- which is a different test
                # from this one.
                finding = (
                    SatelliteFinding(
                        verdict="supports",
                        reasoning="the curve rose and fell",
                        confidence=0.8,
                        crop_present=True,
                    )
                    if agent == "satellite_specialist"
                    else None
                )
                return SpecialistReport(
                    agent=agent, ran=True, duration_ms=1, finding=finding
                )

            return run

        stub(monkeypatch, "weather", failed)
        for name in ("phenology", "vision", "satellite"):
            stub(monkeypatch, name, ok(f"{name}_specialist"))

        async def synth_failed(*a, **k):
            return SpecialistReport(
                agent="synthesiser", ran=False, error="model overloaded"
            )

        monkeypatch.setattr(panel_mod.synthesiser, "run", synth_failed)

        req = with_position(
            make_request(
                points=HEALTHY,
                images=[ground_photo()],
                crop=Crop(name="Soybean", sowing_date="2025-06-20"),
            )
        )
        result = await run_panel(req)

        # All four slots are filled, one of them with a failure.
        assert len(result.reports) == 4
        assert sum(1 for r in result.reports if r.ran) == 3
        # And a failed synthesiser degrades rather than raises.
        assert result.degraded
        assert "model overloaded" in result.degraded_reason


class TestWeatherTool:
    """The one upstream the panel fetches for itself."""

    @pytest.mark.asyncio
    async def test_unreachable_source_returns_rather_than_raises(self, monkeypatch):
        """A tool that raises into a model produces a hallucinated recovery."""

        async def refuse(*a, **k):
            raise httpx.ConnectError("no route to host")

        monkeypatch.setattr(httpx.AsyncClient, "get", refuse)
        out = await weather_mod.get_recent_weather(17.655, 75.905, "2025-10-12")

        assert out["available"] is False
        assert "no route to host" in out["error"]

    @pytest.mark.asyncio
    async def test_a_bad_date_is_reported_not_guessed(self):
        out = await weather_mod.get_recent_weather(17.655, 75.905, "12-10-2025")

        assert out["available"] is False

    @pytest.mark.asyncio
    async def test_hail_is_surfaced_from_the_wmo_codes(self, monkeypatch):
        """96 and 99 are the only direct hail signal this source carries."""
        body = {
            "daily": {
                "time": ["2025-10-11", "2025-10-12"],
                "weather_code": [61, 99],
                "precipitation_sum": [4.2, 38.6],
                "rain_sum": [4.2, 38.6],
                "precipitation_hours": [3, 6],
                "wind_speed_10m_max": [12.0, 41.0],
                "wind_gusts_10m_max": [22.0, 78.4],
                "temperature_2m_max": [31.0, 27.5],
                "temperature_2m_min": [21.0, 19.8],
            }
        }

        async def served(*a, **k):
            return httpx.Response(
                200, json=body, request=httpx.Request("GET", "http://x")
            )

        monkeypatch.setattr(httpx.AsyncClient, "get", served)
        out = await weather_mod.get_recent_weather(17.655, 75.905, "2025-10-12")

        assert out["available"] is True
        assert out["hail_days"] == ["2025-10-12"]
        assert out["max_wind_gust_kmh"] == 78.4
        assert out["total_rain_mm"] == 42.8
        assert out["on_the_incident_day"]["hail_reported"] is True
        # The resolution caveat travels with the data, not in the prompt only.
        assert "village" in out["resolution_note"]


class TestSynthesiserReport:
    """The synthesiser runs through the same machinery as a specialist."""

    def test_its_output_is_an_admissible_finding(self):
        """A closed union here made every live run degrade silently.

        The panel only logged the validation error and fell back, so the
        service looked healthy and every assessment came back deterministic.
        """
        assessment = ModelAssessment(
            crop_presence=CropPresence.CONFIRMED,
            crop_presence_reasoning="the series peaks well above bare soil",
            reasoning="three specialists agreed",
            confidence=0.6,
            insufficient_evidence=True,
        )

        report = SpecialistReport(agent="synthesiser", ran=True, finding=assessment)

        assert isinstance(report.finding, ModelAssessment)
        assert report.finding.crop_presence is CropPresence.CONFIRMED


class TestWeatherSourceSelection:
    """Neither Open-Meteo endpoint covers the range this system needs."""

    @staticmethod
    def _served(recorder, *, refuse_first=False):
        body = {
            "daily": {
                "time": ["2025-10-11", "2025-10-12"],
                "weather_code": [61, 99],
                "precipitation_sum": [4.2, 38.6],
                "rain_sum": [4.2, 38.6],
                "precipitation_hours": [3, 6],
                "wind_speed_10m_max": [12.0, 41.0],
                "wind_gusts_10m_max": [22.0, 78.4],
                "temperature_2m_max": [31.0, 27.5],
                "temperature_2m_min": [21.0, 19.8],
            }
        }

        async def get(self, url, **kwargs):
            recorder.append(url)
            req = httpx.Request("GET", url)
            if refuse_first and len(recorder) == 1:
                return httpx.Response(
                    400,
                    json={
                        "error": True,
                        "reason": "Parameter 'start_date' is out of allowed range",
                    },
                    request=req,
                )
            return httpx.Response(200, json=body, request=req)

        return get

    @pytest.mark.asyncio
    async def test_an_old_incident_goes_to_the_archive_first(self, monkeypatch):
        """A report filed offline can sync long after the loss."""
        seen: list[str] = []
        monkeypatch.setattr(httpx.AsyncClient, "get", self._served(seen))

        old = (date.today() - timedelta(days=300)).isoformat()
        out = await weather_mod.get_recent_weather(17.655, 75.905, old)

        assert out["available"] is True
        assert "archive" in seen[0]
        assert out["source"].startswith("Open-Meteo archive")

    @pytest.mark.asyncio
    async def test_a_recent_incident_goes_to_the_forecast_first(self, monkeypatch):
        """The archive trails real time; a loss reported yesterday needs it not to."""
        seen: list[str] = []
        monkeypatch.setattr(httpx.AsyncClient, "get", self._served(seen))

        recent = (date.today() - timedelta(days=3)).isoformat()
        out = await weather_mod.get_recent_weather(17.655, 75.905, recent)

        assert out["available"] is True
        assert seen[0].endswith("/v1/forecast")

    @pytest.mark.asyncio
    async def test_a_refusal_falls_through_to_the_other_source(self, monkeypatch):
        seen: list[str] = []
        monkeypatch.setattr(
            httpx.AsyncClient, "get", self._served(seen, refuse_first=True)
        )

        recent = (date.today() - timedelta(days=3)).isoformat()
        out = await weather_mod.get_recent_weather(17.655, 75.905, recent)

        assert out["available"] is True
        assert len(seen) == 2
        assert "archive" in seen[1]

    @pytest.mark.asyncio
    async def test_both_refusing_reports_open_meteos_own_reason(self, monkeypatch):
        """"Could not reach Open-Meteo" for a range error misdirects everyone."""

        async def refuse(self, url, **kwargs):
            return httpx.Response(
                400,
                json={"error": True, "reason": "start_date is out of allowed range"},
                request=httpx.Request("GET", url),
            )

        monkeypatch.setattr(httpx.AsyncClient, "get", refuse)
        out = await weather_mod.get_recent_weather(17.655, 75.905, "2025-10-12")

        assert out["available"] is False
        assert "out of allowed range" in out["error"]
        # Both were tried before giving up.
        assert "forecast" in out["error"] and "archive" in out["error"]


class TestDegradedReason:
    """A degraded flag with no reason is a question, not an answer."""

    @pytest.mark.asyncio
    async def test_a_skipped_specialist_is_named(self, monkeypatch):
        def ok(agent):
            async def run(*a, **k):
                # The satellite carries a finding, because the panel reads
                # crop presence off it before deciding whether there is a
                # growth stage to place. A bare "it ran" says nothing about
                # whether a crop is there, and the phenology specialist is
                # rightly not asked in that case -- which is a different test
                # from this one.
                finding = (
                    SatelliteFinding(
                        verdict="supports",
                        reasoning="the curve rose and fell",
                        confidence=0.8,
                        crop_present=True,
                    )
                    if agent == "satellite_specialist"
                    else None
                )
                return SpecialistReport(
                    agent=agent, ran=True, duration_ms=1, finding=finding
                )

            return run

        for name in ("weather", "phenology", "satellite"):
            stub(monkeypatch, name, ok(f"{name}_specialist"))

        async def synthesised(*a, **k):
            return SpecialistReport(
                agent="synthesiser",
                ran=True,
                finding=ModelAssessment(
                    crop_presence=CropPresence.CONFIRMED,
                    crop_presence_reasoning="the curve is a crop",
                    reasoning="three of four reported",
                    confidence=0.4,
                    insufficient_evidence=True,
                ),
            )

        monkeypatch.setattr(panel_mod.synthesiser, "run", synthesised)

        # No photographs, so vision is the one member that cannot run.
        result = await run_panel(
            with_position(
                make_request(points=HEALTHY, crop=Crop(name="Soybean"))
            )
        )

        assert result.degraded
        assert "vision_specialist" in result.degraded_reason
        assert "no ground photographs" in result.degraded_reason
        # And it is not the deterministic floor -- the synthesiser did run.
        assert result.assessment.model != "deterministic-fallback"
