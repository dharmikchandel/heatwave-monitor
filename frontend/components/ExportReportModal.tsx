"use client";

import { AnimatePresence, motion } from "framer-motion";
import { Check, Copy, Download, X } from "lucide-react";
import { useEffect, useId, useRef, useState, type KeyboardEvent } from "react";
import { RISK_LEVEL_LABEL } from "@/lib/heatwaveEngine";
import type { ClimateData, HeatwaveAssessment } from "@/lib/types";
import { celsiusToUnit, cn, FOCUS_RING, RISK_LEVEL_COLOR } from "@/lib/utils";

const FOCUSABLE_SELECTOR =
  'a[href], button:not([disabled]), textarea, input, select, [tabindex]:not([tabindex="-1"])';

interface ExportReportModalProps {
  isOpen: boolean;
  onClose: () => void;
  locationName: string;
  assessment: HeatwaveAssessment;
  climateData: ClimateData;
  unit: "C" | "F";
}

const CARD_WIDTH = 800;
const CARD_HEIGHT = 450;

export default function ExportReportModal({
  isOpen,
  onClose,
  locationName,
  assessment,
  climateData,
  unit,
}: ExportReportModalProps) {
  const canvasRef = useRef<HTMLCanvasElement>(null);
  const dialogRef = useRef<HTMLDivElement>(null);
  const closeButtonRef = useRef<HTMLButtonElement>(null);
  const previouslyFocusedRef = useRef<HTMLElement | null>(null);
  const titleId = useId();
  const [copied, setCopied] = useState(false);

  const reportDate = new Date(climateData.current.time).toLocaleDateString("en-US", {
    weekday: "long",
    month: "long",
    day: "numeric",
    year: "numeric",
  });

  const summaryText = `Heatwave Monitor Report — ${locationName}
${reportDate}

Risk Level: ${RISK_LEVEL_LABEL[assessment.riskLevel]}
Heat Index: ${Math.round(celsiusToUnit(assessment.heatIndexC, unit))}°${unit}
Current Temp: ${Math.round(celsiusToUnit(climateData.current.temperature2m, unit))}°${unit}
Feels Like: ${Math.round(celsiusToUnit(climateData.current.apparentTemperature, unit))}°${unit}
Humidity: ${Math.round(climateData.current.relativeHumidity2m)}%

${assessment.message}`;

  useEffect(() => {
    if (!isOpen) return;
    const canvas = canvasRef.current;
    if (!canvas) return;
    const ctx = canvas.getContext("2d");
    if (!ctx) return;

    const color = RISK_LEVEL_COLOR[assessment.riskLevel];

    canvas.width = CARD_WIDTH;
    canvas.height = CARD_HEIGHT;

    const bgGradient = ctx.createLinearGradient(0, 0, CARD_WIDTH, CARD_HEIGHT);
    bgGradient.addColorStop(0, "#0b0f17");
    bgGradient.addColorStop(1, "#161e2e");
    ctx.fillStyle = bgGradient;
    ctx.fillRect(0, 0, CARD_WIDTH, CARD_HEIGHT);

    const glow = ctx.createRadialGradient(CARD_WIDTH - 100, 80, 10, CARD_WIDTH - 100, 80, 320);
    glow.addColorStop(0, `${color}55`);
    glow.addColorStop(1, "transparent");
    ctx.fillStyle = glow;
    ctx.fillRect(0, 0, CARD_WIDTH, CARD_HEIGHT);

    ctx.fillStyle = "#f1f5f9";
    ctx.font = "bold 22px system-ui, sans-serif";
    ctx.fillText("🌡 Heatwave Monitor", 48, 60);
    ctx.fillStyle = "#94a3b8";
    ctx.font = "14px system-ui, sans-serif";
    ctx.fillText("Climate Intelligence Dashboard", 48, 82);

    ctx.fillStyle = "#f1f5f9";
    ctx.font = "bold 34px system-ui, sans-serif";
    ctx.fillText(locationName, 48, 150);
    ctx.fillStyle = "#94a3b8";
    ctx.font = "16px system-ui, sans-serif";
    ctx.fillText(reportDate, 48, 178);

    ctx.fillStyle = color;
    ctx.font = "800 96px system-ui, sans-serif";
    ctx.fillText(`${Math.round(celsiusToUnit(assessment.heatIndexC, unit))}°${unit}`, 48, 290);
    ctx.fillStyle = "#94a3b8";
    ctx.font = "14px system-ui, sans-serif";
    ctx.fillText("HEAT INDEX", 48, 310);

    ctx.fillStyle = `${color}22`;
    const badgeText = RISK_LEVEL_LABEL[assessment.riskLevel].toUpperCase();
    ctx.font = "bold 16px system-ui, sans-serif";
    const badgeWidth = ctx.measureText(badgeText).width + 40;
    ctx.beginPath();
    ctx.roundRect(48, 330, badgeWidth, 36, 18);
    ctx.fill();
    ctx.fillStyle = color;
    ctx.fillText(badgeText, 68, 354);

    const metrics = [
      ["Current", `${Math.round(celsiusToUnit(climateData.current.temperature2m, unit))}°${unit}`],
      ["Feels Like", `${Math.round(celsiusToUnit(climateData.current.apparentTemperature, unit))}°${unit}`],
      ["Humidity", `${Math.round(climateData.current.relativeHumidity2m)}%`],
    ];
    metrics.forEach(([label, value], i) => {
      const x = 420 + i * 120;
      ctx.fillStyle = "#f1f5f9";
      ctx.font = "bold 24px system-ui, sans-serif";
      ctx.fillText(value, x, 290);
      ctx.fillStyle = "#94a3b8";
      ctx.font = "12px system-ui, sans-serif";
      ctx.fillText(label.toUpperCase(), x, 312);
    });

    ctx.strokeStyle = "rgba(148,163,184,0.15)";
    ctx.beginPath();
    ctx.moveTo(48, CARD_HEIGHT - 60);
    ctx.lineTo(CARD_WIDTH - 48, CARD_HEIGHT - 60);
    ctx.stroke();

    ctx.fillStyle = "#64748b";
    ctx.font = "12px system-ui, sans-serif";
    ctx.fillText("Generated client-side from Open-Meteo climate data", 48, CARD_HEIGHT - 34);
  }, [isOpen, locationName, reportDate, assessment, climateData, unit]);

  useEffect(() => {
    if (isOpen) {
      previouslyFocusedRef.current = document.activeElement as HTMLElement | null;
      closeButtonRef.current?.focus();
    } else {
      previouslyFocusedRef.current?.focus();
    }
  }, [isOpen]);

  function handleDialogKeyDown(e: KeyboardEvent<HTMLDivElement>) {
    if (e.key === "Escape") {
      e.stopPropagation();
      onClose();
      return;
    }
    if (e.key !== "Tab" || !dialogRef.current) return;

    const focusable = Array.from(dialogRef.current.querySelectorAll<HTMLElement>(FOCUSABLE_SELECTOR));
    if (focusable.length === 0) return;
    const first = focusable[0];
    const last = focusable[focusable.length - 1];

    if (e.shiftKey && document.activeElement === first) {
      e.preventDefault();
      last.focus();
    } else if (!e.shiftKey && document.activeElement === last) {
      e.preventDefault();
      first.focus();
    }
  }

  function handleDownload() {
    const canvas = canvasRef.current;
    if (!canvas) return;
    canvas.toBlob((blob) => {
      if (!blob) return;
      const url = URL.createObjectURL(blob);
      const a = document.createElement("a");
      a.href = url;
      a.download = `heatwave-report-${locationName.toLowerCase().replace(/[^a-z0-9]+/g, "-")}.png`;
      document.body.appendChild(a);
      a.click();
      a.remove();
      URL.revokeObjectURL(url);
    }, "image/png");
  }

  async function handleCopy() {
    try {
      await navigator.clipboard.writeText(summaryText);
      setCopied(true);
      setTimeout(() => setCopied(false), 1800);
    } catch {
      // Clipboard API may be unavailable (permissions, insecure context) — no-op.
    }
  }

  return (
    <AnimatePresence>
      {isOpen && (
        <motion.div
          initial={{ opacity: 0 }}
          animate={{ opacity: 1 }}
          exit={{ opacity: 0 }}
          className="fixed inset-0 z-50 flex items-center justify-center bg-black/60 p-4 backdrop-blur-sm"
          onClick={onClose}
        >
          <motion.div
            ref={dialogRef}
            role="dialog"
            aria-modal="true"
            aria-labelledby={titleId}
            onKeyDown={handleDialogKeyDown}
            initial={{ opacity: 0, scale: 0.95, y: 12 }}
            animate={{ opacity: 1, scale: 1, y: 0 }}
            exit={{ opacity: 0, scale: 0.95, y: 12 }}
            transition={{ duration: 0.2 }}
            onClick={(e) => e.stopPropagation()}
            className="glass-card w-full max-w-2xl rounded-2xl p-5 shadow-2xl"
          >
            <div className="mb-4 flex items-center justify-between">
              <h2 id={titleId} className="text-base font-bold">
                Export Heatwave Report
              </h2>
              <button
                ref={closeButtonRef}
                type="button"
                onClick={onClose}
                aria-label="Close"
                className={cn("flex h-8 w-8 items-center justify-center rounded-full text-muted transition hover:bg-surface", FOCUS_RING)}
              >
                <X className="h-4 w-4" aria-hidden="true" />
              </button>
            </div>

            <div className="overflow-hidden rounded-xl border border-surface-border">
              <canvas
                ref={canvasRef}
                role="img"
                aria-label={summaryText}
                className="block w-full"
                style={{ aspectRatio: `${CARD_WIDTH} / ${CARD_HEIGHT}` }}
              />
            </div>

            <div className="mt-4 flex flex-wrap gap-2">
              <button
                type="button"
                onClick={handleDownload}
                className={cn(
                  "flex items-center gap-2 rounded-full bg-orange-600 px-4 py-2 text-sm font-semibold text-white transition hover:bg-orange-500",
                  FOCUS_RING,
                )}
              >
                <Download className="h-4 w-4" aria-hidden="true" /> Download PNG
              </button>
              <button
                type="button"
                onClick={handleCopy}
                className={cn(
                  "flex items-center gap-2 rounded-full border border-surface-border bg-surface/60 px-4 py-2 text-sm font-semibold transition hover:bg-surface",
                  FOCUS_RING,
                )}
              >
                {copied ? <Check className="h-4 w-4 text-emerald-500" aria-hidden="true" /> : <Copy className="h-4 w-4" aria-hidden="true" />}
                {copied ? "Copied!" : "Copy Summary Text"}
              </button>
              <span className="sr-only" role="status" aria-live="polite">
                {copied ? "Summary copied to clipboard" : ""}
              </span>
            </div>
          </motion.div>
        </motion.div>
      )}
    </AnimatePresence>
  );
}
