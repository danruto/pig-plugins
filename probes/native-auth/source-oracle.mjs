import {writeFileSync} from 'node:fs';
import {normalizeDeepSeekBalancePayload} from '../../node_modules/@narumitw/pi-usage/src/providers/deepseek.ts';
import {normalizeOpenRouterKeyPayload} from '../../node_modules/@narumitw/pi-usage/src/providers/openrouter.ts';
import {normalizeFireworksBillingSummaryPayload,normalizeFireworksAccountsPayload} from '../../node_modules/@narumitw/pi-usage/src/providers/fireworks.ts';
import {normalizeCodexBackendPayload} from '../../node_modules/@narumitw/pi-usage/src/providers/codex.ts';
const reports={
 deepseek:normalizeDeepSeekBalancePayload({is_available:true,balance_infos:[{currency:'USD',total_balance:'12.50',granted_balance:'0',topped_up_balance:'12.50'}]},0),
 openrouter:normalizeOpenRouterKeyPayload({data:{label:'fixture',usage:2,limit:10,limit_remaining:8,is_free_tier:false}},0),
 fireworks:normalizeFireworksBillingSummaryPayload({lineItems:[{series:'SERVERLESS',totalCost:{currencyCode:'USD',units:'2',nanos:500000000}}]},'second',0),
 codex:normalizeCodexBackendPayload({plan_type:'plus',rate_limit:{primary_window:{used_percent:10,limit_window_seconds:18000,reset_at:2000000000}}},0),
 choices:normalizeFireworksAccountsPayload({accounts:[{name:'accounts/first'},{name:'accounts/second'}]})
};
writeFileSync(process.env.NA_OUTPUT+'/source-oracle.json',JSON.stringify(reports,null,2));
