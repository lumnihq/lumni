import { task, interrupt } from '@langchain/langgraph';
import { ExternalClient } from './external.js';
import { VerifiedAction } from './runtime.js';

// Call inside a checkpointer-backed node or entrypoint. LangGraph owns tasks,
// interrupts and Command({resume: ...}); Lumni never claims this work.
export function langgraphTool<I,O>(client:ExternalClient,map:(input:I)=>VerifiedAction,call:(input:I)=>Promise<O>){
  const attempt=task('lumni-tool-attempt',async(input:I,index:number)=>{
    const action=map(input);const d=await client.prepare(action,`langgraph:${index}`);
    if(d.enforced&&['REJECT','REPLAN'].includes(d.decision))throw new Error(`Lumni: ${d.reason_code}`);
    return client.execute(action,d,()=>call(input));
  });
  return async(input:I):Promise<O>=>{for(let index=0;;index++){const result=await attempt(input,index);if(result.status==='COMPLETED')return result.output as O;if(result.status!=='PENDING')throw new Error(result.status);interrupt({type:'lumni',status:result.status,decision:result.decision});}};
}
