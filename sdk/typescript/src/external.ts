import { LumniError, Decision, shouldExecute } from './index.js';
import { RuntimeClient, VerifiedAction } from './runtime.js';
export interface ExternalResult<T=unknown>{status:string;output?:T;decision?:Decision}
export class AdmissionPending extends Error{constructor(public result:ExternalResult){super('Lumni is awaiting admission or authoritative verification')}}
export class ExternalClient extends RuntimeClient {
  async prepare(action:VerifiedAction,recheckID=''):Promise<Decision>{
    if(!action.action_id) throw new Error('A stable action_id is required');
    await this.client.request('POST','/v1/actors',{id:action.actor_id,type:'AGENT',runtime:'external'},true);
    await this.client.startWork({id:action.work_id,actor_id:action.actor_id,goal:action.operation});
    const d=await this.client.checkAction(action);
    if(recheckID && (['WAIT','REQUIRE_APPROVAL'].includes(d.decision)||(d.expires_at&&Date.parse(d.expires_at)<Date.now())))return this.recheck(d.action_id,recheckID);
    return d;
  }
  async outcome<T>(action:string,verified:boolean):Promise<ExternalResult<T>>{
    const {items}=await this.query<{status:string;observed:T;verifier_id?:string}>('outcomes','out_'+action);
    const o=items[0];if(o&&['FAILED','MISMATCH','PARTIAL'].includes(o.status))return {status:o.status};
    if((verified||o?.verifier_id)&&o?.status!=='VERIFIED')return {status:'PENDING'};
    try {return {status:'COMPLETED',output:(await this.previousResult<T>(action)).output};}catch(error){if(error instanceof LumniError && error.code==='RESULT_PENDING')return {status:'PENDING'};throw error;}
  }
  async execute<T>(action:VerifiedAction,d:Decision,call:()=>Promise<T>):Promise<ExternalResult<T>>{
    if(d.decision==='DEDUPLICATE'){if(!d.existing_action_id)throw new Error('RESULT_UNAVAILABLE');return this.outcome<T>(d.existing_action_id,!!action.verification);}
    if(!shouldExecute(d))return {status:'PENDING',decision:d};
    const {started}=await this.client.request<{started:boolean}>('POST',`/v1/actions/${encodeURIComponent(d.action_id)}/start-once`,{lease_token:d.lease_token||''},false);
    if(!started)return this.outcome<T>(d.action_id,!!action.verification);
    let output:T;try{output=await call();}catch{await this.client.reportResult(d.action_id,{status:'UNKNOWN',lease_token:d.lease_token});return {status:'PENDING'};}
    await this.client.reportResult(d.action_id,{status:'SUCCEEDED',output,lease_token:d.lease_token});
    return this.outcome<T>(d.action_id,!!action.verification);
  }
}
