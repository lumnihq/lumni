import json
from pathlib import Path

from lumni import LumniContext, should_execute, with_context


def test_shadow_decisions_execute():
    assert should_execute({"decision": "WOULD_WAIT"})
    assert not should_execute({"decision": "WAIT"})


def test_shared_forward_compatible_fixture_and_context():
    fixture = json.loads((Path(__file__).parents[3] / "api" / "fixtures" / "decision-wait.json").read_text())
    assert fixture["future_additive_field"]["ignored"] is True
    assert not should_execute(fixture)
    action = with_context({"actor_id": "old", "work_id": "old", "operation": "read"}, LumniContext(actor_id="actor", work_id="work"))
    assert action["actor_id"] == "actor"
    assert action["work_id"] == "work"
