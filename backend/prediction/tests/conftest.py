import json
import sys
from pathlib import Path

import pytest
from fastapi.testclient import TestClient

sys.path.insert(0, str(Path(__file__).resolve().parents[1] / "src"))

from prediction.app import create_app  # noqa: E402
from prediction.db import Database  # noqa: E402
from prediction.events import new_id  # noqa: E402
from prediction.model import Model  # noqa: E402
from prediction.service import PredictionService  # noqa: E402

TESTDATA = Path(__file__).resolve().parents[3] / "testdata"


@pytest.fixture
def processed_sample() -> dict:
    """A weather.processed payload exactly as the Go processing service writes it."""
    return json.loads((TESTDATA / "weather-processed.sample.json").read_text())


@pytest.fixture
def make_event():
    def build(payload, event_type="weather.processed", event_id=None):
        return {"id": event_id or new_id(), "type": event_type, "source": "processing",
                "created_at": "2026-05-01T09:00:02Z", "payload": payload}
    return build


@pytest.fixture
def db():
    d = Database(":memory:")
    yield d
    d.close()


@pytest.fixture
def model() -> Model:
    return Model.load()


@pytest.fixture
def service(db, model) -> PredictionService:
    return PredictionService(db, model)


@pytest.fixture
def client(tmp_path):
    app = create_app(str(tmp_path / "prediction.db"), start_dispatcher=False)
    with TestClient(app) as c:
        c.app_state = app.state
        yield c
