"""Runtime configuration, entirely from the environment."""

from __future__ import annotations

from pydantic_settings import BaseSettings, SettingsConfigDict


class Settings(BaseSettings):
    model_config = SettingsConfigDict(
        env_prefix="ESURVEY_AI_",
        env_file=".env",
        env_file_encoding="utf-8",
        extra="ignore",
    )

    # Gemini model id. Verify against the current Vertex AI / AI Studio model
    # list before deploying — Google's naming moves, and a stale id fails at
    # call time rather than at startup.
    model: str = "gemini-3.1-pro-preview"

    # Deterministic-leaning. This agent grades evidence for a payout decision;
    # run-to-run variance on identical inputs is a defect, not creativity.
    temperature: float = 0.1
    max_output_tokens: int = 8192

    google_api_key: str = ""
    use_vertex: bool = False
    vertex_project: str = ""
    vertex_location: str = "asia-south1"

    #: The shared token the platform API presents. Empty disables the
    #: check, which is what a developer laptop runs with --
    #: this service holds the Gemini credential and sees
    #: farmer photographs, so the platform API refuses to start in a
    #: hardened environment without one. See callers.py.
    caller_token: str = ""

    host: str = "127.0.0.1"
    port: int = 8100

    request_timeout_s: float = 120.0

    # Refuse rather than guess. Below this many cloud-free passes the agent is
    # told to return insufficient_evidence unless ground imagery is present.
    min_observations: int = 2

    log_prompts: bool = True

    @property
    def configured(self) -> bool:
        """Whether a Gemini credential path is present."""
        return bool(self.google_api_key) or (
            self.use_vertex and bool(self.vertex_project)
        )


settings = Settings()
