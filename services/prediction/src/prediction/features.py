"""Feature construction shared by the service and the training script.

Training imports this module, so a model is always trained on exactly the
features the service computes at inference time.

The model predicts the app's own heatwave warning (see the Go engine's
``isHeatwaveWarning``): apparent temperature >= 54 C, or >= 41 C on two
consecutive days. That condition is defined by absolute temperatures, so the
features are temperatures too, with no location or season, and the model is not
tied to any region.
"""

from __future__ import annotations

from dataclasses import dataclass
from datetime import date

FEATURES = [
    "apparent_max",    # forecast max apparent temperature of the day, °C
    "apparent_prev1",  # the day before, °C (the warning needs two hot days in a row)
    "apparent_prev2",  # two days before, °C
    "tmax",            # forecast max air temperature, °C
    "tmin",            # forecast min air temperature, °C (hot nights prolong heat stress)
    "heat_gap",        # apparent_max - tmax: how much humidity adds on top of the heat
]


@dataclass(frozen=True)
class Day:
    """One day of the daily series, in °C."""

    date: date
    tmax: float
    tmin: float
    apparent_max: float


def day_features(series: list[Day], i: int) -> list[float]:
    """Feature vector for ``series[i]``.

    The series is ordered oldest first and mixes observed history with forecast
    days. Where an earlier day is missing, the nearest later one stands in.
    """
    day = series[i]
    prev1 = series[i - 1].apparent_max if i >= 1 else day.apparent_max
    prev2 = series[i - 2].apparent_max if i >= 2 else prev1
    return [day.apparent_max, prev1, prev2, day.tmax, day.tmin, day.apparent_max - day.tmax]


def expand_poly2(x: list[float]) -> list[float]:
    """Degree-2 expansion without a bias term: the inputs, then every product x_i * x_j
    with i <= j, in row-major order (the same layout as sklearn's PolynomialFeatures).

    The interactions let a linear model express things like "a hot day matters more
    when yesterday was hot too". Training and serving both call this.
    """
    out = list(x)
    for i in range(len(x)):
        for j in range(i, len(x)):
            out.append(x[i] * x[j])
    return out
