import type { FunctionTool } from '@openai/agents';
import { ExternalClient, AdmissionPending } from './external.js';
import { VerifiedAction } from './runtime.js';
import { shouldExecute } from './index.js';

// Use the Agents SDK's needsApproval/RunState boundary. Preserve the RunState
// before resuming; AdmissionPending leaves the same tool call safe to resume.
export function openaiTool<T extends FunctionTool<any,any,any>>(tool:T,client:ExternalClient,map:(input:unknown,callId:string)=>VerifiedAction):T{
  const action=(input:unknown,id?:string)=>{if(!id)throw new Error('A tool call ID is required for durable correlation');const a=map(input,id);return {...a,action_id:a.action_id||`${a.work_id}:${id}`};};
  return {...tool,errorFunction:null,timeoutBehavior:'raise_exception',
    needsApproval:async(ctx:any,input:any,id?:string)=>{
      const d=await client.prepare(action(input,id));
      return (!shouldExecute(d)&&d.decision!=='DEDUPLICATE')||await tool.needsApproval(ctx,input,id);
    },
    invoke:async(ctx:any,raw:string,details?:any)=>{
      const a=action(JSON.parse(raw),details?.toolCall?.callId);
      const d=await client.prepare(a,crypto.randomUUID());
      const result=await client.execute(a,d,()=>tool.invoke(ctx,raw,details));
      if(result.status!=='COMPLETED')throw new AdmissionPending(result);
      return result.output;
    }
  } as T;
}
