"""Train the heatwave probability model and write backend/prediction/src/prediction/model.json.

    cd scripts/train && uv run python train.py

What the model predicts
-----------------------
The probability that a forecast day is a *heatwave-warning day* by the app's own
definition (the Go engine's ``isHeatwaveWarning``): apparent temperature >= 54 C,
or >= 41 C on two consecutive days. Tiers on the dashboard come from the point
forecast; this model adds how likely the warning condition is *given that
forecasts are uncertain*.

Why the training inputs are noisy
---------------------------------
At serving time the model sees forecast values, which are wrong by an amount that
grows with lead time. So the features here are the true values plus forecast-error
noise for that lead time, while the label comes from the true values. The model
therefore learns calibrated probabilities that already account for forecast
uncertainty (one logistic regression per horizon, 0-6 days).

Data: 34 years of ERA5 daily weather via the Open-Meteo archive API (cached in
.cache/) for the cities in CITIES (India, the Gulf, South/Southeast Asia, Africa, the US and
southern Europe), so the model is not tied to one region.
Evaluation: a temporal hold-out (2020-2024) and leave-one-city-out on LOCO_CITIES.
"""

from __future__ import annotations

import hashlib
import json
import sys
import time
import urllib.error
import urllib.parse
import urllib.request
from datetime import date, datetime, timezone
from pathlib import Path

import numpy as np
from sklearn.linear_model import LogisticRegression
from sklearn.metrics import brier_score_loss, log_loss, roc_auc_score
from sklearn.preprocessing import StandardScaler

HERE = Path(__file__).resolve().parent
sys.path.insert(0, str(HERE.parents[1] / "backend" / "prediction" / "src"))
from prediction.features import FEATURES, Day, day_features, expand_poly2  # noqa: E402  (shared with the service)

OUT_PATH = HERE.parents[1] / "backend" / "prediction" / "src" / "prediction" / "model.json"
CACHE = HERE / ".cache"

START, END = "1991-01-01", "2024-12-31"
TEST_FROM_YEAR = 2020  # temporal hold-out: never evaluate on years the model trained on
HORIZONS = 7
SEED = 20261004
RECENCY_HALF_LIFE_YEARS = 10  # newer years count more: the climate is warming, so old heatwave rates mislead

# The app's warning rule (Go engine: EvaluateHeatRisk -> danger needs 2 consecutive days).
DANGER_C, EXTREME_DANGER_C = 41.0, 54.0

# name, lat, lon. India first (the project's home), then hot and humid places elsewhere. Downloads are
# cached in .cache/, so adding a city here only fetches that city (the free API rate-limits long
# histories, so add a few at a time; Sydney, Perth, Chicago, Paris, London, Singapore were left out
# for that reason).
CITIES = [
    ("Mumbai", 19.076, 72.8777), ("Delhi", 28.6139, 77.209), ("Nagpur", 21.1458, 79.0882),
    ("Ahmedabad", 23.0225, 72.5714), ("Jaipur", 26.9124, 75.7873), ("Chennai", 13.0827, 80.2707),
    ("Kolkata", 22.5726, 88.3639), ("Hyderabad", 17.385, 78.4867), ("Bengaluru", 12.9716, 77.5946),
    ("Pune", 18.5204, 73.8567), ("Lucknow", 26.8467, 80.9462), ("Bhopal", 23.2599, 77.4126),
    ("Patna", 25.5941, 85.1376), ("Visakhapatnam", 17.6868, 83.2185), ("Surat", 21.1702, 72.8311),
    ("Varanasi", 25.3176, 82.9739), ("Jodhpur", 26.2389, 73.0243), ("Raipur", 21.2514, 81.6296),
    ("Phoenix", 33.4484, -112.074), ("LasVegas", 36.1699, -115.1398), ("Dubai", 25.2048, 55.2708),
    ("Riyadh", 24.7136, 46.6753), ("KuwaitCity", 29.3759, 47.9774), ("Baghdad", 33.3152, 44.3661),
    ("Karachi", 24.8607, 67.0011), ("Dhaka", 23.8103, 90.4125), ("Bangkok", 13.7563, 100.5018),
    ("Cairo", 30.0444, 31.2357), ("Doha", 25.2854, 51.531), ("Houston", 29.7604, -95.3698),
    ("Miami", 25.7617, -80.1918), ("Manila", 14.5995, 120.9842), ("Lagos", 6.5244, 3.3792),
    ("Seville", 37.3891, -5.9845), ("Athens", 37.9838, 23.7275), ("Madrid", 40.4168, -3.7038),
    ("Rome", 41.9028, 12.4964),
]
LOCO_CITIES = ["Mumbai", "Delhi", "Phoenix", "Dubai", "Seville", "Houston"]  # held out one at a time
LOCO_HORIZON = 2


def forecast_sigma(lead: int) -> float:
    """Assumed std-dev (C) of forecast temperature error at a lead of ``lead`` days (0 = today).

    Roughly 1 C at one day out growing to ~2.4 C at six days, in line with published
    verification of global NWP temperature forecasts. An assumption, stated here so it
    can be challenged.
    """
    return 0.7 + 0.28 * lead


def fetch(name: str, lat: float, lon: float) -> dict[str, np.ndarray]:
    CACHE.mkdir(exist_ok=True)
    path = CACHE / f"{name.lower()}.json"
    if not path.exists():
        query = urllib.parse.urlencode({
            "latitude": lat, "longitude": lon, "start_date": START, "end_date": END, "timezone": "auto",
            "daily": "temperature_2m_max,temperature_2m_min,apparent_temperature_max",
        })
        url = f"https://archive-api.open-meteo.com/v1/archive?{query}"
        for attempt in range(8):
            try:
                with urllib.request.urlopen(url, timeout=120) as resp:
                    path.write_bytes(resp.read())
                break
            except urllib.error.HTTPError as err:
                if err.code != 429 or attempt == 7:
                    raise
                wait = 20 * (attempt + 1)
                print(f"    rate limited, waiting {wait}s", flush=True)
                time.sleep(wait)
        time.sleep(1.0)  # be polite to a free API
    daily = json.loads(path.read_text())["daily"]
    cols = {k: np.array([np.nan if v is None else v for v in daily[k]], dtype=float) for k in
            ("temperature_2m_max", "temperature_2m_min", "apparent_temperature_max")}
    cols["date"] = np.array([date.fromisoformat(t) for t in daily["time"]], dtype=object)
    return cols


def warning_day(app: np.ndarray) -> np.ndarray:
    """True where the app would raise a heatwave warning: >= 54 C, or >= 41 C two days running."""
    prev = np.concatenate([[np.nan], app[:-1]])
    return (app >= EXTREME_DANGER_C) | ((app >= DANGER_C) & (prev >= DANGER_C))


def build_rows(cols, horizon: int, rng: np.random.Generator):
    """Features and labels for every day at one forecast horizon, built from noisy 'forecasts'."""
    dates, tmax, tmin, app = cols["date"], cols["temperature_2m_max"], cols["temperature_2m_min"], cols["apparent_temperature_max"]
    label = warning_day(app)

    X, y, years = [], [], []
    for j in range(3, len(dates)):
        window = slice(j - 3, j + 1)
        if np.isnan(tmax[window]).any() or np.isnan(tmin[window]).any() or np.isnan(app[window]).any():
            continue
        days = []
        for k in range(3, -1, -1):  # k days before the target; its forecast lead is horizon - k
            lead = horizon - k
            sigma = forecast_sigma(lead) if lead >= 0 else 0.0  # before the issue date: observed
            e = rng.normal(0, sigma)
            idx = j - k
            days.append(Day(dates[idx], tmax[idx] + e, tmin[idx] + 0.8 * rng.normal(0, sigma),
                            app[idx] + 1.1 * e + rng.normal(0, 0.4 * sigma)))
        X.append(expand_poly2(day_features(days, 3)))
        y.append(bool(label[j]))
        years.append(dates[j].year)
    return np.array(X), np.array(y), np.array(years)


def expected_calibration_error(y, p, bins=10) -> float:
    edges = np.linspace(0, 1, bins + 1)
    ece = 0.0
    for lo, hi in zip(edges[:-1], edges[1:]):
        m = (p >= lo) & (p < hi if hi < 1 else p <= hi)
        if m.any():
            ece += m.mean() * abs(y[m].mean() - p[m].mean())
    return float(ece)


def fit(X, y, years, mask, last_year):
    w = 0.5 ** ((last_year - years[mask]) / RECENCY_HALF_LIFE_YEARS)
    scaler = StandardScaler().fit(X[mask])
    clf = LogisticRegression(C=1.0, max_iter=5000).fit(scaler.transform(X[mask]), y[mask], sample_weight=w)
    return scaler, clf


def main() -> None:
    print(f"Fetching {len(CITIES)} cities ({START} .. {END}) ...", flush=True)
    data = {}
    for name, lat, lon in CITIES:
        data[name] = fetch(name, lat, lon)
    print(f"  done ({sum(len(d['date']) for d in data.values()):,} city-days)", flush=True)

    horizons_out, evaluation, loco = [], [], []
    total_samples, base_rate = 0, 0.0
    for h in range(HORIZONS):
        rng = np.random.default_rng(SEED + h)
        parts = [build_rows(data[c[0]], h, rng) for c in CITIES]
        X = np.concatenate([p[0] for p in parts])
        y = np.concatenate([p[1] for p in parts])
        years = np.concatenate([p[2] for p in parts])
        city_idx = np.concatenate([np.full(len(p[1]), i) for i, p in enumerate(parts)])
        train, test = years < TEST_FROM_YEAR, years >= TEST_FROM_YEAR

        scaler, clf = fit(X, y, years, train, TEST_FROM_YEAR - 1)
        p = clf.predict_proba(scaler.transform(X[test]))[:, 1]
        apparent_only = X[test][:, FEATURES.index("apparent_max")]
        base = float(y[train].mean())
        e = {
            "horizon": h,
            "testSamples": int(test.sum()),
            "testBaseRate": round(float(y[test].mean()), 5),
            "meanPredicted": round(float(p.mean()), 5),
            "auc": round(float(roc_auc_score(y[test], p)), 4),
            "aucApparentAlone": round(float(roc_auc_score(y[test], apparent_only)), 4),
            "brier": round(float(brier_score_loss(y[test], p)), 5),
            "brierClimatology": round(float(brier_score_loss(y[test], np.full(test.sum(), base))), 5),
            "logLoss": round(float(log_loss(y[test], p)), 4),
            "ece": round(expected_calibration_error(y[test], p), 5),
        }
        evaluation.append(e)
        print(f"  h={h}: AUC {e['auc']} | Brier {e['brier']} vs climatology {e['brierClimatology']} | "
              f"ECE {e['ece']} | mean p {e['meanPredicted']} vs actual {e['testBaseRate']}", flush=True)

        if h == LOCO_HORIZON:  # how does it do in a city it has never seen?
            for name in LOCO_CITIES:
                i = [c[0] for c in CITIES].index(name)
                te, tr = city_idx == i, city_idx != i
                sc, cl = fit(X, y, years, tr, int(END[:4]))
                pp = cl.predict_proba(sc.transform(X[te]))[:, 1]
                loco.append({
                    "city": name,
                    "baseRate": round(float(y[te].mean()), 5),
                    "meanPredicted": round(float(pp.mean()), 5),
                    "auc": round(float(roc_auc_score(y[te], pp)), 4),
                    "brier": round(float(brier_score_loss(y[te], pp)), 5),
                    "brierClimatology": round(float(brier_score_loss(y[te], np.full(te.sum(), y[tr].mean()))), 5),
                })
                print(f"      held-out {name}: AUC {loco[-1]['auc']} | Brier {loco[-1]['brier']} vs {loco[-1]['brierClimatology']} "
                      f"| mean p {loco[-1]['meanPredicted']} vs actual {loco[-1]['baseRate']}", flush=True)

        scaler, clf = fit(X, y, years, np.ones_like(train), int(END[:4]))  # ship a model refit on every year
        horizons_out.append({
            "horizon": h,
            "intercept": float(clf.intercept_[0]),
            "coef": [float(c) for c in clf.coef_[0]],
            "mean": [float(m) for m in scaler.mean_],
            "scale": [float(s) for s in scaler.scale_],
        })
        total_samples += len(y)
        base_rate += float(y.mean()) / HORIZONS

    digest = hashlib.sha256(json.dumps(horizons_out, sort_keys=True).encode()).hexdigest()[:8]
    model = {
        "version": f"lr-{datetime.now(timezone.utc):%Y%m%d}-{digest}",
        "kind": "logistic-per-horizon",
        "trainedAt": datetime.now(timezone.utc).strftime("%Y-%m-%dT%H:%M:%SZ"),
        "features": FEATURES,
        "expansion": "poly2",
        "horizons": horizons_out,
        "training": {
            "target": "heatwave-warning day: apparent max >= 54C, or >= 41C on two consecutive days (the app's own rule)",
            "data": "ERA5 daily reanalysis via the Open-Meteo archive API",
            "years": f"{START[:4]}-{END[:4]}",
            "cities": [c[0] for c in CITIES],
            "forecastNoiseSigmaC": {"lead0": forecast_sigma(0), "perLeadDay": 0.28},
            "samples": total_samples,
            "baseRate": round(base_rate, 5),
            "seed": SEED,
            "recencyHalfLifeYears": RECENCY_HALF_LIFE_YEARS,
        },
        "evaluation": {
            "holdoutYears": f"{TEST_FROM_YEAR}-{END[:4]}",
            "note": "Temporal hold-out: trained on earlier years only. Warning days were more common in the holdout "
                    "years than before (see meanPredicted vs testBaseRate), so the held-out model under-predicts a little; "
                    "the shipped model is refit on all years with recent years weighted up. "
                    "leaveOneCityOut (horizon 2) trains without the named city and tests on it.",
            "perHorizon": evaluation,
            "leaveOneCityOut": {"horizon": LOCO_HORIZON, "cities": loco},
        },
    }
    OUT_PATH.write_text(json.dumps(model, indent=1) + "\n")
    print(f"\nWrote {OUT_PATH.relative_to(HERE.parents[1])}  (version {model['version']})")


if __name__ == "__main__":
    main()
