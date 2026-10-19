import pytest
from lumni import RunContext, Suspended
from lumni.langgraph import langgraph_tool
from lumni.openai import openai_tool

@pytest.mark.asyncio
async def test_native_replay_never_calls_external_function():
    class Runtime:
        async def begin(self, *_): return {"status":"COMPLETED","output":{"id":"saved"}}
    async def never(): raise AssertionError("Replayed callback executed")
    ctx=RunContext(Runtime(),{"token":"token","fence":1,"work_id":"work"})
    assert await ctx.llm("llm",{},never)=={"id":"saved"}

@pytest.mark.asyncio
async def test_native_timeout_suspends_without_retry():
    reports=[]
    class Runtime:
        async def begin(self,*_): return {"status":"READY","attempt":1}
        async def start(self,*_): pass
        async def result(self,*args): reports.append(args[-1]);return {"status":"WAITING"}
    async def uncertain(): raise TimeoutError()
    ctx=RunContext(Runtime(),{"token":"token","fence":1,"work_id":"work"})
    with pytest.raises(Suspended): await ctx.tool("refund",{"arguments":{}},uncertain)
    assert len(reports)==1 and reports[0]["status"]=="UNKNOWN" and not reports[0]["retryable"]

@pytest.mark.asyncio
async def test_langgraph_real_interrupt_and_resume():
    from langgraph.func import entrypoint
    from langgraph.checkpoint.memory import InMemorySaver
    from langgraph.types import Command
    calls=[]
    class External:
        checks=0
        async def prepare(self,*_):
            self.checks+=1
            return {"decision":"WAIT" if self.checks==1 else "ALLOW","enforced":True}
        async def execute(self,a,d,call): return {"status":"PENDING","decision":d} if d["decision"]=="WAIT" else {"status":"COMPLETED","output":await call()}
    async def tool(input): calls.append(input);return 42
    client=External()
    guarded=langgraph_tool(client,lambda _: {"action_id":"stable"},tool)
    @entrypoint(checkpointer=InMemorySaver())
    async def flow(input): return await guarded(input)
    config={"configurable":{"thread_id":"thread"}}
    await flow.ainvoke({},config)
    assert not calls
    assert await flow.ainvoke(Command(resume=True),config)==42
    assert len(calls)==1 and client.checks==2

@pytest.mark.asyncio
async def test_openai_function_tool_approval_boundary():
    from agents import function_tool, RunContextWrapper
    from agents.tool_context import ToolContext
    calls=[]
    @function_tool
    async def refund(amount:int)->str:
        """Refund the test amount."""
        calls.append(amount);return "ok"
    class External:
        ready=False
        async def prepare(self,*_): return {"decision":"ALLOW" if self.ready else "REQUIRE_APPROVAL","enforced":True}
        async def execute(self,a,d,call): return {"status":"COMPLETED","output":await call()}
    client=External()
    wrapped=openai_tool(refund,client,lambda *_: {"work_id":"work"})
    assert await wrapped.needs_approval(RunContextWrapper(None),{"amount":100},"call_1")
    assert not calls
    client.ready=True
    ctx=ToolContext(context=None,tool_name="refund",tool_call_id="call_1",tool_arguments='{"amount":100}')
    assert await wrapped.on_invoke_tool(ctx,'{"amount":100}')=="ok"
    assert calls==[100]

@pytest.mark.asyncio
async def test_external_result_errors_keep_their_recovery_meaning():
    from lumni.external import ExternalClient
    from lumni.client import LumniError
    class External(ExternalClient):
        error = "RESULT_PENDING"
        async def query(self, *_): return [{"status":"UNKNOWN"}]
        async def previous_result(self, *_): raise LumniError(self.error, "fixture", 422)
    client = External(None)
    assert await client.outcome("action", False) == {"status":"PENDING"}
    for code in ("RESULT_UNAVAILABLE", "UNAVAILABLE"):
        client.error = code
        with pytest.raises(LumniError) as caught: await client.outcome("action", False)
        assert caught.value.code == code

@pytest.mark.asyncio
async def test_openai_runner_serialized_approval_resumes_one_tool_call():
    from agents import Agent, Runner, RunConfig, RunState, function_tool
    from agents.models.interface import Model
    from agents.items import ModelResponse
    from agents.usage import Usage
    from openai.types.responses import ResponseFunctionToolCall, ResponseOutputMessage, ResponseOutputText
    calls, identities = [], []
    class FixtureModel(Model):
        calls = 0
        async def get_response(self, *args, **kwargs):
            self.calls += 1
            output = [ResponseFunctionToolCall(type="function_call", name="refund", call_id="refund-call", arguments='{"amount":100}')]
            if self.calls > 1:
                output = [ResponseOutputMessage(id="msg", type="message", role="assistant", status="completed", content=[ResponseOutputText(type="output_text", text="Refund recorded", annotations=[])])]
            return ModelResponse(output=output, usage=Usage(), response_id=None)
        async def stream_response(self, *args, **kwargs):
            raise AssertionError("Unexpected stream")
            yield
    class External:
        approved = False
        async def prepare(self, action, *_):
            identities.append(action["action_id"])
            return {"decision":"ALLOW" if self.approved else "REQUIRE_APPROVAL", "enforced":True}
        async def execute(self, action, decision, call):
            assert decision["decision"] == "ALLOW"
            return {"status":"COMPLETED", "output":await call()}
    @function_tool
    async def refund(amount: int) -> str:
        """Refund a fixture amount."""
        calls.append(amount)
        return "refunded"
    client, model = External(), FixtureModel()
    agent = Agent(name="refund-agent", model=model, tools=[openai_tool(refund, client, lambda *_: {"work_id":"persisted-work"})])
    config = RunConfig(tracing_disabled=True)
    paused = await Runner.run(agent, "Refund 100", run_config=config)
    assert len(paused.interruptions) == 1 and not calls
    restored = await RunState.from_string(agent, paused.to_state().to_string())
    restored.approve(restored.get_interruptions()[0])
    client.approved = True
    completed = await Runner.run(agent, restored, run_config=config)
    assert completed.final_output == "Refund recorded"
    assert calls == [100] and model.calls == 2
    assert set(identities) == {"persisted-work:refund-call"}
