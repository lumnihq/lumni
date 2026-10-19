from __future__ import annotations
from typing import Any, Callable
from .external import ExternalClient

def langgraph_tool(client: ExternalClient, action_for: Callable, call: Callable):
    """Use inside a LangGraph node/entrypoint with a persistent checkpointer."""
    import sys
    if sys.version_info < (3, 11): raise RuntimeError("The asynchronous LangGraph adapter requires Python 3.11 or newer")
    from langgraph.func import task
    from langgraph.types import interrupt

    @task
    async def attempt(input: Any, index: int):
        action = action_for(input)
        d = await client.prepare(action, f"langgraph:{index}")
        if d.get("enforced") and d["decision"] in ("REJECT", "REPLAN"): raise ValueError(d["reason_code"])
        return await client.execute(action, d, lambda: call(input))

    async def execute(input: Any):
        index = 0
        while True:
            result = await attempt(input, index)
            if result["status"] == "COMPLETED": return result.get("output")
            if result["status"] != "PENDING": raise ValueError(result["status"])
            interrupt({"type": "lumni", **result})
            index += 1
    return execute
