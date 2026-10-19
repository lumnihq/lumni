import {readFile} from 'node:fs/promises';
import {spawn} from 'node:child_process';
import {resolve} from 'node:path';
import {pathToFileURL} from 'node:url';

const credentials=JSON.parse(await readFile(process.argv[2],'utf8'));
const env={...process.env,LUMNI_TEST_URL:`http://127.0.0.1:${credentials.port}`,LUMNI_TEST_API_KEY:credentials.admin_key};
if(process.env.LUMNI_INSTALLED_PREFIX)env.LUMNI_TEST_SDK_MODULE=pathToFileURL(resolve(process.env.LUMNI_INSTALLED_PREFIX,'node_modules/@lumniverse/sdk/dist/index.js')).href;
function run(command,args){return new Promise((ok,fail)=>{const child=spawn(command,args,{env,stdio:'inherit'});child.once('error',fail);child.once('exit',code=>code===0?ok():fail(Error(`${command} exited ${code}`)));});}
await run(process.execPath,['--test',resolve('sdk/typescript/test/live.test.mjs')]);
await run(process.env.RELEASE_PYTHON||'python3',['-m','pytest','sdk/python/tests/test_live.py','-v','-o','pythonpath=']);
const response=await fetch(env.LUMNI_TEST_URL+'/console/');
if(!response.ok || !(await response.text()).includes('id="root"'))throw Error('Installed console unavailable');
console.log('Live SDK recovery and installed console checks passed.');
