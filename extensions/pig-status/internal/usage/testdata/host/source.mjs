import {appendFileSync} from 'node:fs';

const token='header.eyJodHRwczovL2FwaS5vcGVuYWkuY29tL2F1dGgiOnsiY2hhdGdwdF9hY2NvdW50X2lkIjoic3ludGhldGljLWFjY291bnQifX0.signature';
const model=(provider,baseUrl,extra={})=>({provider,id:'fixture',name:'Fixture',api:'openai-completions',baseUrl,reasoning:false,input:['text'],cost:{input:0,output:0,cacheRead:0,cacheWrite:0},contextWindow:4096,maxTokens:128,...extra});
const mark=event=>appendFileSync(process.env.NA_OUTPUT+'/source.jsonl',JSON.stringify({event})+'\n');

export default function(pi) {
 let session;
 let key='synthetic-initial';
 let baseUrl='https://api.deepseek.com/v1';
 let headers;
 let duplicate=false;
 let failCurrent=false;
 let failAll=false;
 let otherKey='synthetic-initial';
 let otherBase='https://openrouter.ai/api/v1';
 let otherHeaders;
 let credentialAccess='synthetic-native';
 const native={type:'oauth',access:'synthetic-native',refresh:'synthetic-native-refresh',expires:9999999999999,clientId:'fixture-client',scopes:['chatgpt.tokens.use.direct']};
 const codex={type:'oauth',access:token,refresh:'synthetic-codex-refresh',expires:9999999999999,accountId:'synthetic-account'};
 const unused=()=>{throw new Error('fixture forbids streams');};
 const register=()=>pi.registerProvider({id:'deepseek',name:'Synthetic DeepSeek',getModels:()=>[model('deepseek',baseUrl,headers===undefined?{}:{headers})],stream:unused,streamSimple:unused,auth:{apiKey:{name:'Fixture key',resolve:async()=>{mark('resolve-deepseek');if(failCurrent||failAll)throw new Error('synthetic auth resolver failed');return {auth:{apiKey:key},source:'synthetic'};}}}});
 register();
 const registerOther=()=>pi.registerProvider({id:'openrouter',name:'Synthetic OpenRouter',getModels:()=>[model('openrouter',otherBase,otherHeaders===undefined?{}:{headers:otherHeaders})],stream:unused,streamSimple:unused,auth:{apiKey:{name:'Fixture key',resolve:async()=>{mark('resolve-openrouter');if(failAll)throw new Error('synthetic auth resolver failed');return {auth:{apiKey:otherKey},source:'synthetic'};}}}});
 registerOther();
 for(const [id,base] of [['baseten','https://inference.baseten.co/v1'],['fireworks','https://api.fireworks.ai/inference/v1']])pi.registerProvider({id,name:'Synthetic account',getModels:()=>[model(id,base)],stream:unused,streamSimple:unused,auth:{apiKey:{name:'Fixture key',resolve:async()=>{mark('resolve-'+id);if(failAll)throw new Error('synthetic auth resolver failed');return {auth:{apiKey:'synthetic-initial'},source:'synthetic'};}}}});
 for(const [id,base,access] of [['openai','https://api.openai.com/v1','synthetic-native'],['openai-codex','https://chatgpt.com/backend-api',token]]) {
  pi.registerProvider({id,name:'Synthetic OAuth',getModels:()=>[model(id,base)],stream:unused,streamSimple:unused,auth:{apiKey:{name:'Fixture OAuth',resolve:async()=>{mark('resolve-'+id);if(failAll)throw new Error('synthetic auth resolver failed');return {auth:{apiKey:access},source:'OAuth'};}}}});
 }
 const check=request=>{if(request.session!==session)throw new Error('fixture session mismatch');};
 pi.events.on('oauth:credential-readiness:v1',request=>{check(request);request.waitUntil(new Promise(resolve=>setTimeout(()=>{mark('ready-'+request.provider);resolve();},1)));});
 pi.events.on('oauth:credential-source:v1',request=>{
  check(request);
  if(request.provider==='openai')request.offer({...native,access:credentialAccess});
  if(request.provider==='openai-codex')request.offer(codex);
  if(duplicate&&request.provider==='openai')request.offer({...native,refresh:'different-refresh'});
 });
 pi.registerCommand('na-control',{handler:async(args)=>{
  switch(args){
   case 'fail-all':failAll=true;mark('control-fail-all');return;
   case 'other-rotate':otherKey='synthetic-rotated';registerOther();mark('control-'+args);return;
   case 'other-origin':otherBase='https://proxy.invalid/v1';registerOther();mark('control-'+args);return;
   case 'other-config':otherHeaders={'X-Configuration':'changed'};registerOther();mark('control-'+args);return;
   case 'fail-current':failCurrent=true;mark('control-fail-current');return;
   case 'rotate':key='synthetic-rotated';break;
   case 'origin':baseUrl='https://proxy.invalid/v1';break;
   case 'config':headers={'X-Configuration':'changed'};break;
   case 'override':headers={Authorization:'Bearer synthetic-selected'};break;
   case 'empty':headers={Authorization:''};break;
   case 'null':headers={Authorization:null};break;
   case 'mismatch':credentialAccess='unmatched';break;
   case 'duplicate':duplicate=true;break;
   case 'reset':failAll=false;otherKey='synthetic-initial';otherBase='https://openrouter.ai/api/v1';otherHeaders=undefined;registerOther();failCurrent=false;key='synthetic-initial';baseUrl='https://api.deepseek.com/v1';headers=undefined;credentialAccess='synthetic-native';duplicate=false;break;
   default:throw new Error('invalid fixture control');
  }
  register();mark('control-'+args);
 }});
 pi.on('session_start',(_event,ctx)=>{session=ctx.sessionManager;mark('source-ready');});
}
