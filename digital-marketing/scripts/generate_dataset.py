"""Generate a synthetic marketing dataset for heatwave-monitor users.

No real customer data exists for this project, so we simulate it. Five hidden
audience groups are built in (each with its own age, income, behaviour, channel
and interest profile) and then noise, duplicates, missing values and
inconsistent entries are injected so the cleaning step has real work to do.
"""
from pathlib import Path

import numpy as np
import pandas as pd

rng = np.random.default_rng(42)
OUTPUT_PATH = Path(__file__).resolve().parent.parent / "data" / "customers.csv"

CITIES = {  # city -> state
    "Mumbai": "Maharashtra", "Pune": "Maharashtra", "Delhi": "Delhi",
    "Ahmedabad": "Gujarat", "Jaipur": "Rajasthan", "Hyderabad": "Telangana",
    "Chennai": "Tamil Nadu", "Bengaluru": "Karnataka", "Nagpur": "Maharashtra",
    "Lucknow": "Uttar Pradesh",
}
CITY_WEIGHTS = [0.24, 0.09, 0.15, 0.09, 0.07, 0.09, 0.08, 0.09, 0.05, 0.05]

# Hidden groups. Each: size, age (mean, sd), income in INR (mean, sd),
# visits/month, purchases/year, avg order value (INR), spending-score mean,
# occupations, education, channel weights, interest pool, gender share female.
GROUPS = {
    "outdoor_workers": dict(
        n=170, age=(31, 6), income=(280_000, 60_000), visits=(24, 5),
        purchases=(9, 2.5), aov=(350, 90), score=(38, 9), female=0.12,
        occupations=["Delivery Partner", "Construction Worker", "Traffic Police", "Street Vendor", "Cab Driver"],
        education=["High School", "Diploma"],
        channels={"WhatsApp": 0.45, "Push Notification": 0.30, "SMS": 0.20, "Email": 0.05},
        interests=["Hydration", "Work Safety", "Heat Alerts", "Cooling Gear"],
    ),
    "student_commuters": dict(
        n=200, age=(21, 2.5), income=(120_000, 40_000), visits=(14, 5),
        purchases=(4, 2), aov=(250, 80), score=(45, 12), female=0.50,
        occupations=["Student", "Intern"],
        education=["High School", "Bachelor's"],
        channels={"Instagram": 0.50, "YouTube": 0.20, "Push Notification": 0.20, "WhatsApp": 0.10},
        interests=["Weather Trends", "Campus Life", "Fitness", "Travel", "Gadgets"],
    ),
    "health_families_seniors": dict(
        n=160, age=(54, 9), income=(900_000, 250_000), visits=(9, 3),
        purchases=(6, 2), aov=(1_400, 350), score=(62, 10), female=0.55,
        occupations=["Retired", "Homemaker", "Teacher", "Doctor", "Government Employee"],
        education=["Bachelor's", "Master's"],
        channels={"Email": 0.40, "WhatsApp": 0.35, "Facebook": 0.15, "SMS": 0.10},
        interests=["Elderly Care", "Health Advisories", "Home Cooling", "Family Safety", "Hydration"],
    ),
    "fitness_enthusiasts": dict(
        n=110, age=(32, 5), income=(1_600_000, 350_000), visits=(20, 5),
        purchases=(16, 3), aov=(2_800, 600), score=(82, 8), female=0.45,
        occupations=["Software Engineer", "Marketing Manager", "Entrepreneur", "Fitness Trainer", "Consultant"],
        education=["Bachelor's", "Master's"],
        channels={"Instagram": 0.35, "Email": 0.25, "Google Search": 0.25, "YouTube": 0.15},
        interests=["Running", "Outdoor Sports", "Wearable Tech", "Travel", "Fitness"],
    ),
    "planners_organisers": dict(
        n=80, age=(41, 7), income=(1_300_000, 300_000), visits=(6, 2.5),
        purchases=(2, 1.2), aov=(4_500, 1_100), score=(70, 10), female=0.40,
        occupations=["Event Organiser", "School Administrator", "Facility Manager", "NGO Coordinator", "City Planner"],
        education=["Bachelor's", "Master's"],
        channels={"LinkedIn": 0.40, "Email": 0.40, "Google Search": 0.20},
        interests=["Event Planning", "Public Health", "Climate Data", "Heat Action Plans", "Reports"],
    ),
}


def build_group(spec):
    n = spec["n"]
    channels, weights = zip(*spec["channels"].items())
    interest_pairs = [
        "; ".join(rng.choice(spec["interests"], size=2, replace=False)) for _ in range(n)
    ]
    return pd.DataFrame({
        "age": rng.normal(*spec["age"], n).round().clip(18, 80),
        "gender": np.where(rng.random(n) < spec["female"], "Female", "Male"),
        "occupation": rng.choice(spec["occupations"], n),
        "education": rng.choice(spec["education"], n),
        "annual_income": (rng.normal(*spec["income"], n) // 1000 * 1000).clip(60_000),
        "website_visits_per_month": rng.normal(*spec["visits"], n).round().clip(1),
        "purchase_frequency": rng.normal(*spec["purchases"], n).round().clip(1),
        "avg_order_value": rng.normal(*spec["aov"], n).round().clip(100),
        "spending_score": rng.normal(*spec["score"], n).round().clip(1, 100),
        "preferred_channel": rng.choice(channels, n, p=weights),
        "interests": interest_pairs,
    })


def main():
    df = pd.concat([build_group(spec) for spec in GROUPS.values()], ignore_index=True)

    df["city"] = rng.choice(list(CITIES), len(df), p=CITY_WEIGHTS)
    df["state"] = df["city"].map(CITIES)
    df["country"] = "India"
    df = df.sample(frac=1, random_state=42).reset_index(drop=True)  # hide group order
    df.insert(0, "customer_id", [f"HW{i:04d}" for i in range(1, len(df) + 1)])

    # --- deliberately inject messiness for the cleaning step ---
    gender_variants = {"Male": ["male", "M", "MALE", " m"], "Female": ["female", "F", "FEMALE", "f "]}
    for idx in rng.choice(df.index, 90, replace=False):
        df.loc[idx, "gender"] = rng.choice(gender_variants[df.loc[idx, "gender"]])
    for idx in rng.choice(df.index, 40, replace=False):
        df.loc[idx, "city"] = rng.choice([df.loc[idx, "city"].upper(), df.loc[idx, "city"].lower() + " "])
    for idx in rng.choice(df.index[df["city"] == "Bengaluru"], 6, replace=False):
        df.loc[idx, "city"] = "Bangalore"
    for idx in rng.choice(df.index, 25, replace=False):
        df.loc[idx, "preferred_channel"] = df.loc[idx, "preferred_channel"].lower()

    for column, count in [("age", 18), ("annual_income", 25), ("gender", 12),
                          ("preferred_channel", 15), ("interests", 10), ("avg_order_value", 12)]:
        df.loc[rng.choice(df.index, count, replace=False), column] = np.nan

    duplicates = df.sample(20, random_state=7)
    df = pd.concat([df, duplicates], ignore_index=True).sample(frac=1, random_state=3).reset_index(drop=True)

    OUTPUT_PATH.parent.mkdir(parents=True, exist_ok=True)
    df.to_csv(OUTPUT_PATH, index=False)
    print(f"Saved {len(df)} rows to {OUTPUT_PATH.name}")


if __name__ == "__main__":
    main()
