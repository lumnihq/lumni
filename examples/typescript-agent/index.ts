import {LumniClient} from "../../sdk/typescript/dist/index.js";

const client = new LumniClient(process.env.LUMNI_URL ?? "http://localhost:8080", process.env.LUMNI_API_KEY ?? "lumni_local_change_me");
const decision = await client.checkAction({work_id:"typescript-work",actor_id:"typescript-agent",operation:"subscription.cancel",arguments:{subscription_id:"sub_91"},resources:[{type:"subscription",external_id:"sub_91",mode:"WRITE",observed_version:"20",version_source:"etag"},],risk:"HIGH"});
console.log(decision);
