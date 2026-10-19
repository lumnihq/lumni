# lumni-sdk

The Python client for [Lumni](https://lumniverse.com): your AI agents check with Lumni before they change anything, so each action happens once, the way you meant, and is proven afterwards.

```bash
pip install lumni-sdk
```

The quickest start is `npx lumniverse init` in your agent's folder: it connects the agent, writes `LUMNI_URL` and `LUMNI_API_KEY` to `.env`, and prints the lines below for your code.

```python
import os
from lumni import Client, should_execute

lumni = Client(os.environ["LUMNI_URL"], os.environ["LUMNI_API_KEY"])

decision = lumni.check_action({
    "actor_id": "support-agent", "work_id": run_id, "operation": "stripe.refund",
    "arguments": {"charge": charge_id, "amount": 60},
    "resources": [{"type": "charge", "external_id": charge_id, "mode": "WRITE"}],
})
if should_execute(decision):
    stripe.Refund.create(charge=charge_id, amount=6000)
```

New workspaces start in shadow mode: Lumni records what it would have done, and `should_execute` lets everything through until you turn enforcement on.

Extras: `pip install "lumni-sdk[langgraph]"` or `"lumni-sdk[openai]"` for the LangGraph and OpenAI Agents adapters. Python 3.10+.

Docs: [github.com/lumnihq/lumni](https://github.com/lumnihq/lumni/blob/main/docs/sdk.md). License: Apache-2.0 (the SDK; Lumni’s server is FSL).
