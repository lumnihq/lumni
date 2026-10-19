import type { ActionInput, Decision, LumniClient } from './index.js';

export interface RunInput { id: string; actor_id: string; handler: string; handler_version: string; input?: unknown; goal?: string; parent_work_id?: string; handle_child_failure?: boolean; expected_effects?:Record<string,{verifier_id:string;expected:unknown}> }
export interface RunClaim { work_id: string; actor_id: string; handler: string; handler_version: string; input: unknown; token: string; fence: number; expires_at: string }
export interface RuntimeAuth { token: string; fence: number }
export interface WaitCondition { type: 'TIMER'|'SIGNAL'|'RESOURCE'|'WORK'|'APPROVAL'; delay_ms?:number; wake_at?: string; signal?: string; resource_id?: string; version?: string; work_id?: string; approval_id?: string }
export interface StepInput extends RuntimeAuth { key: string; type: 'LLM'|'TOOL'|'CUSTOM'|'WAIT'|'APPROVAL'|'AGENT'; input?: unknown; action?: ActionInput; wait?: WaitCondition; retry_policy?: {max_attempts?:number;initial_delay_ms?:number;max_delay_ms?:number;multiplier?:number;retry_on?:string[]} }
export interface DurableStep { id: string; work_id: string; key: string; type: string; status: string; attempt: number; output?: unknown; error?: string; replay?: boolean; decision?: Decision }
export interface StepResult extends RuntimeAuth { attempt?: number; status?: 'COMPLETED'|'FAILED'|'UNKNOWN'; output?: unknown; error?: string; retryable?: boolean; effect_absent?: boolean }
export interface Verifier {id:string;name:string;kind:'HTTP'|'POSTGRES'|'STRIPE'|'GITHUB'|'SHOPIFY'|'CUSTOM'|'EVENT'|'COMPOSITE';version:string;config:Record<string,unknown>;secret?:string;enabled?:boolean}
export interface VerificationEvidence {id:string;status:'UNKNOWN'|'PENDING'|'VERIFIED'|'MISMATCH'|'PARTIAL'|'FAILED';observed?:unknown;external_result_id?:string;effect_absent?:boolean;prior_attempt_terminated?:boolean;reason?:string}
export type VerifiedAction = ActionInput & {verification?: {verifier_id:string;expected:unknown};step_id?:string};

const enc = encodeURIComponent;
export interface ToolMapping {source:string;tool:string;operation:string;resources:Array<{type:string;mode:'READ'|'WRITE'|'CREATE'|'DELETE'|'OBSERVE';id_pointer:string;namespace?:string;observed_version_pointer?:string;version_source?:string}>;effect_key_fields?:string[]}
export interface ReconciliationInput {id:string;outcome_id:string;handler:string;handler_version:string;input?:unknown}
export class RuntimeClient {
  constructor(public client:LumniClient) {}
  confirmMapping(input:ToolMapping){return this.client.request('POST','/v1/discovery/confirm',input,true)}
  enforcement(operation:string,rule:string,mode:'SHADOW'|'WARN'|'ENFORCE'){return this.client.request('PUT','/v1/config/enforcement',{operation,rule,mode},true)}
  retryVerification(outcome:string){return this.client.request('POST',`/v1/outcomes/${enc(outcome)}/retry`,{},true)}
  discover(source:string,name:string,schema:Record<string,unknown>){return this.client.request('POST','/v1/discovery/tools',{source,name,schema},true)}
  registerReconciliation(name:string,version:string){return this.client.request('POST','/v1/reconciliation/handlers',{name,version},true)}
  reconcile(input:ReconciliationInput){return this.client.request<{work_id:string}>('POST','/v1/reconciliation',input,true)}
  resolveEffectIdentity(legacy_key:string,current_key:string,evidence:unknown){return this.client.request('POST','/v1/effects/resolve-identity',{legacy_key,current_key,evidence},true)}
  submit(input:RunInput) {return this.client.request<{id:string}>('POST','/v1/runtime/work',input,true)}
  claim(worker_id:string,handlers:Record<string,string>) {return this.client.request<{claim:RunClaim|null}>('POST','/v1/runtime/claim',{worker_id,handlers},false)}
  heartbeat(work:string,auth:RuntimeAuth) {return this.client.request('POST',`/v1/runtime/work/${enc(work)}/heartbeat`,auth,true)}
  begin(work:string,input:StepInput) {return this.client.request<DurableStep>('POST',`/v1/runtime/work/${enc(work)}/steps/begin`,input,true)}
  start(work:string,key:string,input:StepResult) {return this.client.request('POST',`/v1/runtime/work/${enc(work)}/steps/${enc(key)}/start`,input,true)}
  result(work:string,key:string,input:StepResult) {return this.client.request<DurableStep>('POST',`/v1/runtime/work/${enc(work)}/steps/${enc(key)}/result`,input,true)}
  finish(work:string,input:StepResult) {return this.client.request('POST',`/v1/runtime/work/${enc(work)}/finish`,input,true)}
  cancel(work:string) {return this.client.request('POST',`/v1/runtime/work/${enc(work)}/cancel`,{},true)}
  signal(work:string,name:string,payload:unknown,id=crypto.randomUUID()) {return this.client.request('POST',`/v1/runtime/work/${enc(work)}/signals`,{id,name,payload},true)}
  verifier(input:Verifier) {return this.client.request<Verifier>('POST','/v1/verifiers',input,true)}
  evidence(outcome:string,input:VerificationEvidence) {return this.client.request('POST',`/v1/outcomes/${enc(outcome)}/evidence`,input,true)}
  bindOperationVerifier(operation:string,verifierID:string,expectedTemplate:Record<string,unknown>){return this.client.request('PUT','/v1/config/operation-verifiers',{operation,verifier_id:verifierID,expected_template:expectedTemplate},true)}
  recheck(action:string,id:string=crypto.randomUUID()) {return this.client.request<Decision>('POST',`/v1/actions/${enc(action)}/recheck`,{id},true)}
  previousResult<T>(action:string) {return this.client.request<{output:T}>('GET',`/v1/actions/${enc(action)}/previous-result`,undefined,true)}
  query<T=Record<string,unknown>>(collection:string,id='',work='') {return this.client.request<{items:T[]}>('GET',`/v1/${enc(collection)}${id?'/'+enc(id):''}?work_id=${enc(work)}`,undefined,true)}
  dependency(input:{id:string;source_id:string;target_id:string;entity_type:'work'|'action';relationship:'DEPENDS_ON'|'SUPERSEDES'}) {return this.client.request('POST','/v1/dependencies',input,true)}
}
export class Suspended extends Error {constructor(){super('Work suspended durably')}}
export class StepFailure extends Error {constructor(public step:DurableStep){super(step.error||`Step ${step.status}`)}}
export class SafeFailure extends Error {constructor(public code:string,public retryable=false){super(code)}}
export type AgentHandler = (ctx:RunContext,input:unknown)=>Promise<unknown>;

export class RunContext {
  private executing=false;
  constructor(public runtime:RuntimeClient,public claim:RunClaim,public signal:AbortSignal) {}
  get auth():RuntimeAuth {return {token:this.claim.token,fence:this.claim.fence}}
  llm<T>(key:string,input:unknown,call:()=>Promise<T>) {return this.execute<T>(key,'LLM',input,undefined,call)}
  step<T>(key:string,input:unknown,call:()=>Promise<T>) {return this.execute<T>(key,'CUSTOM',input,undefined,call)}
  tool<T>(key:string,action:VerifiedAction,call:()=>Promise<T>) {return this.execute<T>(key,'TOOL',action.arguments,action,call)}
  private async execute<T>(key:string,type:StepInput['type'],input:unknown,action:ActionInput|undefined,call:()=>Promise<T>):Promise<T> {
    if(this.executing) throw new Error('Use child work for concurrent steps'); this.executing=true;
    try {
      this.signal.throwIfAborted();
      let st=await this.runtime.begin(this.claim.work_id,{...this.auth,key,type,input,action});
      if(st.status==='COMPLETED') return st.output as T;
      if(st.status==='WAITING'||st.status==='UNKNOWN') throw new Suspended();
      if(st.status!=='READY') throw new StepFailure(st);
      await this.runtime.start(this.claim.work_id,key,{...this.auth,attempt:st.attempt});
      this.signal.throwIfAborted();
      let result:StepResult={...this.auth,attempt:st.attempt,status:'COMPLETED'};
      try {result.output=await call()} catch(error) {
        result.status=type==='TOOL'&&!(error instanceof SafeFailure)?'UNKNOWN':'FAILED';
        result.error=error instanceof SafeFailure?error.code:'STEP_EXECUTION_FAILED';
        result.effect_absent=error instanceof SafeFailure;
        result.retryable=error instanceof SafeFailure?error.retryable:type==='LLM';
      }
      st=await this.runtime.result(this.claim.work_id,key,result);
      if(st.status==='WAITING'||st.status==='UNKNOWN') throw new Suspended();
      if(st.status!=='COMPLETED') throw new StepFailure(st);
      return st.output as T;
    } finally {this.executing=false}
  }
  async wait<T=unknown>(key:string,condition:WaitCondition):Promise<T> {
    const st=await this.runtime.begin(this.claim.work_id,{...this.auth,key,type:'WAIT',wait:condition});
    if(st.status==='COMPLETED') return st.output as T;
    if(st.status==='FAILED') throw new StepFailure(st);
    throw new Suspended();
  }
  sleep(key:string,delayMS:number) {return this.wait(key,{type:'TIMER',delay_ms:delayMS})}
  approval(key:string,id:string) {return this.wait(key,{type:'APPROVAL',approval_id:id})}
  async child<T>(key:string,handler:string,version:string,input:unknown):Promise<T> {
    const id=`${this.claim.work_id}:${key}`;
    await this.runtime.submit({id,actor_id:this.claim.actor_id,parent_work_id:this.claim.work_id,handler,handler_version:version,input,goal:key});
    return this.wait<T>(key,{type:'WORK',work_id:id});
  }
  handoff<T>(key:string,handler:string,version:string,input:unknown) {return this.child<T>(key,handler,version,input)}
}
export class Worker {
  private handlers=new Map<string,{version:string;handler:AgentHandler}>();
  constructor(public runtime:RuntimeClient,public id=`worker_${crypto.randomUUID()}`) {}
  register(name:string,version:string,handler:AgentHandler) {this.handlers.set(name,{version,handler});return this}
  async runOnce(signal?:AbortSignal):Promise<boolean> {
    const {claim}=await this.runtime.claim(this.id,Object.fromEntries([...this.handlers].map(([k,v])=>[k,v.version])));
    if(!claim) return false;
    const controller=new AbortController();const abort=()=>controller.abort(signal?.reason);signal?.addEventListener('abort',abort,{once:true});
    const ctx=new RunContext(this.runtime,claim,controller.signal);
    const heartbeat=setInterval(()=>{void this.runtime.heartbeat(claim.work_id,ctx.auth).catch(error=>controller.abort(error))},10000);
    try {
      let output:unknown;
      try {output=await this.handlers.get(claim.handler)!.handler(ctx,claim.input)} catch(error) {
        if(error instanceof Suspended) return true;
        if(!controller.signal.aborted) await this.runtime.finish(claim.work_id,{...ctx.auth,status:'FAILED',error:'HANDLER_FAILED'});
        throw error;
      }
      controller.signal.throwIfAborted();
      await this.runtime.finish(claim.work_id,{...ctx.auth,status:'COMPLETED',output});return true;
    } finally {clearInterval(heartbeat);signal?.removeEventListener('abort',abort)}
  }
  async run(signal?:AbortSignal) {while(!signal?.aborted){if(!await this.runOnce(signal)) await new Promise(resolve=>setTimeout(resolve,1000))}}
}
