from __future__ import annotations
import json
import uuid
from dataclasses import replace
from typing import Callable
from .client import should_execute
from .external import ExternalClient, AdmissionPending

def openai_tool(tool, client: ExternalClient, action_for: Callable):
    """Wrap an Agents FunctionTool; retain RunState before approval/resumption."""
    def action(input, id):
        value = dict(action_for(input, id))
        value.setdefault("action_id", f'{value["work_id"]}:{id}')
        return value

    async def needs_approval(ctx, input, call_id):
        d = await client.prepare(action(input, call_id))
        original = tool.needs_approval
        requested = await original(ctx, input, call_id) if callable(original) else original
        return (not should_execute(d) and d["decision"] != "DEDUPLICATE") or requested

    async def invoke(ctx, raw):
        value = action(json.loads(raw), ctx.tool_call_id)
        decision = await client.prepare(value, str(uuid.uuid4()))
        result = await client.execute(value, decision, lambda: tool.on_invoke_tool(ctx, raw))
        if result["status"] != "COMPLETED": raise AdmissionPending(result)
        return result.get("output")
    return replace(tool, needs_approval=needs_approval, on_invoke_tool=invoke, timeout_behavior="raise_exception")
