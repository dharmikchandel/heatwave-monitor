"""Go <-> Python contract tests, using the shared fixtures in the repo's testdata/ directory.

* weather-processed.sample.json is written by the Go services (see
  backend/internal/contracts/fixtures_test.go) and must be accepted here.
* heatwave-predicted.sample.json is written by this service and must decode strictly
  in Go. Regenerate it with:  UPDATE_FIXTURES=1 uv run pytest tests/test_contract.py
"""

import json
import os
from pathlib import Path

from prediction.events import receive

TESTDATA = Path(__file__).resolve().parents[3] / "testdata"
PREDICTED = TESTDATA / "heatwave-predicted.sample.json"


def shape(value):
    """The structure of a JSON value: key names and value types, ignoring the values themselves."""
    if isinstance(value, dict):
        return {k: shape(v) for k, v in sorted(value.items())}
    if isinstance(value, list):
        return [shape(value[0])] if value else []
    return type(value).__name__ if not isinstance(value, bool) else "bool"


def emitted_event(db, service, processed_sample, make_event):
    assert receive(db, make_event(processed_sample), service.handle_event)
    (row,) = db.read("SELECT target, type, payload FROM outbox")
    assert (row["target"], row["type"]) == ("risk", "heatwave.predicted")
    return json.loads(row["payload"])


def test_go_written_processed_sample_is_accepted(db, service, processed_sample, make_event):
    payload = emitted_event(db, service, processed_sample, make_event)
    assert service.recent_rejections(5) == []
    assert payload["weather"] == processed_sample


def test_heatwave_predicted_shape_matches_the_committed_sample(db, service, processed_sample, make_event):
    payload = emitted_event(db, service, processed_sample, make_event)

    if os.environ.get("UPDATE_FIXTURES") == "1":
        PREDICTED.write_text(json.dumps(payload, indent=1) + "\n")
        return
    committed = json.loads(PREDICTED.read_text())
    assert shape(payload) == shape(committed), (
        "the heatwave.predicted payload shape changed; regenerate the sample with UPDATE_FIXTURES=1 "
        "and make sure the Go contract (backend/internal/contracts/prediction.go) still decodes it"
    )
