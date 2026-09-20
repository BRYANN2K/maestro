import { getProviderDefinition, getOAuthApiKey, refreshOAuthToken, streamSimple } from '@oh-my-pi/pi-ai';
import models from '@oh-my-pi/pi-catalog/models.json';
import type { Protocol } from './protocol';

export async function login(p: Protocol,input: any) {
 const provider=getProviderDefinition(input.provider);
 if(!provider?.login) throw new Error('This provider has no account login');
 const credentials=await provider.login({
  onAuth: info=>{void p.request('auth',info).catch(error=>p.fail(error));},
  onProgress: ()=>{},
  onPrompt: prompt=>p.request('prompt',prompt),
 });
 await p.request('credentials',{provider:input.provider,credentials});
}

export function modelCatalog(provider: string) {
 return Object.values((models as any)[provider] || {}).map((m:any)=>({ID:m.id,Name:m.name,ContextWindow:m.contextWindow,DefaultMaxTokens:m.maxTokens,CanReason:m.reasoning,SupportsImages:m.input?.includes('image'),PriceInput:m.cost.input,PriceOutput:m.cost.output,PriceCacheHit:m.cost.cacheRead,PriceCacheCreate:m.cost.cacheWrite}));
}

export async function providerStream(p:Protocol,input:any) {
 const req=input.request;
 let model:any=(models as any)[input.provider]?.[req.Model];
 if(!model && input.baseURL) model={id:req.Model,name:req.Model,provider:input.provider,api:input.api || 'openai-completions',compat:{},baseUrl:input.baseURL,reasoning:false,input:['text'],cost:{input:0,output:0,cacheRead:0,cacheWrite:0},contextWindow:0,maxTokens:4096};
 if(!model) throw new Error('Unknown provider model; refresh the catalog or configure a compatible endpoint');
 if(input.baseURL) model={...model,baseUrl:input.baseURL};
 let key=input.key;
 if(input.oauth){
  let credentials=input.oauth;
  if(Date.now()>=credentials.expires-60000){
   credentials=await refreshOAuthToken(input.provider,credentials);
   await p.request('credentials',{provider:input.provider,credentials});
  }
  const resolved=await getOAuthApiKey(input.provider,{[input.provider]:credentials});
  if(!resolved)throw new Error('Provider account is disconnected');
  if(JSON.stringify(resolved.newCredentials)!==JSON.stringify(credentials) && resolved.newCredentials) await p.request('credentials',{provider:input.provider,credentials:resolved.newCredentials});
  key=resolved.apiKey;
 }
 const messages=(req.Messages || []).filter((m:any)=>m.Role!=='system').map((m:any)=>{
  if(m.ProviderState && m.ProviderState.provider===model.provider && m.ProviderState.model===model.id)return m.ProviderState;
  if(m.Role==='user')return {role:'user',content:m.Content,timestamp:Date.now()};
  if(m.Role==='tool')return {role:'toolResult',toolCallId:m.ToolCallID,toolName:m.Name,content:[{type:'text',text:m.Content}],isError:m.Content.startsWith('error: '),timestamp:Date.now()};
  const content:any[]=[];
  if(m.Content)content.push({type:'text',text:m.Content});
  for(const call of m.ToolCalls || [])content.push({type:'toolCall',id:call.ID,name:call.Name,arguments:JSON.parse(call.Args || '{}')});
  return {role:'assistant',content,api:model.api,provider:model.provider,model:model.id,usage:{input:0,output:0,cacheRead:0,cacheWrite:0,totalTokens:0,cost:{input:0,output:0,cacheRead:0,cacheWrite:0,total:0}},stopReason:content.some(c=>c.type==='toolCall')?'toolUse':'stop',timestamp:Date.now()};
 });
 const systemPrompt=[...(req.System || []),...(req.Messages || []).filter((m:any)=>m.Role==='system')].map((m:any)=>m.Content).join('\n\n');
 const tools=(req.Tools || []).map((s:any)=>({name:s.Name,description:s.Description,parameters:s.InputSchema}));
 let done=false;
 for await(const event of streamSimple(model,{systemPrompt,messages,tools} as any,{apiKey:key,maxTokens:req.Sampling?.MaxTokens || undefined,reasoning:req.Sampling?.ReasoningEffort || undefined,temperature:req.Sampling?.Temperature,maxRetries:0} as any)){
  if(event.type==='text_delta')await p.request('event',{Type:'text_delta',Content:{Text:event.delta}});
  else if(event.type==='thinking_delta')await p.request('event',{Type:'reasoning_delta',Content:{Text:event.delta}});
  else if(event.type==='toolcall_end')await p.request('event',{Type:'tool_call',Content:{ID:event.toolCall.id,Name:event.toolCall.name,Args:JSON.stringify(event.toolCall.arguments)}});
  else if(event.type==='error')throw new Error(event.error.errorMessage || 'Provider stream failed');
  else if(event.type==='done'){
   if(event.message.stopReason==='length')throw new Error('Provider output was truncated; tool calls were not executed');
   const u=event.message.usage;
   await p.request('event',{Type:'done',Content:{Usage:{InputTokens:u.input,OutputTokens:u.output,CacheCreateTokens:u.cacheWrite,CacheHitTokens:u.cacheRead},Cost:{InputUSD:u.cost.input,OutputUSD:u.cost.output,CacheCreateUSD:u.cost.cacheWrite,CacheHitUSD:u.cost.cacheRead},ProviderState:event.message}});done=true;
  }
 }
 if(!done)throw new Error('Provider stream ended without completion');
}
