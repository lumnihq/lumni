#!/bin/sh
set -eu

LUMNI_DEMO_URL=${LUMNI_URL:-http://localhost:8080}
LUMNI_DEMO_KEY=${LUMNI_API_KEY:-lumni_local_change_me}
LUMNI_DEMO_SUBSCRIPTION=${LUMNI_DEMO_SUBSCRIPTION:-sub_demo_$(date +%s)}
LUMNI_DEMO_RESOURCE_ID="default:subscription:${LUMNI_DEMO_SUBSCRIPTION}"
AUTH_HEADER="Authorization: Bearer ${LUMNI_DEMO_KEY}"
JSON_HEADER="Content-Type: application/json"

echo "Waiting for Lumni at ${LUMNI_DEMO_URL}"
attempt=0
until curl -fsS "${LUMNI_DEMO_URL}/v1/health/ready" >/dev/null; do
  attempt=$((attempt + 1))
  if [ "$attempt" -ge 30 ]; then
    echo "Lumni did not become ready within 30 seconds" >&2
    exit 1
  fi
  sleep 1
done

post() {
  curl -fsS -X POST -H "$AUTH_HEADER" -H "$JSON_HEADER" --data "$2" "${LUMNI_DEMO_URL}$1"
}

put() {
  curl -fsS -X PUT -H "$AUTH_HEADER" -H "$JSON_HEADER" --data "$2" "${LUMNI_DEMO_URL}$1"
}

show_decision() {
  printf '%s' "$1" | python3 -c 'import json,sys; d=json.load(sys.stdin); print(json.dumps({"action_id":d.get("action_id"),"decision":d.get("decision"),"reason_code":d.get("reason_code"),"enforced":d.get("enforced"),"receipt_id":d.get("receipt_id"),"fencing_tokens":[x.get("fencing_token") for x in d.get("leases",[])],"evaluation_stages":[x.get("stage") for x in d.get("evaluations",[])]}, indent=2))'
}

echo "Registering demo actor and work"
post /v1/actors '{"id":"demo-agent","type":"AGENT","runtime":"demo","display_name":"Demo Agent"}' >/dev/null
post /v1/agents '{"id":"demo-agent-profile","actor_id":"demo-agent","name":"Demo Agent","framework":"shell","model_provider":"demo","model":"deterministic-v1"}' >/dev/null
post /v1/sessions '{"id":"demo-session","agent_id":"demo-agent-profile","actor_id":"demo-agent","runtime":"shell","external_id":"demo-run"}' >/dev/null
post /v1/work '{"id":"demo-work","actor_id":"demo-agent","agent_id":"demo-agent-profile","session_id":"demo-session","goal":"Demonstrate deterministic subscription coordination","risk":"HIGH"}' >/dev/null
post /v1/resources/resolve "{\"type\":\"subscription\",\"external_id\":\"${LUMNI_DEMO_SUBSCRIPTION}\",\"namespace\":\"default\"}" >/dev/null
post "/v1/resources/${LUMNI_DEMO_RESOURCE_ID}/version" '{"version":"19","version_source":"etag","source":"demo","sequence":19}' >/dev/null

echo "Shadow mode: stale action is observed but not blocked"
put /v1/config/mode '{"mode":"SHADOW"}' >/dev/null
SHADOW_JSON=$(post /v1/actions/check "{\"work_id\":\"demo-work\",\"actor_id\":\"demo-agent\",\"operation\":\"subscription.upgrade\",\"arguments\":{\"plan\":\"pro\"},\"resources\":[{\"type\":\"subscription\",\"external_id\":\"${LUMNI_DEMO_SUBSCRIPTION}\",\"mode\":\"WRITE\",\"observed_version\":\"18\",\"version_source\":\"etag\"}],\"risk\":\"HIGH\"}")
show_decision "$SHADOW_JSON"

echo "Enforcement mode: first mutation is allowed and reserved"
put /v1/config/mode '{"mode":"ENFORCE"}' >/dev/null
ALLOW_JSON=$(post /v1/actions/check "{\"work_id\":\"demo-work\",\"actor_id\":\"demo-agent\",\"operation\":\"subscription.upgrade\",\"arguments\":{\"plan\":\"pro\"},\"resources\":[{\"type\":\"subscription\",\"external_id\":\"${LUMNI_DEMO_SUBSCRIPTION}\",\"mode\":\"WRITE\",\"observed_version\":\"19\",\"version_source\":\"etag\"}],\"risk\":\"HIGH\"}")
show_decision "$ALLOW_JSON"
ACTION_ID=$(printf '%s' "$ALLOW_JSON" | python3 -c 'import json,sys; print(json.load(sys.stdin)["action_id"])')
LEASE_TOKEN=$(printf '%s' "$ALLOW_JSON" | python3 -c 'import json,sys; print(json.load(sys.stdin)["lease_token"])')
post "/v1/actions/${ACTION_ID}/start" "{\"lease_token\":\"${LEASE_TOKEN}\"}" >/dev/null

echo "Concurrent cancel is made to wait"
WAIT_JSON=$(post /v1/actions/check "{\"work_id\":\"demo-work\",\"actor_id\":\"demo-agent\",\"operation\":\"subscription.cancel\",\"arguments\":{},\"resources\":[{\"type\":\"subscription\",\"external_id\":\"${LUMNI_DEMO_SUBSCRIPTION}\",\"mode\":\"WRITE\",\"observed_version\":\"19\",\"version_source\":\"etag\"}],\"risk\":\"HIGH\"}")
show_decision "$WAIT_JSON"

echo "Commit the first effect, then prove exact deduplication"
post "/v1/actions/${ACTION_ID}/result" "{\"status\":\"SUCCEEDED\",\"lease_token\":\"${LEASE_TOKEN}\",\"external_result_id\":\"upgrade_${LUMNI_DEMO_SUBSCRIPTION}\",\"resource_versions\":{\"${LUMNI_DEMO_RESOURCE_ID}\":\"20\"}}" >/dev/null
DEDUP_JSON=$(post /v1/actions/check "{\"work_id\":\"demo-work\",\"actor_id\":\"demo-agent\",\"operation\":\"subscription.upgrade\",\"arguments\":{\"plan\":\"pro\"},\"resources\":[{\"type\":\"subscription\",\"external_id\":\"${LUMNI_DEMO_SUBSCRIPTION}\",\"mode\":\"WRITE\",\"observed_version\":\"20\",\"version_source\":\"etag\"}],\"risk\":\"HIGH\"}")
show_decision "$DEDUP_JSON"
