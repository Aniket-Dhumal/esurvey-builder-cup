"""Runtime configuration, entirely from the environment."""

from __future__ import annotations

from pydantic_settings import BaseSettings, SettingsConfigDict


class Settings(BaseSettings):
    model_config = SettingsConfigDict(
        env_prefix="ESURVEY_SAR_",
        env_file=".env",
        env_file_encoding="utf-8",
        extra="ignore",
    )

    # The GCP project registered for Earth Engine.
    ee_project: str = ""

    # Plausible backscatter without Earth Engine, for developing the
    # platform's side of the contract.
    #
    # Off by default and labelled SYNTHETIC in every result. A synthetic
    # reading that reached an officer as though it were an observation would
    # be a fabricated finding on a compensation record, which is worse than
    # having no radar at all.
    synthetic: bool = False

    # Earth Engine limits concurrent requests per project, and a
    # district-wide event submits hundreds of parcels within minutes. Four is
    # conservative; raise it once the project's actual quota is known.
    max_concurrent: int = 4

    # Beyond this many queued tasks the service answers 503 with Retry-After
    # rather than accepting work it cannot start. An unbounded queue is a
    # slower way to fall over.
    max_queued: int = 200

    # How long a finished task is answerable for. The platform polls within
    # seconds; an hour is generous and bounds the memory a crashed poller
    # can leak.
    keep_completed_s: float = 3600

    #: The shared token the platform API presents. Empty disables the
    #: check, which is what a developer laptop runs with --
    #: Earth Engine work runs against somebody's billing account, so the
    #: platform API refuses to start in a hardened environment without
    #: one. See callers.py.
    caller_token: str = ""

    # Where task state is shared, when there is more than one replica.
    #
    # Empty keeps it in this process, which is right for a laptop and wrong
    # for a deployment: a second replica would answer "no such task" to the
    # first replica's pollers, and a restart would lose the Earth Engine work
    # in flight (docs/PLATFORM_SCALE_REVIEW.md, A4). Nothing kept here is a
    # record -- a lost task is a resubmission -- so it is a cache in the
    # strict sense and may be the platform's own Redis.
    redis_url: str = ""

    host: str = "127.0.0.1"
    port: int = 8200

    @property
    def configured(self) -> bool:
        """Whether this service can answer at all."""
        return self.synthetic or bool(self.ee_project)


settings = Settings()
