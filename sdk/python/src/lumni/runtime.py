"""User-operated async workers. Orchestration replays; completed calls do not."""
from __future__ import annotations

import asyncio
import inspect
import uuid
from contextlib import suppress
from typing import Any, Awaitable, Callable
from urllib.parse import quote

from .client import Client


def _q(value: str) -> str:
    return quote(value, safe="")


class RuntimeClient:
    def __init__(self, client: Client): self.client = client
    async def request(self, method: str, path: str, value: Any = None, retry: bool = True) -> Any:
        return await asyncio.to_thread(self.client._request, method, path, value, retry)
    async def confirm_mapping(self, mapping: dict[str, Any]) -> Any: return await self.request("POST", "/v1/discovery/confirm", mapping)
    async def enforcement(self, operation: str, rule: str, mode: str) -> Any: return await self.request("PUT", "/v1/config/enforcement", {"operation":operation,"rule":rule,"mode":mode})
    async def retry_verification(self, outcome: str) -> Any: return await self.request("POST", f"/v1/outcomes/{_q(outcome)}/retry", {})
    async def discover(self, source: str, name: str, schema: dict[str, Any]) -> Any: return await self.request("POST", "/v1/discovery/tools", {"source":source,"name":name,"schema":schema})
    async def register_reconciliation(self, name: str, version: str) -> Any: return await self.request("POST", "/v1/reconciliation/handlers", {"name":name,"version":version})
    async def reconcile(self, id: str, outcome_id: str, handler: str, handler_version: str, input: Any) -> Any: return await self.request("POST", "/v1/reconciliation", {"id":id,"outcome_id":outcome_id,"handler":handler,"handler_version":handler_version,"input":input})
    async def resolve_effect_identity(self, legacy_key: str, current_key: str, evidence: Any) -> Any: return await self.request("POST", "/v1/effects/resolve-identity", {"legacy_key":legacy_key,"current_key":current_key,"evidence":evidence})
    async def submit(self, value: dict[str, Any]) -> Any: return await self.request("POST", "/v1/runtime/work", value)
    async def claim(self, worker_id: str, handlers: dict[str, str]) -> Any: return (await self.request("POST", "/v1/runtime/claim", {"worker_id": worker_id, "handlers": handlers}, False)).get("claim")
    async def heartbeat(self, work: str, auth: dict[str, Any]) -> Any: return await self.request("POST", f"/v1/runtime/work/{_q(work)}/heartbeat", auth)
    async def begin(self, work: str, value: dict[str, Any]) -> Any: return await self.request("POST", f"/v1/runtime/work/{_q(work)}/steps/begin", value)
    async def start(self, work: str, key: str, value: dict[str, Any]) -> Any: return await self.request("POST", f"/v1/runtime/work/{_q(work)}/steps/{_q(key)}/start", value)
    async def result(self, work: str, key: str, value: dict[str, Any]) -> Any: return await self.request("POST", f"/v1/runtime/work/{_q(work)}/steps/{_q(key)}/result", value)
    async def finish(self, work: str, value: dict[str, Any]) -> Any: return await self.request("POST", f"/v1/runtime/work/{_q(work)}/finish", value)
    async def cancel(self, work: str) -> Any: return await self.request("POST", f"/v1/runtime/work/{_q(work)}/cancel", {})
    async def signal(self, work: str, name: str, payload: Any = None, id: str | None = None) -> Any: return await self.request("POST", f"/v1/runtime/work/{_q(work)}/signals", {"id": id or str(uuid.uuid4()), "name": name, "payload": payload})
    async def verifier(self, value: dict[str, Any]) -> Any: return await self.request("POST", "/v1/verifiers", value)
    async def evidence(self, outcome: str, value: dict[str, Any]) -> Any: return await self.request("POST", f"/v1/outcomes/{_q(outcome)}/evidence", value)
    async def bind_operation_verifier(self, operation: str, verifier_id: str, expected_template: dict[str, Any]) -> Any:
        return await self.request("PUT", "/v1/config/operation-verifiers", {"operation":operation,"verifier_id":verifier_id,"expected_template":expected_template})
    async def recheck(self, action: str, id: str | None = None) -> Any: return await self.request("POST", f"/v1/actions/{_q(action)}/recheck", {"id": id or str(uuid.uuid4())})
    async def previous_result(self, action: str) -> Any: return (await self.request("GET", f"/v1/actions/{_q(action)}/previous-result"))["output"]
    async def dependency(self, value: dict[str, Any]) -> Any: return await self.request("POST", "/v1/dependencies", value)
    async def query(self, collection: str, id: str = "", work: str = "") -> Any: return (await self.request("GET", f"/v1/{_q(collection)}" + (f"/{_q(id)}" if id else "") + f"?work_id={_q(work)}"))["items"]


class Suspended(Exception):
    """The durable wait has released ownership and worker capacity."""


class StepFailure(Exception):
    def __init__(self, step: dict[str, Any]):
        self.step = step
        super().__init__(step.get("error") or step["status"])


class SafeFailure(Exception):
    """Only raise when the attempted operation is known unable to commit."""
    def __init__(self, code: str, retryable: bool = False):
        self.code, self.retryable = code, retryable
        super().__init__(code)


class RunContext:
    def __init__(self, runtime: RuntimeClient, claim: dict[str, Any]):
        self.runtime, self.claim = runtime, claim
        self._executing = False

    @property
    def auth(self) -> dict[str, Any]: return {"token": self.claim["token"], "fence": self.claim["fence"]}
    async def llm(self, key: str, input: Any, call: Callable[[], Awaitable[Any]]) -> Any: return await self._execute(key, "LLM", input, call)
    async def step(self, key: str, input: Any, call: Callable[[], Awaitable[Any]]) -> Any: return await self._execute(key, "CUSTOM", input, call)
    async def tool(self, key: str, action: dict[str, Any], call: Callable[[], Awaitable[Any]]) -> Any: return await self._execute(key, "TOOL", action.get("arguments"), call, action)

    async def _execute(self, key: str, type: str, input: Any, call: Callable[[], Awaitable[Any]], action: dict[str, Any] | None = None) -> Any:
        if self._executing: raise RuntimeError("Use child work for concurrent steps")
        self._executing = True
        try:
            st = await self.runtime.begin(self.claim["work_id"], {**self.auth, "key": key, "type": type, "input": input, "action": action})
            if st["status"] == "COMPLETED": return st.get("output")
            if st["status"] in ("WAITING", "UNKNOWN"): raise Suspended()
            if st["status"] != "READY": raise StepFailure(st)
            result = {**self.auth, "attempt": st["attempt"], "status": "COMPLETED"}
            await self.runtime.start(self.claim["work_id"], key, result)
            try:
                result["output"] = await call()
            except Exception as error:
                safe = isinstance(error, SafeFailure)
                result.update(status="UNKNOWN" if type == "TOOL" and not safe else "FAILED", error=error.code if safe else "STEP_EXECUTION_FAILED", effect_absent=safe, retryable=error.retryable if safe else type == "LLM")
            st = await self.runtime.result(self.claim["work_id"], key, result)
            if st["status"] in ("WAITING", "UNKNOWN"): raise Suspended()
            if st["status"] != "COMPLETED": raise StepFailure(st)
            return st.get("output")
        finally:
            self._executing = False

    async def wait(self, key: str, condition: dict[str, Any]) -> Any:
        st = await self.runtime.begin(self.claim["work_id"], {**self.auth, "key": key, "type": "WAIT", "wait": condition})
        if st["status"] == "COMPLETED": return st.get("output")
        if st["status"] == "FAILED": raise StepFailure(st)
        raise Suspended()

    async def sleep(self, key: str, delay_ms: int) -> Any: return await self.wait(key, {"type":"TIMER","delay_ms":delay_ms})
    async def approval(self, key: str, id: str) -> Any: return await self.wait(key, {"type": "APPROVAL", "approval_id": id})
    async def child(self, key: str, handler: str, version: str, input: Any) -> Any:
        id = f'{self.claim["work_id"]}:{key}'
        await self.runtime.submit({"id": id, "actor_id": self.claim["actor_id"], "parent_work_id": self.claim["work_id"], "handler": handler, "handler_version": version, "input": input, "goal": key})
        return await self.wait(key, {"type": "WORK", "work_id": id})
    async def handoff(self, key: str, handler: str, version: str, input: Any) -> Any: return await self.child(key, handler, version, input)


def agent(name: str, version: str):
    def decorate(handler):
        if not inspect.iscoroutinefunction(handler): raise TypeError("Agent handlers must be async")
        handler.__lumni_agent__ = (name, version)
        return handler
    return decorate


class Worker:
    def __init__(self, runtime: RuntimeClient, id: str | None = None):
        self.runtime, self.id = runtime, id or f"worker_{uuid.uuid4()}"
        self.handlers: dict[str, tuple[str, Callable]] = {}
    def register(self, name: str, version: str, handler: Callable) -> Worker:
        if not name or not version or not inspect.iscoroutinefunction(handler): raise ValueError("A named, versioned async handler is required")
        self.handlers[name] = (version, handler)
        return self
    def add(self, handler: Callable) -> Worker: return self.register(*handler.__lumni_agent__, handler)
    async def run_once(self) -> bool:
        claim = await self.runtime.claim(self.id, {name: item[0] for name, item in self.handlers.items()})
        if claim is None: return False
        ctx = RunContext(self.runtime, claim)
        owner = asyncio.current_task()
        async def renew():
            try:
                while True:
                    await asyncio.sleep(10)
                    await self.runtime.heartbeat(claim["work_id"], ctx.auth)
            except Exception:
                owner.cancel()
        heartbeat = asyncio.create_task(renew())
        try:
            try:
                output = await self.handlers[claim["handler"]][1](ctx, claim["input"])
            except Suspended: return True
            except Exception:
                await self.runtime.finish(claim["work_id"], {**ctx.auth, "status": "FAILED", "error": "HANDLER_FAILED"})
                raise
            await self.runtime.finish(claim["work_id"], {**ctx.auth, "status": "COMPLETED", "output": output})
            return True
        finally:
            heartbeat.cancel()
            with suppress(asyncio.CancelledError): await heartbeat
    async def run(self):
        while True:
            if not await self.run_once(): await asyncio.sleep(1)
