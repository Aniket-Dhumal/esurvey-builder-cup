"""Submitted work, and the limits that keep it standing up.

# What binds

Earth Engine limits concurrent requests per project. A district-wide hailstorm
produces hundreds of parcel submissions within minutes, all at once, and a
service that forwarded them would be throttled by EE and would report every
one of them as a failure.

So the concurrency is bounded here and the rest queue. A slower answer is
worth far more than a 429, because the platform's client retries a 5xx and
gives up on a persistent one -- and giving up means an assessment made without
radar during exactly the event radar was bought for.

# Why there is a queue limit as well

An unbounded queue is a slower way to fall over. Beyond a depth the service
returns 503 with Retry-After, which is a promise the caller can act on rather
than an acceptance it cannot keep.

# Why identical work is not done twice

A closed window over an archive cannot change. Two farmers in the same
locality, and the same parcel resubmitted after a poll timeout, are the same
question -- so a submission that matches one already running joins it rather
than starting a second analysis.

# Why the state is not in this process

It was, and that meant one replica could not answer another replica's pollers
and a restart lost every analysis in flight. The tasks, the results and the
claim on a piece of work now live behind store.Store -- a dict on a laptop,
Redis anywhere with more than one of anything. See store.py.
"""

from __future__ import annotations

import asyncio
import logging
import time
import uuid
from collections.abc import Callable
from dataclasses import dataclass, field

from pydantic import BaseModel

from .earth import (
    Backend,
    BadGeometryError,
    QuotaError,
    UnavailableError,
    Window,
    geometry_key,
)
from .schemas import ErrorCode, Geometry, Status
from .store import MemoryStore, Store, TaskState

log = logging.getLogger(__name__)


@dataclass
class Task:
    id: str
    status: Status = Status.PROCESSING
    # Whatever the work produced: a radar series or a moisture record, as the
    # plain object it serialises to. The runner does not care which, and
    # neither does the poller -- the status endpoint for each kind validates
    # it back into its own model.
    result: dict | None = None
    kind: str = "sar"
    error: str | None = None
    error_code: str | None = None

    created_at: float = field(default_factory=time.time)
    finished_at: float | None = None

    @staticmethod
    def of(state: TaskState) -> Task:
        return Task(
            id=state.id,
            status=state.status,
            result=state.result,
            kind=state.kind,
            error=state.error,
            error_code=state.error_code,
            created_at=state.created_at,
            finished_at=state.finished_at,
        )


class AtCapacityError(Exception):
    """The queue is at its limit."""

    def __init__(self, retry_after: int) -> None:
        super().__init__("the service is at capacity")
        self.retry_after = retry_after


class Runner:
    """Runs submissions, bounded."""

    def __init__(
        self,
        backend: Backend,
        *,
        max_concurrent: int = 4,
        max_queued: int = 200,
        keep_completed_s: float = 3600,
        keep_results_s: float = 6 * 3600,
        store: Store | None = None,
        lease_s: float = 60,
    ) -> None:
        self.backend = backend
        self.max_queued = max_queued
        # How long a finished task is answerable for, and -- separately -- how
        # long its answer is kept. They are not the same lifetime: a task
        # exists so a poller can find it, and an answer exists so a
        # resubmission does not cost another Earth Engine call. Tying the
        # second to the first would mean sweeping tasks aggressively also
        # threw away paid-for results.
        self.keep_completed_s = keep_completed_s
        self.keep_results_s = keep_results_s

        # Per replica, not per project: see the note in store.py on why the
        # deployment multiplies this and must keep replicas x max_concurrent
        # inside the Earth Engine quota.
        self._sem = asyncio.Semaphore(max_concurrent)
        self.max_concurrent = max_concurrent

        # Where tasks, results and work claims live. A dict unless a shared
        # store was handed in.
        self._store: Store = store or MemoryStore()

        # How long the replica running a task says it is still alive for. A
        # task found running with no lease is reported failed, because
        # something has to tell the caller that nobody is producing it.
        self.lease_s = lease_s

        self._running: set[asyncio.Task[None]] = set()

    # --- submission -------------------------------------------------------

    async def submit(self, geometry: Geometry, window: Window) -> tuple[str, bool]:
        """Accept radar work. Returns the task id and whether it is new."""
        return await self.submit_work(
            "sar",
            geometry_key(geometry, window),
            lambda: self.backend.series(geometry, window),
        )

    async def submit_work(
        self, kind: str, key: str, work: Callable[[], BaseModel]
    ) -> tuple[str, bool]:
        """Accept any unit of Earth Engine work.

        # Why every kind shares one runner

        Radar and moisture are two questions put to the same Earth Engine
        project, and the project's concurrency limit is one limit. Two runners
        with four slots each would put eight requests against a quota sized
        for four, and a district-wide event would be throttled into failures
        on both. One semaphore, one queue limit, one honest 503.

        The key is namespaced by kind so a radar series and a moisture record
        for the same parcel and window are never mistaken for each other.
        """
        key = f"{kind}:{key}"

        # Already answered. A closed window over an archive cannot change, so
        # this is the same answer, and it costs nothing.
        cached = await self._store.result_for(key)
        if cached is not None:
            task = TaskState(
                id=_new_id(),
                kind=kind,
                status=Status.COMPLETED,
                result=cached,
                finished_at=time.time(),
            )
            await self._store.save(task, self.keep_completed_s)
            return task.id, False

        if await self._store.pending() >= self.max_queued:
            # A promise the caller can act on, rather than an acceptance that
            # cannot be kept.
            raise AtCapacityError(retry_after=30)

        task = TaskState(id=_new_id(), kind=kind)
        # One claim wins; anybody else joins the task that holds it. Across
        # replicas as well, which is the point of the shared store.
        holder = await self._store.claim_work(key, task.id, self.keep_results_s)
        if holder is not None:
            return holder, False

        await self._store.save(task, self.keep_completed_s)
        await self._store.mark_pending(task.id)
        await self._store.renew_lease(task.id, self.lease_s)

        runner = asyncio.create_task(self._run(task, key, work))
        self._running.add(runner)
        runner.add_done_callback(self._running.discard)
        return task.id, True

    async def get(self, task_id: str) -> Task | None:
        """The task as a poller sees it.

        A task still PROCESSING whose replica has stopped renewing its lease
        is reported failed rather than left pending for ever: the caller's own
        queue reads that as retryable and asks again, which is the only way
        the work gets done.
        """
        state = await self._store.load(task_id)
        if state is None:
            return None
        alive = await self._store.lease_alive(task_id)
        if state.status is Status.PROCESSING and not alive:
            state.status = Status.FAILED
            state.error = (
                "the analysis stopped before it finished; submit it again"
            )
            state.error_code = str(ErrorCode.EE_UNAVAILABLE)
            state.finished_at = time.time()
            await self._store.save(state, self.keep_completed_s)
            await self._store.clear_pending(task_id)
        return Task.of(state)

    # --- execution --------------------------------------------------------

    async def _run(
        self, task: TaskState, key: str, work: Callable[[], BaseModel]
    ) -> None:
        heartbeat = asyncio.create_task(self._hold_lease(task.id))
        try:
            async with self._sem:
                try:
                    # The Earth Engine client is synchronous and network-bound,
                    # so it goes to a thread: holding the event loop through a
                    # multi-second call would stall every poll the platform
                    # makes.
                    result = await asyncio.to_thread(work)
                except BadGeometryError as exc:
                    await self._fail(task, key, str(exc), exc.code)
                    return
                except QuotaError as exc:
                    # Retryable, and the platform's client already knows how.
                    await self._fail(task, key, str(exc), ErrorCode.EE_QUOTA)
                    return
                except UnavailableError as exc:
                    await self._fail(task, key, str(exc), ErrorCode.EE_UNAVAILABLE)
                    return
                except Exception as exc:  # pragma: no cover - defensive
                    log.exception("%s analysis failed", task.kind)
                    await self._fail(task, key, str(exc), ErrorCode.INTERNAL)
                    return

            task.result = _plain(result)
            task.status = Status.COMPLETED
            task.finished_at = time.time()
            await self._store.save(task, self.keep_completed_s)
            await self._store.save_result(key, task.result, self.keep_results_s)
            await self._store.release_work(key)
            await self._store.clear_pending(task.id)
        finally:
            heartbeat.cancel()

    async def _hold_lease(self, task_id: str) -> None:
        """Says this replica is still working, until it is not."""
        try:
            while True:
                await self._store.renew_lease(task_id, self.lease_s)
                await asyncio.sleep(self.lease_s / 3)
        except asyncio.CancelledError:  # pragma: no cover - shutdown path
            return

    async def _fail(
        self, task: TaskState, key: str, message: str, code: ErrorCode
    ) -> None:
        task.status = Status.FAILED
        task.error = message
        task.error_code = str(code)
        task.finished_at = time.time()
        await self._store.save(task, self.keep_completed_s)
        # Release the work key. A failure is not an answer, and leaving it
        # claimed would make one Earth Engine outage permanent for that parcel
        # and window until it expired -- so a caller retrying during an outage
        # would be handed the failure again instead of a new attempt.
        await self._store.release_work(key)
        await self._store.clear_pending(task.id)

    # --- reporting --------------------------------------------------------

    async def stats(self) -> dict[str, int]:
        """What the service is doing, for /readyz.

        A queue that is always full and a queue that is always empty look the
        same from outside, and the difference is whether anybody needs to
        raise the concurrency -- or add a replica.
        """
        out = await self._store.stats()
        out["queue_limit"] = self.max_queued
        out["concurrency_per_replica"] = self.max_concurrent
        return out


def _plain(result: BaseModel) -> dict:
    """The model as data, which is what a shared store can hold."""
    return result.model_dump(mode="json")


def _new_id() -> str:
    return uuid.uuid4().hex
