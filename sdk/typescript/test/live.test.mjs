import {test} from 'node:test';
import assert from 'node:assert/strict';
const {LumniClient, RuntimeClient, Worker} = await import(process.env.LUMNI_TEST_SDK_MODULE || '../dist/index.js');

test('native TypeScript worker resumes through real APIs and independent evidence', {skip:!process.env.LUMNI_TEST_URL}, async()=>{
  const client=new LumniClient(process.env.LUMNI_TEST_URL,process.env.LUMNI_TEST_API_KEY);
  const runtime=new RuntimeClient(client),id='ts-live-'+crypto.randomUUID(),handlerName=id+'-handler';
  await client.request('POST','/v1/actors',{id,type:'AGENT'},true);
  await runtime.verifier({id,name:'TypeScript fixture event',kind:'EVENT',version:'1',enabled:true,config:{}});
  await runtime.submit({id,actor_id:id,goal:'SDK live recovery',handler:handlerName,handler_version:'1',input:{private:'fixture'},expected_effects:{issue:{verifier_id:id,expected:{record:id}}}});
  let llmCalls=0,toolCalls=0,passes=0;
  const handler=async ctx=>{
    passes++;
    const saved=await ctx.llm('reason',{prompt:'fixture'},async()=>{llmCalls++;return {answer:42}});
    assert.equal(saved.answer,42);
    await ctx.wait('continue',{type:'SIGNAL',signal:'continue'});
    return ctx.tool('issue',{operation:'fixture.issue',resources:[{type:'fixture',external_id:id,mode:'WRITE'}],arguments:{record:id},verification:{verifier_id:id,expected:{record:id}}},async()=>{toolCalls++;return {record:id}});
  };
  const newWorker=()=>new Worker(runtime).register(handlerName,'1',handler);
  await newWorker().runOnce();assert.equal(llmCalls,1);assert.equal(toolCalls,0);
  await runtime.signal(id,'continue',true,'continue-'+id);
  let complete=false,evidence=false;
  for(let n=0;n<150;n++){
    await newWorker().runOnce();
    const {items:outcomes}=await runtime.query('outcomes','',id);
    if(toolCalls===1&&!evidence&&outcomes[0]?.status==='PENDING'){
      await runtime.evidence(outcomes[0].id,{id:'callback-'+id,status:'VERIFIED',observed:{record:id},external_result_id:id});evidence=true;
    }
    const {items}=await runtime.query('activity',id);
    if(items[0]?.status==='COMPLETED'){complete=true;break}
    await new Promise(resolve=>setTimeout(resolve,100));
  }
  assert.equal(complete,true);assert.equal(evidence,true);assert.equal(llmCalls,1);assert.equal(toolCalls,1);assert.ok(passes>=3);
});
