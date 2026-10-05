from datetime import date

from prediction.features import FEATURES, Day, day_features, expand_poly2


def d(day, tmax, tmin=25.0, app=None):
    return Day(date(2026, 5, day), tmax, tmin, tmax + 2 if app is None else app)


def test_feature_vector_matches_the_documented_layout():
    series = [d(1, 36, app=38), d(2, 38, app=41), d(3, 40, app=44), d(4, 44, tmin=29, app=48)]
    f = dict(zip(FEATURES, day_features(series, 3)))
    assert f == {"apparent_max": 48, "apparent_prev1": 44, "apparent_prev2": 41, "tmax": 44, "tmin": 29, "heat_gap": 4}


def test_only_the_two_previous_days_matter():
    series = [d(1, 10, app=11), d(2, 20, app=21), d(3, 30, app=31), d(4, 40, app=41)]
    f = dict(zip(FEATURES, day_features(series, 3)))
    assert f["apparent_prev1"] == 31 and f["apparent_prev2"] == 21  # day 1 is ignored


def test_missing_history_is_filled_from_the_nearest_known_day():
    first = dict(zip(FEATURES, day_features([d(1, 41, app=45)], 0)))
    assert first["apparent_prev1"] == 45 and first["apparent_prev2"] == 45
    second = dict(zip(FEATURES, day_features([d(1, 30, app=33), d(2, 41, app=45)], 1)))
    assert second["apparent_prev1"] == 33 and second["apparent_prev2"] == 33


def test_features_ignore_dates_so_the_model_is_not_seasonal_or_regional():
    a = day_features([Day(date(2026, 1, 5), 40, 28, 44)], 0)
    b = day_features([Day(date(2026, 7, 5), 40, 28, 44)], 0)
    assert a == b


def test_expand_poly2_layout_matches_sklearn_polynomial_features():
    # sklearn PolynomialFeatures(2, include_bias=False) on [a, b, c]:
    # a, b, c, a², ab, ac, b², bc, c²
    assert expand_poly2([2.0, 3.0, 5.0]) == [2.0, 3.0, 5.0, 4.0, 6.0, 10.0, 9.0, 15.0, 25.0]
    n = len(FEATURES)
    assert len(expand_poly2([0.0] * n)) == n + n * (n + 1) // 2
