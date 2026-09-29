# Experiment No. 08 — AI-Based Customer Persona Generation from Marketing Data

## Aim

To generate AI-based customer personas from marketing data using Python (pandas, scikit-learn, matplotlib) in a Jupyter notebook, by cleaning and scaling the data, segmenting customers with k-means clustering, and turning each segment into a data-driven persona with a recommended digital marketing strategy.

The product studied is **Heatwave Monitor**, a free real-time heatwave risk dashboard. It has no real customer database, so a synthetic dataset of 740 user records (`data/customers.csv`, generated with seed 42) was used to represent its audience.

## Results

Notebook: `notebooks/expt_08_persona_generation.ipynb`. Figures are in `outputs/figures/`.

**Step 1 — Install Python and libraries.** A `uv` project was created and pandas, numpy, scikit-learn, matplotlib and jupyter were installed into its own `.venv` with `uv add` / `uv sync`. *Snapshot: notebook first markdown cell (and the `uv sync` terminal output).*

**Step 2 — Load the dataset.** `customers.csv` was loaded with `pd.read_csv`: 740 rows × 15 columns. *Snapshot: notebook output of `raw.head()`.*

**Step 3 — Explore the dataset.** `info()`, `describe()`, missing-value and duplicate counts were inspected. Relevant attributes: demographic (age, gender, occupation, education, income), geographic (city, state, country), behavioural (spending score, purchase frequency, site visits, order value, preferred channel) and psychographic (interests). Before cleaning: 94 missing cells, 20 duplicate rows, 10 gender spellings, 28 city spellings and 18 channel spellings. *Snapshot: `01_data_exploration.png` and the notebook outputs of `info()`, `describe()` and the quality checks.*

**Step 4 — Clean the data.** Duplicates were dropped, gender/city/channel labels were standardised, numeric gaps were filled with the median and categorical gaps with the mode. After cleaning: 720 rows, 0 duplicates, 0 missing cells, 2 gender values, 10 cities and 9 channels. *Snapshot: notebook "before/after" table.*

**Step 5 — Select features.** Six numeric features were chosen: age, annual income, spending score, purchase frequency, website visits per month and average order value. Categorical columns were kept to describe the clusters afterwards. *Snapshot: notebook output of `X.describe()`.*

**Step 6 — Normalise with StandardScaler.** All features were rescaled to mean 0 and standard deviation 1 so income (in lakhs) does not dominate age. *Snapshot: `02_scaling_before_after.png` and the before/after summary-statistics table.*

**Step 7 — Apply k-means.** Elbow and silhouette analysis was run for k = 2–10 (`random_state=42`, `n_init=10`). The elbow appears at k = 5. The silhouette score is highest at k = 3 (0.472) and k = 4 (0.469) and is 0.433 at k = 5, before dropping for k ≥ 6. k = 5 was chosen as a trade-off between statistical separation and distinct, actionable personas. *Snapshot: `03_elbow.png`, `04_silhouette.png` and the k-scores table.*

**Step 8 — Visualise the clusters.** An income-versus-spending scatter plot with centroids, a PCA 2-D projection, bar charts of feature means and a stacked bar of preferred channels per segment. *Snapshot: `05_clusters_scatter.png`, `06_clusters_pca.png`, `07_segment_feature_means.png`, `08_channel_by_segment.png`.*

**Step 9 — Analyse clusters and create personas.** A cluster profile table (size, average age, income, spending, purchases, visits, order value, top channels, interests, occupations, cities) was built, and a persona written for each segment. *Snapshot: notebook output of the profile table and persona cards; `outputs/personas.csv`.*

| Segment | Persona | Users (%) | Age | Income (INR/yr) | Spending profile | Top channels | Top interests |
|---|---|---|---|---|---|---|---|
| 1 | Budget-Conscious Campus Commuter | 200 (27.8%) | ~22 | ~1.4 lakh | score 47, 4 purchases/yr, ~INR 258/order | Instagram, YouTube | Fitness, Campus Life, Weather Trends |
| 2 | Outdoor Frontline Worker | 171 (23.8%) | ~31 | ~2.7 lakh | score 36, 9 purchases/yr, ~INR 349/order, 25 visits/month | WhatsApp, Push Notification | Heat Alerts, Work Safety, Hydration |
| 3 | Health-Conscious Family Guardian | 165 (22.9%) | ~54 | ~9.0 lakh | score 63, 6 purchases/yr, ~INR 1,378/order | Email, WhatsApp | Home Cooling, Elderly Care, Health Advisories |
| 4 | Institutional Heat Planner | 75 (10.4%) | ~41 | ~13.4 lakh | score 69, 2 purchases/yr, ~INR 4,632/order | Email, LinkedIn | Public Health, Heat Action Plans, Climate Data |
| 5 | Active Urban Fitness Enthusiast | 109 (15.1%) | ~32 | ~14.8 lakh | score 84, 16 purchases/yr, ~INR 2,797/order | Email, Google Search | Fitness, Running, Travel |

**Step 10 — Summarise personas and suggest strategies.**

| Persona | Recommended digital marketing strategy |
|---|---|
| Campus Commuter | Instagram Reels / YouTube Shorts heat tips, shareable PNG heat report as a viral loop, push notifications, student-discount hydration affiliate offers. |
| Outdoor Frontline Worker | WhatsApp broadcast and SMS/push alerts at the Danger tier, Hindi/Marathi micro-content, gig-platform partnerships, local SEO, low-cost retargeting. |
| Family Guardian | Weekly family heat-safety email, SEO-optimised long-form guides on heat illness, Facebook community posts, retargeting for home-cooling products. |
| Institutional Planner | LinkedIn thought leadership on the methodology page, Google Search ads on "heat action plan", email nurture drip to a premium report/API offer, webinars. |
| Fitness Enthusiast | Personalised "best hour to train" email, Google Search ads and retargeting for wearables, premium upsell, Instagram/YouTube creator partnerships. |

*Snapshot: notebook output of the persona summary and strategy tables; `outputs/personas.csv`.*

## Questions

**Explain the impact of AI on social media.**

Artificial intelligence now sits behind almost every part of social media. Its most visible effect is on **content recommendation**: machine-learning models rank feeds, suggest videos and surface accounts based on each user's behaviour, which keeps users engaged but also shapes what they see. The same behavioural data powers **personalisation and targeted advertising**. Platforms segment users by demographics, interests and activity, so advertisers can reach narrow audiences with tailored messages, which is essentially what this experiment does on a small scale with k-means.

**Generative AI** lets brands and creators produce text, images, video and captions quickly and at low cost, and lets marketers test many creative variations. **Chatbots** handle routine customer queries around the clock, while **social listening and sentiment analysis** let brands monitor conversations, spot trends and respond to complaints or crises early. In **influencer marketing**, analytics help brands identify relevant creators, check audience fit and measure campaign results.

AI also brings serious risks. Automated **moderation** helps detect spam, abuse and harmful content at scale, but it can make mistakes and struggles with context. Generative tools make **misinformation and deepfakes** easier to create and harder to detect. **Algorithmic bias** can appear when models learn from skewed data, leading to unequal reach or unfair targeting, and the heavy use of personal data raises **privacy** concerns, which is why data-protection regulation and transparent consent matter.

Looking ahead, we can expect more real-time personalisation, AI-generated and AI-labelled content, conversational and voice-based commerce, and stronger demands for transparency and ethical use.

For marketers, the lesson from persona generation is that AI turns raw data into actionable segments, but the personas must be built from good, clean data and used responsibly, with human judgement guiding the strategy.

## Outcomes

- Loaded, explored and cleaned a marketing dataset: 20 duplicate rows removed, 94 missing cells filled, and inconsistent gender, city and channel labels standardised (740 → 720 clean records).
- Selected six behavioural and demographic features and scaled them with `StandardScaler`.
- Chose k = 5 using the elbow method and silhouette analysis (silhouette 0.433 at k = 5), with a written justification.
- Segmented users with k-means and visualised the result with a scatter plot, a PCA projection, feature-mean bar charts and a channel chart (8 saved figures).
- Generated five data-driven personas with age group, income level, spending behaviour, shopping preferences, interests, goals, pain points, preferred channels and a recommended digital marketing strategy, saved in `outputs/personas.csv`.
- Answered the question on the impact of AI on social media.

## Conclusion

The aim of generating customer personas with AI was achieved. Starting from a raw, messy dataset, we cleaned and scaled the data, used k-means to separate Heatwave Monitor's audience into five distinct segments, and turned each into a persona that is supported by the cluster numbers. The experiment showed why preprocessing matters (scaling stops income from dominating) and that the choice of k is a judgement that balances statistical scores against practical usefulness. Each persona maps to different channels and tactics, such as WhatsApp alerts for outdoor workers and personalised email and search for fitness users, so segmentation makes marketing more targeted and efficient. The main limitation is that the data is synthetic, so the results demonstrate the method rather than real audience behaviour. With real analytics data the same workflow could be applied directly.
