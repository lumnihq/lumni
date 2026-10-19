export type DecisionType = "ALLOW"|"WARN"|"WAIT"|"REPLAN"|"DEDUPLICATE"|"REQUIRE_APPROVAL"|"REJECT"|"WOULD_ALLOW"|"WOULD_WAIT"|"WOULD_REPLAN"|"WOULD_DEDUPLICATE"|"WOULD_REQUIRE_APPROVAL"|"WOULD_REJECT";
export type AccessMode = "OBSERVE"|"READ"|"WRITE"|"CREATE"|"DELETE"|"IMPACT";
export type RiskLevel = "LOW"|"MEDIUM"|"HIGH"|"CRITICAL";
export interface ResourceIntent {id?:string;type:string;external_id?:string;namespace?:string;mode:AccessMode;operation?:string;observed_version?:string;expected_version?:string;version_source?:string;confidence?:number;evidence_type?:string}
export interface ActionInput {verification?:{verifier_id:string;expected:unknown};step_id?:string;schema_version?:string;action_id?:string;work_id:string;actor_id:string;session_id?:string;operation:string;tool?:string;source?:string;arguments?:unknown;resources?:ResourceIntent[];idempotency_key?:string;effect_key?:string;risk?:RiskLevel;metadata?:unknown}
export interface LeaseGrant {resource_id:string;fencing_token:number;expires_at:string}
export interface Decision {schema_version:string;action_id:string;receipt_id:string;decision:DecisionType;reason_code:string;enforced:boolean;confidence:number;retry_after_ms?:number;lease_token?:string;expires_at?:string;leases?:LeaseGrant[];approval_id?:string;existing_action_id?:string;conflicting_action_ids?:string[]}
export interface WorkInput {id?:string;actor_id:string;agent_id?:string;session_id?:string;parent_work_id?:string;goal:string;priority?:number;risk?:RiskLevel;metadata?:unknown}
export interface ObservationInput {step_id?:string;id?:string;work_id:string;session_id?:string;resource:{id?:string;type:string;external_id?:string;namespace?:string};version:string;version_source:string;observed_at?:string;confidence?:number;evidence_type?:string;metadata?:unknown}
export interface ResultInput {output?:unknown;status:"SUCCEEDED"|"FAILED"|"UNKNOWN";lease_token?:string;external_result_id?:string;resource_versions?:Record<string,string>;retryable?:boolean;result_metadata?:unknown}
export interface LumniContext {actor_id?:string;agent_id?:string;session_id?:string;work_id?:string}

export class LumniError extends Error {constructor(public code:string,message:string,public status:number,public details?:unknown){super(message)}}

export class LumniClient {
  constructor(public baseURL:string,private apiKey:string,private tenantID?:string,private fetcher:typeof fetch=fetch) {this.baseURL=baseURL.replace(/\/$/,"")}
  startWork(input:WorkInput){return this.request<Record<string,unknown>>("POST","/v1/work",{...input,id:input.id??`work_${crypto.randomUUID()}`},true)}
  completeWork(id:string){return this.request("POST",`/v1/work/${encodeURIComponent(id)}/complete`,{},true)}
  observe(input:ObservationInput){return this.request<{id:string}>("POST","/v1/observations",{...input,id:input.id??crypto.randomUUID()},true)}
  updateResourceVersion(id:string,input:{version:string;version_source:string;observed_at?:string;source?:string;sequence?:number}){return this.request("POST",`/v1/resources/${encodeURIComponent(id)}/version`,input,true)}
  checkAction(input:ActionInput){return this.request<Decision>("POST","/v1/actions/check",{...input,action_id:input.action_id??`act_${crypto.randomUUID()}`},true)}
  observeAction(input:ActionInput){return this.request<Decision>("POST","/v1/actions/observe",{...input,action_id:input.action_id??`act_${crypto.randomUUID()}`},true)}
  startAction(id:string,leaseToken?:string){return this.request("POST",`/v1/actions/${encodeURIComponent(id)}/start`,{lease_token:leaseToken??""},true)}
  heartbeat(id:string,leaseToken:string){return this.request<{expires_at:string}>("POST",`/v1/actions/${encodeURIComponent(id)}/heartbeat`,{lease_token:leaseToken},true)}
  reportResult(id:string,input:ResultInput){return this.request("POST",`/v1/actions/${encodeURIComponent(id)}/result`,input,true)}
  approval(id:string){return this.request<Record<string,unknown>>("GET",`/v1/approvals/${encodeURIComponent(id)}`,undefined,true)}
  ingestEvent(input:Record<string,unknown>){return this.request<{created:boolean}>("POST","/v1/events",{schema_version:"1.0",event_id:`evt_${crypto.randomUUID()}`,occurred_at:new Date().toISOString(),...input},true)}
  recordCost(input:Record<string,unknown>){return this.request<{id:string}>("POST","/v1/costs",{id:`cost_${crypto.randomUUID()}`,...input},true)}
  async request<T=unknown>(method:string,path:string,input?:unknown,retryable=false):Promise<T>{const body=input===undefined?undefined:JSON.stringify(input);const attempts=retryable?3:1;const requestID=`req_${crypto.randomUUID()}`;let last:unknown;for(let attempt=0;attempt<attempts;attempt++){try{const headers:Record<string,string>={Authorization:`Bearer ${this.apiKey}`,"X-Request-ID":requestID};if(body!==undefined)headers["Content-Type"]="application/json";if(this.tenantID)headers["X-Lumni-Tenant"]=this.tenantID;const response=await this.fetcher(this.baseURL+path,{method,headers,body});const data=await response.json().catch(()=>({}));if(response.ok)return data as T;const error=(data as {error?:{code?:string;message?:string;details?:unknown}}).error;last=new LumniError(error?.code??"HTTP_ERROR",error?.message??`HTTP ${response.status}`,response.status,error?.details);if(response.status<500)throw last}catch(error){if(error instanceof LumniError&&error.status<500)throw error;last=error}if(attempt+1<attempts)await new Promise(resolve=>setTimeout(resolve,50*(attempt+1)))}throw last}
}

export function shouldExecute(decision:Decision):boolean{return ["ALLOW","WARN","WOULD_ALLOW","WOULD_WAIT","WOULD_REPLAN","WOULD_DEDUPLICATE","WOULD_REQUIRE_APPROVAL","WOULD_REJECT"].includes(decision.decision)}
export function withContext(input:ActionInput,context:LumniContext):ActionInput{return {...input,actor_id:context.actor_id??input.actor_id,session_id:context.session_id??input.session_id,work_id:context.work_id??input.work_id,metadata:{...((input.metadata as Record<string,unknown>|undefined)??{}),lumni_context:context}}}
export function contextHeaders(context:LumniContext):Record<string,string>{return {"X-Lumni-Actor":context.actor_id??"","X-Lumni-Agent":context.agent_id??"","X-Lumni-Session":context.session_id??"","X-Lumni-Work":context.work_id??""}}

export * from "./runtime.js";

export * from "./external.js";
