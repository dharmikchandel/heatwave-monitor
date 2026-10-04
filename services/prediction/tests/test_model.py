import copy
import json
from datetime import date, timedelta

import pytest

from prediction.features import FEATURES, Day, day_features, expand_poly2
from prediction.model import Model, predict_days, rule_probability


def series(apparents, start=date(2026, 5, 1), app_boost=3.0):
    """A daily series where tmax = apparent - app_boost (so humidity adds a few degrees)."""
    return [Day(start + timedelta(days=i), a - app_boost, a - app_boost - 10, a) for i, a in enumerate(apparents)]


def test_shipped_model_is_internally_consistent(model):
    assert model.version.startswith("lr-")
    assert len(model.horizons) == 7
    assert model.expansion == "poly2"
    assert model.raw["features"] == FEATURES


def test_shipped_model_beats_climatology_on_held_out_years(model):
    """A guard against shipping a model that is worse than just quoting the base rate."""
    for h in model.raw["evaluation"]["perHorizon"]:
        assert h["brier"] < h["brierClimatology"], f"horizon {h['horizon']}: no better than climatology"
        assert h["auc"] > 0.95, f"horizon {h['horizon']}: poor ranking (AUC {h['auc']})"


def test_shipped_model_generalises_to_cities_it_never_saw(model):
    cities = model.raw["evaluation"]["leaveOneCityOut"]["cities"]
    assert cities, "no leave-one-city-out evidence recorded"
    for c in cities:
        if c["auc"] is not None:
            assert c["auc"] > 0.9, c
        assert c["brier"] <= c["brierClimatology"], c


def test_a_sustained_scorching_run_is_almost_certainly_a_warning(model):
    method, days = predict_days(series([46, 48, 49, 50, 49, 48, 47, 47, 46, 45]), 3, model)
    assert method == "model"
    assert all(d.probability > 0.9 for d in days[:3])


def test_a_mild_week_has_negligible_probability(model):
    _, days = predict_days(series([32, 33, 32, 33, 34, 33, 32, 32, 33, 33]), 3, model)
    assert max(d.probability for d in days) < 0.01


def test_hotter_means_likelier_at_every_horizon(model):
    """The property that matters most: more heat must never read as less danger."""
    for horizon in range(7):
        prev = -1.0
        for apparent in range(30, 61):
            s = series([apparent] * 10)
            x = day_features(s, 3)
            p = model.probability(x, horizon)
            assert p >= prev - 0.005, f"horizon {horizon}: p fell from {prev:.3f} to {p:.3f} at apparent {apparent}"
            prev = p
        assert model.probability(day_features(series([30] * 10), 3), horizon) < 0.01
        assert model.probability(day_features(series([58] * 10), 3), horizon) > 0.9


def test_a_second_hot_day_is_likelier_than_an_isolated_one(model):
    isolated = predict_days(series([34, 34, 34, 43, 34, 34, 34]), 3, model)[1][0].probability
    streak = predict_days(series([34, 43, 43, 43, 34, 34, 34]), 3, model)[1][0].probability
    assert streak > isolated  # the warning needs two hot days in a row


def test_probabilities_are_valid_and_cover_each_forecast_day(model):
    s = series([43] * 10)
    _, days = predict_days(s, 3, model)
    assert [d.horizon for d in days] == list(range(7))
    assert [d.date for d in days] == [(date(2026, 5, 1) + timedelta(days=3 + i)).isoformat() for i in range(7)]
    assert all(0.0 <= d.probability <= 1.0 for d in days)


def test_beyond_the_trained_horizons_the_last_model_is_reused(model):
    s = series([43] * 20)
    _, days = predict_days(s, 3, model)
    assert len(days) == 17
    features = expand_poly2(day_features(s, 3 + 16))
    assert days[16].probability == pytest.approx(model.horizons[6].probability(features), abs=1e-4)


def test_the_model_is_not_tied_to_a_region(model):
    # Same temperatures anywhere on Earth give the same answer: the rule is defined by absolute heat.
    assert predict_days(series([44] * 10), 3, model)[1] == predict_days(series([44] * 10), 3, model)[1]


def test_without_a_model_the_rule_fallback_is_used():
    method, days = predict_days(series([44] * 10), 3, None)
    assert method == "rules" and len(days) == 7
    assert days[0].probability == pytest.approx(rule_probability(series([44] * 10), 3), abs=1e-4)


def test_rule_probability_behaves_sensibly():
    assert rule_probability(series([33]), 0) < 0.01
    assert rule_probability(series([49]), 0) > 0.9
    # The same hot day is likelier when the days before it were already hot: heatwaves are sustained.
    isolated = rule_probability(series([33, 33, 33, 41]), 3)
    sustained = rule_probability(series([39, 40, 41, 41]), 3)
    assert sustained > isolated


def test_loading_rejects_a_model_for_different_features(model):
    raw = copy.deepcopy(model.raw)
    raw["features"] = FEATURES[:-1]
    with pytest.raises(ValueError, match="features"):
        Model(raw)


def test_loading_rejects_wrong_coefficient_counts(model):
    raw = copy.deepcopy(model.raw)
    raw["horizons"][2]["coef"] = raw["horizons"][2]["coef"][:-1]
    with pytest.raises(ValueError, match="coefficient count"):
        Model(raw)


def test_loading_rejects_unknown_expansion_and_horizon_gaps(model):
    raw = copy.deepcopy(model.raw)
    raw["expansion"] = "cubic"
    with pytest.raises(ValueError, match="expansion"):
        Model(raw)
    raw = copy.deepcopy(model.raw)
    del raw["horizons"][3]
    with pytest.raises(ValueError, match="horizons"):
        Model(raw)


def test_model_file_roundtrips(model, tmp_path):
    path = tmp_path / "m.json"
    path.write_text(json.dumps(model.raw))
    assert Model.load(path).version == model.version
