import { ProjectStore, json, atomic } from '../stipulate/store.mjs';
import { Coordinator } from '../stipulate/coordinator.mjs';
import type { Protocol } from './protocol';

export async function workflow(p: Protocol, input: any) {
 const store=new ProjectStore(input.root,{engine:input.engine});
 let job:any;
 const sessionPath=(id:string)=>{if(!/^ses_[a-zA-Z0-9_-]+$/.test(id))throw new Error('invalid session');return store.path(`.workflow/.runtime/sessions/${id}.json`)};
 const host={
  models:async()=>input.models || [],
  session:async(id:string)=>{if(id===input.parent.id)return input.parent;const value=json(sessionPath(id),null);if(!value)throw new Error('Maestro session unavailable; reconcile before redispatch');return value},
  waiting:async()=>false,
  interrupt:async()=>({stopped:false,reason:'Worker is not attached to this command'}),
  prepare:async(value:any)=>{job=value},
  spawn:async(args:any)=>{
   const session=await p.request('worker',{job,prompt:args.prompt,sessionID:args.sessionID});
   atomic(sessionPath(session.id),session);
   return {metadata:{sessionID:session.id}};
  },
  childID:(result:any)=>result.metadata.sessionID,
 };
 const coordinator=new Coordinator(store,host);
 let result:any;
 switch(input.action){
 case 'snapshot': result=await coordinator.snapshot(input.parent.id,input.change);break;
 case 'delegate': result=await coordinator.delegate({...input.task,background:false},{sessionID:input.parent.id});await coordinator.reconcile(input.task.change_id);result=await coordinator.snapshot(input.parent.id,input.task.change_id);break;
 case 'research': result=await coordinator.research(input.task,{sessionID:input.parent.id});break;
 case 'contribution': result=await coordinator.contribution(input.change,input.taskID,input.decision,input.reason,input.runID);break;
 case 'settings': result=store.saveSettings(input.settings,input.scope,input.revision);break;
 default:throw new Error('Unknown coordinator action');
 }
 await p.request('workflow_result',result);
}
