"""Merge generated public protocol schemas and additive routes into OpenAPI."""
import json,re
from pathlib import Path
import yaml
path=Path('api/openapi.yaml');spec=yaml.safe_load(path.read_text());definitions=json.loads(Path('api/schemas/durable-1.1.json').read_text())['$defs']
def replace_refs(value):
 if isinstance(value,dict):return {k:replace_refs(v) for k,v in value.items()}
 if isinstance(value,list):return [replace_refs(v) for v in value]
 if isinstance(value,str):return value.replace('#/$defs/','#/components/schemas/')
 return value
def merge(left,right):
 if isinstance(left,dict) and isinstance(right,dict):
  out=dict(left)
  for key,value in right.items():out[key]=merge(out[key],value) if key in out else value
  return out
 return right
schemas=spec.setdefault('components',{}).setdefault('schemas',{})
for name,definition in replace_refs(definitions).items():schemas[name]=merge(schemas.get(name,{}),definition)
def add_operation(route,method,operation):
 # Explicit API contracts retain pagination, response schemas and status codes.
 # Generated defaults only fill fields that have not yet been documented.
 path_item=spec['paths'].setdefault(route,{})
 path_item[method]={**operation,**path_item.get(method,{})}
spec['info']['version']='0.2.0'
mutations=[
('post','/runtime/work','RunInput','Submit versioned native work'),('post','/runtime/claim','ClaimInput','Claim work with fencing'),('post','/runtime/work/{id}/heartbeat','RuntimeAuth','Renew current worker ownership'),('post','/runtime/work/{id}/finish','StepResult','Persist native work completion'),('post','/runtime/work/{id}/cancel',None,'Cancel work and children'),('post','/runtime/work/{id}/signals','SignalInput','Deliver an identified signal'),('post','/runtime/work/{id}/steps/begin','StepInput','Begin or replay a stable step'),('post','/runtime/work/{id}/steps/{key}/start','StepResult','Persist call-start marker'),('post','/runtime/work/{id}/steps/{key}/result','StepResult','Persist immutable step result'),('post','/actions/{id}/recheck',None,'Evaluate current admission with a new recheck identity'),('post','/actions/{id}/start-once',None,'Consume one external invocation permission'),('post','/dependencies','DependencyInput','Create dependency or supersession without cycles'),('post','/verifiers','Verifier','Register versioned verifier'),('post','/outcomes/{id}/evidence','VerificationEvidence','Append authenticated verifier evidence'),('post','/outcomes/{id}/retry',None,'Retry verification without replaying mutation'),('put','/config/enforcement','EnforcementRule','Set operation and rule enforcement'),('put','/config/operation-verifiers','OperationVerifier','Bind a verifier to a mapped operation'),('post','/discovery/tools',None,'Record discovered tool schema'),('post','/discovery/confirm','ToolMapping','Confirm canonical operation resource and effect mapping'),('post','/reconciliation/handlers','ReconciliationHandler','Register reconciliation handler version'),('post','/reconciliation','ReconciliationInput','Submit reconciliation through normal runtime admission'),('post','/reconciliation/{id}/review',None,'Record operator review'),('post','/effects/resolve-identity','EffectIdentityResolution','Resolve ambiguous legacy effect identity with evidence'),('post','/model-calls',None,'Persist identified model invocation and complete result')]
prefix='/v1' if any(p.startswith('/v1/') for p in spec['paths']) else ''
for method,route,typ,summary in mutations:
 operation={'summary':summary,'operationId':method+re.sub('[^A-Za-z0-9]','_',route),'tags':['Durable execution'],'responses':{'200':{'description':'Persisted result'},'401':{'description':'Authentication required'},'403':{'description':'Insufficient scope'},'409':{'description':'Ownership or lifecycle conflict'},'422':{'description':'Typed identity, replay, verification, or validation error'}},'requestBody':{'required':True,'content':{'application/json':{'schema':{'$ref':'#/components/schemas/'+typ} if typ else {'type':'object'}}}}}
 if '{' in route:operation['parameters']=[{'name':name,'in':'path','required':True,'schema':{'type':'string'}} for name in re.findall(r'{(.*?)}',route)]
 add_operation(prefix+route,method,operation)
for collection in ['findings','incidents','activity','steps','step-attempts','journal','waits','effects','outcomes','verifiers','verification-attempts','dependencies','enforcement','operation-verifiers','discovery','reconciliation','reconciliation-handlers','model-calls']:
 for suffix in ['', '/{id}']:
  operation={'summary':'Read '+collection,'operationId':'get_'+collection.replace('-','_')+('_by_id' if suffix else ''),'tags':['Durable execution'],'parameters':[{'name':'work_id','in':'query','schema':{'type':'string'}}], 'responses':{'200':{'description':'Tenant-scoped bounded metadata; encrypted payloads are excluded','content':{'application/json':{'schema':{'type':'object','properties':{'items':{'type':'array','items':{'type':'object'}}}}}}}}}
  if suffix:operation['parameters'].append({'name':'id','in':'path','required':True,'schema':{'type':'string'}})
  add_operation(prefix+'/'+collection+suffix,'get',operation)
for route in ['/actions/{id}/previous-result','/model-calls/{id}/payload','/gateway/config']:
 add_operation(prefix+route,'get',{'summary':'Read retained payload (payload scope) or gateway policy (query scope)','parameters':[{'name':name,'in':'path','required':True,'schema':{'type':'string'}} for name in re.findall(r'{(.*?)}',route)],'responses':{'200':{'description':'Result'},'422':{'description':'RESULT_PENDING requires authoritative verification; RESULT_UNAVAILABLE means retained result cannot be recovered. Neither permits repeating the mutation.'}}})
add_operation(prefix+'/mcp','post',{'summary':'Seven read-only Lumni MCP query tools; requires query scope','responses':{'200':{'description':'MCP JSON-RPC result'}}})
path.write_text(yaml.safe_dump(spec,sort_keys=False,allow_unicode=True))
