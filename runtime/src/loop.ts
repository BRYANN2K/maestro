import { agentLoop } from '../omp/agent-loop';
import { EventStream } from '@oh-my-pi/pi-ai';
import type { Protocol } from './protocol';

const usage = {input:0,output:0,cacheRead:0,cacheWrite:0,totalTokens:0,cost:{input:0,output:0,cacheRead:0,cacheWrite:0,total:0}};
// OMP schedules turns and validates tools. Maestro owns canonical history,
// provider streaming, budget admission and every actual tool execution.
export async function runLoop(p: Protocol, input: any) {
  const model: any = {id:'maestro',name:'Maestro',provider:'maestro',api:'openai-completions',baseUrl:'http://localhost',reasoning:false,input:['text'],cost:usage.cost,contextWindow:1000000,maxTokens:4096};
  const tools = input.tools.map((spec: any) => ({
    name:spec.Name,label:spec.Name,description:spec.Description,
    parameters:spec.InputSchema,intentTracing:'omit',concurrency:'exclusive',
    execute: async (id: string,args: any) => {
      const result = await p.request('tool',{ID:id,Name:spec.Name,Args:JSON.stringify(args)});
      if (result.error) throw new Error(result.error);
      return {content:[{type:'text',text:result.output}],details:{}};
    }
  }));
  const stream: any = agentLoop([{role:'user',content:input.prompt,timestamp:Date.now()}],
    {systemPrompt:[],messages:[],tools},
    {model,convertToLlm:(messages:any)=>messages,intentTracing:false,maxToolConcurrency:1} as any,
    undefined,
    async () => {
      const result = await p.request('turn');
      const message = result.assistant;
      const content: any[] = [];
      if (message.Content) content.push({type:'text',text:message.Content});
      for (const call of message.ToolCalls || []) {
        let args:any;
        try { args=JSON.parse(call.Args || '{}'); } catch { throw new Error('malformed tool arguments'); }
        content.push({type:'toolCall',id:call.ID,name:call.Name,arguments:args});
      }
      const response:any={role:'assistant',content,api:model.api,provider:model.provider,model:model.id,usage,stopReason:content.some(c=>c.type==='toolCall')?'toolUse':'stop',timestamp:Date.now()};
      const events:any=new EventStream((event:any)=>event.type==='done',(event:any)=>event.message);
      events.push({type:'done',reason:response.stopReason,message:response}); events.end(response);
      return events;
    });
  for await (const event of stream) {
    if(event.type==='message_end' && event.message.role==='toolResult') {
      await p.request('tool_result',event.message);
    }
  }
}
