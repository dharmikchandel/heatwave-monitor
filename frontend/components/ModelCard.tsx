import type { ModelInfo } from "@/lib/backend";

/** What the heatwave model is and how well it did on years it never saw. */
export default function ModelCard({ model }: { model: ModelInfo }) {
  if (!model.loaded) {
    return (
      <section aria-labelledby="model-title" className="glass-card rounded-2xl p-5">
        <h2 id="model-title" className="text-sm font-bold uppercase tracking-wide text-muted">
          Heatwave model
        </h2>
        <p className="mt-2 text-sm text-muted">No trained model is loaded; the prediction service is using its rule-based fallback.</p>
      </section>
    );
  }

  const horizons = model.evaluation?.perHorizon ?? [];
  const first = horizons[0];
  const last = horizons[horizons.length - 1];
  const unseen = model.evaluation?.leaveOneCityOut?.cities.filter((c) => c.auc !== null) ?? [];
  const improvement = (h: { brier: number; brierClimatology: number }) => Math.round((1 - h.brier / h.brierClimatology) * 100);

  return (
    <section aria-labelledby="model-title" className="glass-card rounded-2xl p-5">
      <h2 id="model-title" className="text-sm font-bold uppercase tracking-wide text-muted">
        Heatwave model
      </h2>
      <dl className="mt-3 grid gap-x-8 gap-y-2 text-sm sm:grid-cols-2">
        <div>
          <dt className="text-xs font-semibold text-muted">Version</dt>
          <dd className="font-mono text-xs">{model.version}</dd>
        </div>
        <div>
          <dt className="text-xs font-semibold text-muted">Predicts</dt>
          <dd>{model.training?.target ?? "heatwave-warning days"}</dd>
        </div>
        {model.training && (
          <div>
            <dt className="text-xs font-semibold text-muted">Trained on</dt>
            <dd>
              {model.training.years} weather from {model.training.cities.length} cities
            </dd>
          </div>
        )}
        {first && last && (
          <div>
            <dt className="text-xs font-semibold text-muted">Held-out accuracy ({model.evaluation?.holdoutYears})</dt>
            <dd>
              {improvement(first)}% lower error than climatology today, {improvement(last)}% {horizons.length - 1} days out
            </dd>
          </div>
        )}
        {unseen.length > 0 && (
          <div className="sm:col-span-2">
            <dt className="text-xs font-semibold text-muted">On cities it never saw</dt>
            <dd>
              {unseen.map((c) => `${c.city} (AUC ${c.auc})`).join(", ")}
            </dd>
          </div>
        )}
      </dl>
    </section>
  );
}
