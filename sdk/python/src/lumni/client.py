from __future__ import annotations

import json
import time
import uuid
from dataclasses import dataclass
from datetime import datetime, timezone
from typing import Any, Literal, TypedDict
from urllib.error import HTTPError, URLError
from urllib.request import Request, urlopen


@dataclass
class LumniError(Exception):
    code: str
    message: str
    status: int
    details: Any = None

    def __str__(self) -> str:
        return f"{self.code}: {self.message}"


DecisionType = Literal["ALLOW", "WARN", "WAIT", "REPLAN", "DEDUPLICATE", "REQUIRE_APPROVAL", "REJECT", "WOULD_ALLOW", "WOULD_WAIT", "WOULD_REPLAN", "WOULD_DEDUPLICATE", "WOULD_REQUIRE_APPROVAL", "WOULD_REJECT"]


class Decision(TypedDict, total=False):
    schema_version: str
    action_id: str
    receipt_id: str
    decision: DecisionType
    reason_code: str
    enforced: bool
    confidence: float
    retry_after_ms: int
    lease_token: str
    approval_id: str


@dataclass(frozen=True)
class LumniContext:
    actor_id: str = ""
    agent_id: str = ""
    session_id: str = ""
    work_id: str = ""


class Client:
    def __init__(self, base_url: str, api_key: str, tenant_id: str | None = None, timeout: float = 30.0):
        self.base_url = base_url.rstrip("/")
        self.api_key = api_key
        self.tenant_id = tenant_id
        self.timeout = timeout

    def start_work(self, value: dict[str, Any]) -> dict[str, Any]: return self._request("POST", "/v1/work", {"id": f"work_{uuid.uuid4()}", **value})
    def complete_work(self, work_id: str) -> dict[str, Any]: return self._request("POST", f"/v1/work/{work_id}/complete", {})
    def observe(self, value: dict[str, Any]) -> dict[str, Any]: return self._request("POST", "/v1/observations", {"id": f"obs_{uuid.uuid4()}", **value})
    def update_resource_version(self, resource_id: str, value: dict[str, Any]) -> dict[str, Any]: return self._request("POST", f"/v1/resources/{resource_id}/version", value)
    def check_action(self, value: dict[str, Any]) -> Decision: return self._request("POST", "/v1/actions/check", {"action_id": f"act_{uuid.uuid4()}", **value})  # type: ignore[return-value]
    def observe_action(self, value: dict[str, Any]) -> Decision: return self._request("POST", "/v1/actions/observe", {"action_id": f"act_{uuid.uuid4()}", **value})  # type: ignore[return-value]
    def start_action(self, action_id: str, lease_token: str = "") -> dict[str, Any]: return self._request("POST", f"/v1/actions/{action_id}/start", {"lease_token": lease_token})
    def heartbeat(self, action_id: str, lease_token: str) -> dict[str, Any]: return self._request("POST", f"/v1/actions/{action_id}/heartbeat", {"lease_token": lease_token})
    def report_result(self, action_id: str, value: dict[str, Any]) -> dict[str, Any]: return self._request("POST", f"/v1/actions/{action_id}/result", value)
    def approval(self, approval_id: str) -> dict[str, Any]: return self._request("GET", f"/v1/approvals/{approval_id}")
    def ingest_event(self, value: dict[str, Any]) -> dict[str, Any]: return self._request("POST", "/v1/events", {"schema_version": "1.0", "event_id": f"evt_{uuid.uuid4()}", "occurred_at": datetime.now(timezone.utc).isoformat(), **value})
    def record_cost(self, value: dict[str, Any]) -> dict[str, Any]: return self._request("POST", "/v1/costs", {"id": f"cost_{uuid.uuid4()}", **value})

    def _request(self, method: str, path: str, value: Any = None, retry: bool = True) -> dict[str, Any]:
        data = None if value is None else json.dumps(value, separators=(",", ":")).encode()
        headers = {"Authorization": f"Bearer {self.api_key}", "X-Request-ID": f"req_{uuid.uuid4()}"}
        if data is not None: headers["Content-Type"] = "application/json"
        if self.tenant_id: headers["X-Lumni-Tenant"] = self.tenant_id
        last: Exception | None = None
        attempts = 3 if retry else 1
        for attempt in range(attempts):
            try:
                with urlopen(Request(self.base_url + path, data=data, headers=headers, method=method), timeout=self.timeout) as response:
                    body = response.read()
                    return json.loads(body) if body else {}
            except HTTPError as error:
                body = error.read()
                payload = json.loads(body) if body else {}
                detail = payload.get("error", {})
                last = LumniError(detail.get("code", "HTTP_ERROR"), detail.get("message", str(error)), error.code, detail.get("details"))
                if error.code < 500: raise last
            except URLError as error:
                last = error
            if attempt < attempts - 1: time.sleep(0.05 * (attempt + 1))
        assert last is not None
        raise last


def should_execute(decision: dict[str, Any]) -> bool:
    return decision.get("decision") in {"ALLOW", "WARN", "WOULD_ALLOW", "WOULD_WAIT", "WOULD_REPLAN", "WOULD_DEDUPLICATE", "WOULD_REQUIRE_APPROVAL", "WOULD_REJECT"}


def with_context(action: dict[str, Any], context: LumniContext) -> dict[str, Any]:
    result = dict(action)
    if context.actor_id: result["actor_id"] = context.actor_id
    if context.session_id: result["session_id"] = context.session_id
    if context.work_id: result["work_id"] = context.work_id
    result["metadata"] = {**result.get("metadata", {}), "lumni_context": context.__dict__}
    return result


def context_headers(context: LumniContext) -> dict[str, str]:
    return {"X-Lumni-Actor": context.actor_id, "X-Lumni-Agent": context.agent_id, "X-Lumni-Session": context.session_id, "X-Lumni-Work": context.work_id}
