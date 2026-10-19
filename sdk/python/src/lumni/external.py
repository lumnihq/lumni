from __future__ import annotations
import time
from datetime import datetime
from typing import Any, Awaitable, Callable
from .runtime import RuntimeClient, _q
from .client import should_execute, LumniError

class AdmissionPending(Exception):
    def __init__(self, result): self.result = result; super().__init__("Awaiting admission or authoritative verification")

class ExternalClient(RuntimeClient):
    async def prepare(self, action: dict[str, Any], recheck_id: str = "") -> dict[str, Any]:
        if not action.get("action_id"): raise ValueError("A stable action_id is required")
        await self.request("POST", "/v1/actors", {"id": action["actor_id"], "type": "AGENT", "runtime": "external"})
        await self.request("POST", "/v1/work", {"id": action["work_id"], "actor_id": action["actor_id"], "goal": action["operation"]})
        d = await self.request("POST", "/v1/actions/check", action)
        expired = d.get("expires_at") and datetime.fromisoformat(d["expires_at"].replace("Z", "+00:00")).timestamp() < time.time()
        if recheck_id and (d["decision"] in ("WAIT", "REQUIRE_APPROVAL") or expired): return await self.recheck(d["action_id"], recheck_id)
        return d

    async def outcome(self, action: str, verified: bool) -> dict[str, Any]:
        items = await self.query("outcomes", "out_" + action)
        o = items[0] if items else {}
        if o.get("status") in ("FAILED", "MISMATCH", "PARTIAL"): return {"status": o["status"]}
        if (verified or o.get("verifier_id")) and o.get("status") != "VERIFIED": return {"status": "PENDING"}
        try: return {"status": "COMPLETED", "output": await self.previous_result(action)}
        except LumniError as error:
            if error.code == "RESULT_PENDING": return {"status": "PENDING"}
            raise

    async def execute(self, action: dict[str, Any], decision: dict[str, Any], call: Callable[[], Awaitable[Any]]) -> dict[str, Any]:
        if decision["decision"] == "DEDUPLICATE":
            if not decision.get("existing_action_id"): raise ValueError("RESULT_UNAVAILABLE")
            return await self.outcome(decision["existing_action_id"], bool(action.get("verification")))
        if not should_execute(decision): return {"status": "PENDING", "decision": decision}
        result = await self.request("POST", f'/v1/actions/{_q(decision["action_id"])}/start-once', {"lease_token": decision.get("lease_token", "")}, False)
        if not result["started"]: return await self.outcome(decision["action_id"], bool(action.get("verification")))
        try: output = await call()
        except Exception:
            await self.request("POST", f'/v1/actions/{_q(decision["action_id"])}/result', {"status": "UNKNOWN", "lease_token": decision.get("lease_token")})
            return {"status": "PENDING"}
        await self.request("POST", f'/v1/actions/{_q(decision["action_id"])}/result', {"status": "SUCCEEDED", "output": output, "lease_token": decision.get("lease_token")})
        return await self.outcome(decision["action_id"], bool(action.get("verification")))
