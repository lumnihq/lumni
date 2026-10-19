import os

from lumni import Client

client = Client(os.getenv("LUMNI_URL", "http://localhost:8080"), os.getenv("LUMNI_API_KEY", "lumni_local_change_me"))
decision = client.check_action({
    "work_id": "python-work",
    "actor_id": "python-agent",
    "operation": "subscription.cancel",
    "arguments": {"subscription_id": "sub_91"},
    "resources": [{"type": "subscription", "external_id": "sub_91", "mode": "WRITE", "observed_version": "20", "version_source": "etag"}],
    "risk": "HIGH",
})
print(decision)
