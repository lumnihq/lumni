import {test} from 'node:test';
import assert from 'node:assert/strict';
import {RunContext,Suspended,ExternalClient,LumniClient,LumniError} from '../dist/index.js';
import {langgraphTool} from '../dist/langgraph.js';
import {openaiTool} from '../dist/openai.js';
import {entrypoint,MemorySaver,Command} from '@langchain/langgraph';
import {tool,RunContext as AgentsContext,Agent,Runner,RunState,Usage} from '@openai/agents';
import {z} from 'zod';
const claim={work_id:'work',actor_id:'actor',token:'token',fence:1};
test('stored native outputs replay without executing the callback',async()=>{
 let calls=0;const ctx=new RunContext({begin:async()=>({status:'COMPLETED',output:{id:'saved'}})},claim,new AbortController().signal);
 assert.deepEqual(await ctx.llm('reason',{},async()=>{calls++;return {}}),{id:'saved'});assert.equal(calls,0);
});
test('unknown native tool result suspends and does not retry',async()=>{
 let calls=0,reported;const runtime={begin:async()=>({status:'READY',attempt:1}),start:async()=>{},result:async(_w,_k,r)=>{reported=r;return {status:'WAITING'}}};
 const ctx=new RunContext(runtime,claim,new AbortController().signal);
 await assert.rejects(ctx.tool('refund',{arguments:{}},async()=>{calls++;throw Error('lost transport')}),Suspended);
 assert.equal(calls,1);assert.equal(reported.status,'UNKNOWN');assert.equal(reported.retryable,false);
});
test('LangGraph owns suspension and resumes with one external call',async()=>{
 let checks=0,calls=0;
 const client={prepare:async()=>({decision:checks++?'ALLOW':'WAIT',enforced:true}),execute:async(_a,d,call)=>d.decision==='WAIT'?{status:'PENDING',decision:d}:{status:'COMPLETED',output:await call()}};
 const guarded=langgraphTool(client,()=>({action_id:'stable'}),async()=>{calls++;return 42});
 const flow=entrypoint({name:'test',checkpointer:new MemorySaver()},guarded);
 const config={configurable:{thread_id:'thread'}};
 await flow.invoke({},config);assert.equal(calls,0);
 const result=await flow.invoke(new Command({resume:true}),config);
 assert.equal(result,42);assert.equal(calls,1);assert.equal(checks,2);
});
test('OpenAI tool approval and invoke wrappers gate the real FunctionTool',async()=>{
 let calls=0,ready=false;const original=tool({name:'refund',description:'Refund test',parameters:z.object({amount:z.number()}),execute:async()=>{calls++;return 'ok'}});
 const client={prepare:async()=>({decision:ready?'ALLOW':'REQUIRE_APPROVAL',enforced:true}),execute:async(_a,d,call)=>d.decision==='ALLOW'?{status:'COMPLETED',output:await call()}:{status:'PENDING',decision:d}};
 const wrapped=openaiTool(original,client,()=>({work_id:'work'}));const ctx=new AgentsContext({});
 assert.equal(await wrapped.needsApproval(ctx,{amount:100},'call_1'),true);assert.equal(calls,0);
 ready=true;assert.equal(await wrapped.invoke(ctx,'{"amount":100}',{toolCall:{callId:'call_1'}}),'ok');assert.equal(calls,1);
});

test('external recovery distinguishes pending results, expired payloads and API outages',async()=>{
  const client=new ExternalClient(new LumniClient('http://unused','test'));
  client.query=async()=>({items:[{status:'UNKNOWN'}]});
  client.previousResult=async()=>{throw new LumniError('RESULT_PENDING','verification pending',422)};
  assert.deepEqual(await client.outcome('action',false),{status:'PENDING'});
  client.previousResult=async()=>{throw new LumniError('RESULT_UNAVAILABLE','retained payload expired',422)};
  await assert.rejects(client.outcome('action',false),e=>e.code==='RESULT_UNAVAILABLE');
  client.previousResult=async()=>{throw new LumniError('UNAVAILABLE','offline',503)};
  await assert.rejects(client.outcome('action',false),e=>e.code==='UNAVAILABLE');
});

test('OpenAI Runner serializes approval state and resumes the same tool call',async()=>{
  let modelCalls=0,toolCalls=0,approved=false;
  const identities=[];
  const model={
    async getResponse(){return {usage:new Usage(),output:modelCalls++===0
      ?[{type:'function_call',name:'refund',callId:'refund-call',arguments:'{"amount":100}',status:'completed'}]
      :[{type:'message',role:'assistant',status:'completed',content:[{type:'output_text',text:'Refund recorded',annotations:[]}]}]};},
    async *getStreamedResponse(){throw Error('Unexpected stream')}
  };
  const client={prepare:async(a,recheck)=>{identities.push(a.action_id);return {decision:approved?'ALLOW':'REQUIRE_APPROVAL',enforced:true}},execute:async(a,d,call)=>{assert.equal(d.decision,'ALLOW');return {status:'COMPLETED',output:await call()}}};
  const guarded=openaiTool(tool({name:'refund',description:'Refund fixture',parameters:z.object({amount:z.number()}),execute:async()=>{toolCalls++;return 'refunded'}}),client,()=>({work_id:'persisted-work'}));
  const agent=new Agent({name:'refund-agent',model,tools:[guarded]});
  const runner=new Runner({tracingDisabled:true});
  const paused=await runner.run(agent,'Refund 100');
  assert.equal(paused.interruptions.length,1);assert.equal(toolCalls,0);
  const restored=await RunState.fromString(agent,paused.state.toString());
  restored.approve(restored.getInterruptions()[0]);approved=true;
  const completed=await runner.run(agent,restored);
  assert.equal(completed.finalOutput,'Refund recorded');assert.equal(toolCalls,1);assert.equal(modelCalls,2);
  assert.deepEqual([...new Set(identities)],['persisted-work:refund-call']);
});
