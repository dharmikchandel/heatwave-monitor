"""Heatwave probability: a per-horizon logistic model, plus a rule-based fallback."""

from __future__ import annotations

import json
import math
from dataclasses import dataclass
from pathlib import Path
from typing import Any

from .features import FEATURES, Day, day_features, expand_poly2

DEFAULT_MODEL_PATH = Path(__file__).with_name("model.json")

METHOD_MODEL = "model"
METHOD_RULES = "rules"


def _sigmoid(z: float) -> float:
    z = max(-30.0, min(30.0, z))
    return 1.0 / (1.0 + math.exp(-z))


@dataclass(frozen=True)
class HorizonModel:
    intercept: float
    coef: list[float]
    mean: list[float]
    scale: list[float]

    def probability(self, x: list[float]) -> float:
        z = self.intercept
        for value, coef, mean, scale in zip(x, self.coef, self.mean, self.scale):
            z += coef * (value - mean) / scale
        return _sigmoid(z)


class Model:
    """Loaded from model.json: one logistic model per forecast horizon."""

    def __init__(self, raw: dict[str, Any]):
        if raw.get("features") != FEATURES:
            raise ValueError(
                f"model was trained on features {raw.get('features')}, but the service computes {FEATURES}"
            )
        horizons = sorted(raw["horizons"], key=lambda h: h["horizon"])
        if [h["horizon"] for h in horizons] != list(range(len(horizons))) or not horizons:
            raise ValueError("model horizons must be 0..N without gaps")
        if raw.get("expansion") not in (None, "poly2"):
            raise ValueError(f"unknown feature expansion {raw.get('expansion')!r}")
        self.expansion: str | None = raw.get("expansion")
        expected = len(FEATURES) if self.expansion is None else len(expand_poly2([0.0] * len(FEATURES)))
        for h in horizons:
            if not (len(h["coef"]) == len(h["mean"]) == len(h["scale"]) == expected):
                raise ValueError(f"horizon {h['horizon']}: coefficient count does not match {expected} features")
        self.raw = raw
        self.version: str = raw["version"]
        self.horizons = [
            HorizonModel(h["intercept"], h["coef"], h["mean"], h["scale"]) for h in horizons
        ]

    @classmethod
    def load(cls, path: Path | str = DEFAULT_MODEL_PATH) -> "Model":
        with open(path, encoding="utf-8") as f:
            return cls(json.load(f))

    def probability(self, features: list[float], horizon: int) -> float:
        if self.expansion == "poly2":
            features = expand_poly2(features)
        # Beyond the trained horizons the longest-range model is the best we have.
        return self.horizons[min(max(horizon, 0), len(self.horizons) - 1)].probability(features)

    def info(self) -> dict[str, Any]:
        return {
            "version": self.version,
            "kind": self.raw.get("kind"),
            "expansion": self.expansion,
            "trainedAt": self.raw.get("trainedAt"),
            "horizons": len(self.horizons),
            "training": self.raw.get("training"),
            "evaluation": self.raw.get("evaluation"),
        }


def rule_probability(series: list[Day], i: int) -> float:
    """Fallback used when no trained model is loaded.

    A logistic curve centred on the WMO "danger" apparent temperature (41 °C),
    pushed up when the preceding days were already hot (a heatwave is a
    *sustained* event). Deliberately simple and explainable; no training involved.
    """
    consecutive = 0
    for d in reversed(series[:i]):
        if d.apparent_max < 38.0:
            break
        consecutive += 1
    return _sigmoid(0.7 * (series[i].apparent_max - 41.0) + 0.5 * min(consecutive, 3))


@dataclass(frozen=True)
class DayProbability:
    date: str
    horizon: int
    probability: float


def predict_days(series: list[Day], first_forecast: int, model: Model | None) -> tuple[str, list[DayProbability]]:
    """Probability that each day from ``first_forecast`` on is a heatwave-warning day.

    Returns the method used ("model", or "rules" when no model is loaded) and one
    entry per forecast day; horizon 0 is the first forecast day (today).
    """
    out = []
    for i in range(first_forecast, len(series)):
        horizon = i - first_forecast
        if model is not None:
            p = model.probability(day_features(series, i), horizon)
        else:
            p = rule_probability(series, i)
        out.append(DayProbability(series[i].date.isoformat(), horizon, round(p, 4)))
    return (METHOD_MODEL if model is not None else METHOD_RULES), out
