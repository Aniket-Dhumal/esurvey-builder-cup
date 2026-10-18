"""Where submitted work is remembered.

# Why this is not a dictionary any more

The runner kept its tasks in a dict, which is correct for one process and
wrong for two. A second replica answers "no such task" to the first replica's
pollers, so the service could not be scaled out at all -- and a restart lost
every analysis in flight, including the paid-for Earth Engine results
(docs/PLATFORM_SCALE_REVIEW.md, A4).

So the state lives behind this interface: the tasks a poller asks about, the
results that make a resubmission free, and the claim that stops two replicas
paying twice for one parcel. In memory for a laptop, in Redis for anything
with more than one of anything.

# What is deliberately *not* shared

The semaphore. Earth Engine's limit is per project, and the honest way to hold
a project-wide limit across replicas would be a distributed lease with all the
failure modes that implies. Instead each replica admits `max_concurrent`, and
the deployment multiplies: two replicas at four each is eight against the
project's quota. Set it so that replicas x max_concurrent is what the project
actually allows -- it is written down in docker-compose.yml beside the setting.

# The lease

A replica that dies mid-analysis would otherwise leave a task PROCESSING for
ever, and a caller polling it waits for something nobody is doing. The
replica running a task renews a short lease; a task found PROCESSING with no
lease is reported failed, which the platform's assessment queue reads as a
retryable failure and tries again.
"""

from __future__ import annotations

import json
import time
from dataclasses import dataclass, field
from typing import Any, Protocol

from .schemas import Status


@dataclass
class TaskState:
    """A task as anyone polling it sees it."""

    id: str
    kind: str = "sar"
    status: Status = Status.PROCESSING
    # The analysis, as JSON. Kept encoded because the store does not know
    # which model it is -- the endpoint that serves it does.
    result: dict[str, Any] | None = None
    error: str | None = None
    error_code: str | None = None
    created_at: float = field(default_factory=time.time)
    finished_at: float | None = None

    @property
    def finished(self) -> bool:
        return self.status in (Status.COMPLETED, Status.FAILED)

    def to_json(self) -> str:
        return json.dumps(
            {
                "id": self.id,
                "kind": self.kind,
                "status": str(self.status),
                "result": self.result,
                "error": self.error,
                "error_code": self.error_code,
                "created_at": self.created_at,
                "finished_at": self.finished_at,
            }
        )

    @staticmethod
    def from_json(raw: str) -> TaskState:
        d = json.loads(raw)
        return TaskState(
            id=d["id"],
            kind=d.get("kind", "sar"),
            status=Status(d["status"]),
            result=d.get("result"),
            error=d.get("error"),
            error_code=d.get("error_code"),
            created_at=d.get("created_at", time.time()),
            finished_at=d.get("finished_at"),
        )


class Store(Protocol):
    """What the runner needs to remember, wherever it is kept."""

    async def save(self, task: TaskState, ttl_s: float) -> None: ...

    async def load(self, task_id: str) -> TaskState | None: ...

    async def claim_work(self, key: str, task_id: str, ttl_s: float) -> str | None:
        """Claim a work key, or return the task id already holding it."""

    async def release_work(self, key: str) -> None: ...

    async def result_for(self, key: str) -> dict[str, Any] | None: ...

    async def save_result(
        self, key: str, result: dict[str, Any], ttl_s: float
    ) -> None: ...

    async def pending(self) -> int: ...

    async def mark_pending(self, task_id: str) -> None: ...

    async def clear_pending(self, task_id: str) -> None: ...

    async def renew_lease(self, task_id: str, ttl_s: float) -> None: ...

    async def lease_alive(self, task_id: str) -> bool: ...

    async def stats(self) -> dict[str, int]: ...


class MemoryStore:
    """One process, no dependencies. What a laptop and the tests run on."""

    def __init__(self) -> None:
        self._tasks: dict[str, tuple[float, TaskState]] = {}
        self._work: dict[str, str] = {}
        self._results: dict[str, tuple[float, dict[str, Any]]] = {}
        self._pending: set[str] = set()
        self._leases: dict[str, float] = {}

    def _sweep(self) -> None:
        now = time.time()
        for task_id, (expires, _) in list(self._tasks.items()):
            if expires < now:
                del self._tasks[task_id]
        for key, (expires, _) in list(self._results.items()):
            if expires < now:
                del self._results[key]
        for task_id, expires in list(self._leases.items()):
            if expires < now:
                del self._leases[task_id]

    async def save(self, task: TaskState, ttl_s: float) -> None:
        self._sweep()
        self._tasks[task.id] = (time.time() + ttl_s, task)

    async def load(self, task_id: str) -> TaskState | None:
        self._sweep()
        held = self._tasks.get(task_id)
        return held[1] if held else None

    async def claim_work(self, key: str, task_id: str, ttl_s: float) -> str | None:
        self._sweep()
        existing = self._work.get(key)
        if existing is not None and existing in self._tasks:
            return existing
        self._work[key] = task_id
        return None

    async def release_work(self, key: str) -> None:
        self._work.pop(key, None)

    async def result_for(self, key: str) -> dict[str, Any] | None:
        self._sweep()
        held = self._results.get(key)
        return held[1] if held else None

    async def save_result(
        self, key: str, result: dict[str, Any], ttl_s: float
    ) -> None:
        self._results[key] = (time.time() + ttl_s, result)

    async def pending(self) -> int:
        return len(self._pending)

    async def mark_pending(self, task_id: str) -> None:
        self._pending.add(task_id)

    async def clear_pending(self, task_id: str) -> None:
        self._pending.discard(task_id)

    async def renew_lease(self, task_id: str, ttl_s: float) -> None:
        self._leases[task_id] = time.time() + ttl_s

    async def lease_alive(self, task_id: str) -> bool:
        self._sweep()
        return task_id in self._leases

    async def stats(self) -> dict[str, int]:
        self._sweep()
        return {
            "tasks_held": len(self._tasks),
            "in_flight_or_queued": len(self._pending),
            "cached_results": len(self._results),
        }


class RedisStore:
    """Shared state, so replicas answer each other's pollers.

    Every key carries a time to live, because everything here is recoverable:
    a lost task is a resubmission, and a lost result is one more Earth Engine
    call. Nothing in this store is a record anybody keeps.
    """

    def __init__(self, client: Any, prefix: str = "sar") -> None:
        self._r = client
        self._p = prefix

    def _k(self, *parts: str) -> str:
        return ":".join((self._p, *parts))

    async def save(self, task: TaskState, ttl_s: float) -> None:
        await self._r.set(self._k("task", task.id), task.to_json(), ex=int(ttl_s))

    async def load(self, task_id: str) -> TaskState | None:
        raw = await self._r.get(self._k("task", task_id))
        if raw is None:
            return None
        if isinstance(raw, bytes):
            raw = raw.decode()
        return TaskState.from_json(raw)

    async def claim_work(self, key: str, task_id: str, ttl_s: float) -> str | None:
        # SET NX is the claim: exactly one replica wins, and the loser is told
        # which task to join.
        won = await self._r.set(self._k("work", key), task_id, nx=True, ex=int(ttl_s))
        if won:
            return None
        existing = await self._r.get(self._k("work", key))
        if existing is None:
            # It expired between the two calls; try once more.
            won = await self._r.set(
                self._k("work", key), task_id, nx=True, ex=int(ttl_s)
            )
            if won:
                return None
            existing = await self._r.get(self._k("work", key))
        if isinstance(existing, bytes):
            existing = existing.decode()
        return existing

    async def release_work(self, key: str) -> None:
        await self._r.delete(self._k("work", key))

    async def result_for(self, key: str) -> dict[str, Any] | None:
        raw = await self._r.get(self._k("result", key))
        if raw is None:
            return None
        if isinstance(raw, bytes):
            raw = raw.decode()
        return json.loads(raw)

    async def save_result(
        self, key: str, result: dict[str, Any], ttl_s: float
    ) -> None:
        await self._r.set(self._k("result", key), json.dumps(result), ex=int(ttl_s))

    async def pending(self) -> int:
        # Stale entries are workers that died; they are pruned here rather
        # than by a sweeper, so the admission limit is never held by ghosts.
        cutoff = time.time() - 3600
        await self._r.zremrangebyscore(self._k("pending"), 0, cutoff)
        return int(await self._r.zcard(self._k("pending")))

    async def mark_pending(self, task_id: str) -> None:
        await self._r.zadd(self._k("pending"), {task_id: time.time()})

    async def clear_pending(self, task_id: str) -> None:
        await self._r.zrem(self._k("pending"), task_id)

    async def renew_lease(self, task_id: str, ttl_s: float) -> None:
        await self._r.set(self._k("lease", task_id), "1", ex=max(int(ttl_s), 1))

    async def lease_alive(self, task_id: str) -> bool:
        return bool(await self._r.exists(self._k("lease", task_id)))

    async def stats(self) -> dict[str, int]:
        return {
            "in_flight_or_queued": await self.pending(),
            "shared": 1,
        }
