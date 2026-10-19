import asyncio
import os
import uuid
import pytest
from lumni import Client, RuntimeClient, Worker

@pytest.mark.asyncio
@pytest.mark.skipif(not os.getenv("LUMNI_TEST_URL"), reason="live API not configured")
async def test_native_python_worker_restarts_with_independent_evidence():
    client=Client(os.environ["LUMNI_TEST_URL"],os.environ["LUMNI_TEST_API_KEY"])
    runtime=RuntimeClient(client)
    id="py-live-"+str(uuid.uuid4())
    await runtime.request("POST","/v1/actors",{"id":id,"type":"AGENT"})
    await runtime.verifier({"id":id,"name":"Python fixture event","kind":"EVENT","version":"1","enabled":True,"config":{}})
    await runtime.submit({"id":id,"actor_id":id,"goal":"SDK live recovery","handler":id,"handler_version":"1","input":{"private":"fixture"},"expected_effects":{"issue":{"verifier_id":id,"expected":{"record":id}}}})
    counts={"llm":0,"tool":0,"passes":0}
    async def handler(ctx, input):
        counts["passes"]+=1
        async def llm(): counts["llm"]+=1;return {"answer":42}
        assert await ctx.llm("reason",{"prompt":"fixture"},llm)=={"answer":42}
        await ctx.wait("continue",{"type":"SIGNAL","signal":"continue"})
        async def issue(): counts["tool"]+=1;return {"record":id}
        return await ctx.tool("issue",{"operation":"fixture.issue","resources":[{"type":"fixture","external_id":id,"mode":"WRITE"}],"arguments":{"record":id},"verification":{"verifier_id":id,"expected":{"record":id}}},issue)
    def worker(): return Worker(runtime).register(id,"1",handler)
    await worker().run_once()
    assert counts["llm"]==1 and counts["tool"]==0
    await runtime.signal(id,"continue",True,"continue-"+id)
    complete=evidence=False
    for _ in range(150):
        await worker().run_once()
        outcomes=await runtime.query("outcomes",work=id)
        if counts["tool"]==1 and not evidence and outcomes and outcomes[0]["status"]=="PENDING":
            await runtime.evidence(outcomes[0]["id"],{"id":"callback-"+id,"status":"VERIFIED","observed":{"record":id},"external_result_id":id})
            evidence=True
        work=await runtime.query("activity",id)
        if work and work[0]["status"]=="COMPLETED": complete=True;break
        await asyncio.sleep(.1)
    assert complete and evidence and counts["llm"]==1 and counts["tool"]==1 and counts["passes"]>=3
