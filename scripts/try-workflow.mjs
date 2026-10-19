#!/usr/bin/env node
import assert from 'node:assert/strict';
import {createServer} from 'node:http';
import {readFile} from 'node:fs/promises';
import {parseArgs} from 'node:util';
import {randomUUID} from 'node:crypto';

const {values}=parseArgs({options:{credentials:{type:'string'},url:{type:'string'},help:{type:'boolean'}}});
if(values.help){console.log('node scripts/try-workflow.mjs [--credentials PATH] [--url URL]\nUses LUMNI_URL/LUMNI_API_KEY or the development service at localhost:8080.\nCreates identified demo records and one operation-specific enforcement rule. No real provider calls.');process.exit(0)}
const credentials=values.credentials?JSON.parse(await readFile(values.credentials,'utf8')):undefined;
const origin=(values.url||process.env.LUMNI_URL||(credentials?`http://127.0.0.1:${credentials.port}`:'http://127.0.0.1:8080')).replace(/\/$/,'');
const key=process.env.LUMNI_API_KEY||credentials?.admin_key||'lumni_local_change_me';
async function request(method,path,body){const response=await fetch(origin+path,{method,headers:{Authorization:`Bearer ${key}`,'Content-Type':'application/json'},body:body===undefined?undefined:JSON.stringify(body),signal:AbortSignal.timeout(15000)});const data=await response.json();if(!response.ok)throw Error(`${method} ${path}: ${data.error?.message||response.status}`);return data}

// This ledger is a deterministic external-system fixture, separate from Lumni.
const records=new Map();let writes=0,reads=0;
const ledger=createServer(async(req,res)=>{res.setHeader('Content-Type','application/json');if(req.method==='POST'&&req.url==='/refunds'){let body='';for await(const chunk of req)body+=chunk;const refund={...JSON.parse(body),id:'re_'+randomUUID(),status:'succeeded'};writes++;records.set(refund.payment_id,refund);res.end(JSON.stringify(refund));}else if(req.method==='GET'){reads++;const record=records.get(decodeURIComponent(req.url.slice(1)));res.writeHead(record?200:404);res.end(JSON.stringify(record||{error:'missing'}));}else{res.writeHead(405).end('{}')}});
await new Promise((ok,fail)=>{ledger.once('error',fail);ledger.listen(0,'127.0.0.1',ok)});
const fixtureURL=`http://127.0.0.1:${ledger.address().port}`;
try{
  await request('GET','/v1/health/ready');
  const id='try-'+randomUUID(),operation=`demo.refund.${id}`,work=id+'-work',payment=id+'-payment',verifier=id+'-readback';
  console.log('Using real Lumni APIs and PostgreSQL with a local fixture payment ledger.');
  await request('POST','/v1/actors',{id,type:'AGENT',display_name:'Quick-start refund agent',runtime:'node'});
  await request('POST','/v1/work',{id:work,actor_id:id,goal:'Refund once, prevent a duplicate, verify the amount',risk:'LOW'});
  await request('POST','/v1/verifiers',{id:verifier,name:'Quick-start ledger observer',kind:'EVENT',version:'1',enabled:true,config:{}});
  await request('PUT','/v1/config/enforcement',{operation,mode:'ENFORCE',rule:'*'});
  const expected={payment_id:payment,amount:10000,currency:'usd',status:'succeeded'};
  const intent={work_id:work,actor_id:id,operation,arguments:{payment_id:payment,amount:10000,currency:'usd'},resources:[{type:'demo_payment',external_id:payment,mode:'WRITE'}],verification:{verifier_id:verifier,expected},risk:'LOW'};
  const first=await request('POST','/v1/actions/check',{...intent,action_id:id+'-first'});
  assert.equal(first.decision,'ALLOW');assert.equal(first.enforced,true);
  console.log('1. First refund admitted.');
  await request('POST',`/v1/actions/${first.action_id}/start`,{lease_token:first.lease_token});
  const external=await fetch(fixtureURL+'/refunds',{method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify(intent.arguments)});
  const refund=await external.json();
  await request('POST',`/v1/actions/${first.action_id}/result`,{status:'SUCCEEDED',lease_token:first.lease_token,external_result_id:refund.id,output:refund});
  let outcome=(await request('GET',`/v1/outcomes?work_id=${work}`)).items.find(row=>row.action_id===first.action_id);
  assert.ok(outcome&&outcome.status!=='VERIFIED');
  console.log('2. Ledger changed once. Tool success is still unverified.');
  async function verify(outcomeID){const readback=await (await fetch(fixtureURL+'/'+encodeURIComponent(payment))).json();for(const [field,value] of Object.entries(expected))assert.equal(readback[field],value);await request('POST',`/v1/outcomes/${outcomeID}/evidence`,{id:outcomeID+'-observer',status:'VERIFIED',observed:readback,external_result_id:readback.id});}
  await verify(outcome.id);
  const second=await request('POST','/v1/actions/check',{...intent,action_id:id+'-second'});
  assert.equal(second.decision,'DEDUPLICATE');assert.equal(second.enforced,true);
  console.log('3. Duplicate refund stopped before a second ledger call.');
  const outcomes=(await request('GET',`/v1/outcomes?work_id=${work}`)).items;
  const duplicate=outcomes.find(row=>row.action_id===second.action_id);if(duplicate)await verify(duplicate.id);
  outcome=(await request('GET',`/v1/outcomes/${outcome.id}`)).items[0];assert.equal(outcome.status,'VERIFIED');assert.equal(writes,1);assert.ok(reads>=1);
  await request('POST',`/v1/work/${work}/complete`,{});
  console.log(`4. Independent ledger reads confirmed $100.00 USD. External mutations: ${writes}.`);
  console.log(`\nPASS · Open the real work timeline:\n${origin}/console/activity/${work}\n\nFindings: ${origin}/console/findings\nOutcomes: ${origin}/console/outcomes\nThis fixture uses authenticated observer evidence. Production refunds need an authoritative provider verifier.`);
}catch(error){console.error('Workflow check failed:',error.message);console.error('Start the service first, or pass --credentials with the file printed by the local launcher.');process.exitCode=1}finally{await new Promise(resolve=>ledger.close(resolve))}
