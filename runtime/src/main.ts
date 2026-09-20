import { workflow } from './workflow';
import { Protocol } from './protocol';
import { runLoop } from './loop';
import { login, providerStream, modelCatalog } from './providers';
const protocol = new Protocol();
process.on('unhandledRejection',error=>{
 protocol.send({method:'failure',error:error instanceof Error ? error.message : 'runtime adapter failed'});
 process.exit(1);
});
try {
  const input = await protocol.initial;
  if(input.version !== 1) throw new Error('unsupported Maestro IPC version');
  if(input.operation === 'loop') await runLoop(protocol,input);
  else if(input.operation === 'login') await login(protocol,input);
  else if(input.operation === 'provider') await providerStream(protocol,input);
  else if(input.operation === 'workflow') await workflow(protocol,input);
  else if(input.operation === 'models') await protocol.request('models',modelCatalog(input.provider));
  else throw new Error('unknown Maestro runtime operation');
  protocol.send({method:'complete'});
  process.exit(0);
} catch(error) {
  protocol.send({method:'failure',error:error instanceof Error ? error.message : 'runtime failed'});
  process.exit(1);
}
