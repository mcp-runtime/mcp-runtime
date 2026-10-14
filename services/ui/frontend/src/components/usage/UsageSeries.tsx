import type { UsageTimePoint } from "../../api/types";
import { formatTimestamp } from "../../lib/format";

const rateLabel = (value: number) =>
  value.toLocaleString(undefined, { maximumSignificantDigits: 3 });

export function UsageSeries({
  points,
  testId = "usage-series",
  bucketSeconds,
}: {
  points: UsageTimePoint[];
  testId?: string;
  bucketSeconds?: number;
}) {
  if (points.length === 0) return null;
  const ordered = [...points].sort(
    (a, b) => Date.parse(a.bucket) - Date.parse(b.bucket),
  );
  const width = 800,
    height = 252;
  const left = 78,
    right = 22,
    top = 20,
    bottom = 57;
  const first = Date.parse(ordered[0].bucket),
    last = Date.parse(ordered[ordered.length - 1].bucket);
  const step =
    bucketSeconds ||
    (ordered.length > 1
      ? Math.max((last - first) / 1000 / (ordered.length - 1), 1)
      : 3600);
  const rates = ordered.map(
    (point) => point.requests_per_second ?? point.events / step,
  );
  const maxRate = Math.max(...rates, 0);
  const peak = maxRate || 1;
  const x = (time: number) =>
    left +
    ((time - first) / Math.max(last - first, 1)) * (width - left - right);
  const y = (rate: number) =>
    height - bottom - (rate / peak) * (height - top - bottom);
  const line = ordered
    .map((point, index) => `${x(Date.parse(point.bucket))},${y(rates[index])}`)
    .join(" ");
  const tickIndexes = [
    ...new Set(
      Array.from({ length: Math.min(5, ordered.length) }, (_, i) =>
        Math.round(
          (i * (ordered.length - 1)) /
            Math.max(Math.min(5, ordered.length) - 1, 1),
        ),
      ),
    ),
  ];
  const timeLabel = (bucket: string) =>
    new Date(bucket).toLocaleString(
      undefined,
      last - first > 2 * 86400000
        ? { month: "short", day: "numeric" }
        : { hour: "2-digit", minute: "2-digit" },
    );
  return (
    <section
      className="usage-series"
      data-testid={testId}
      aria-labelledby={`${testId}-title`}
    >
      <div className="section-head">
        <div>
          <h2 className="section-title" id={`${testId}-title`}>
            Requests over time
          </h2>
          <p className="section-note">
            Request rate for the selected filters. Hover a point for its time,
            count and rate.
          </p>
        </div>
        <span className="series-legend">
          <span className="series-dot" aria-hidden="true" /> Requests / second
        </span>
      </div>
      <div
        className="usage-series-scroll"
        tabIndex={0}
        aria-label="Requests over time chart"
      >
        <svg
          className="usage-series-chart"
          viewBox={`0 0 ${width} ${height}`}
          role="img"
          aria-label={`${ordered.length} time buckets; peak ${rateLabel(maxRate)} requests per second`}
        >
          {[0, 1, 2, 3, 4].map((i) => {
            const value = (peak * i) / 4;
            return (
              <g key={i}>
                <line
                  x1={left}
                  y1={y(value)}
                  x2={width - right}
                  y2={y(value)}
                  className="series-grid"
                />
                <text
                  x={left - 10}
                  y={y(value) + 4}
                  textAnchor="end"
                  className="series-tick"
                >
                  {rateLabel(value)}
                </text>
              </g>
            );
          })}
          <line
            x1={left}
            y1={top}
            x2={left}
            y2={height - bottom}
            className="series-axis"
          />
          <line
            x1={left}
            y1={height - bottom}
            x2={width - right}
            y2={height - bottom}
            className="series-axis"
          />
          <text
            transform={`translate(16 ${(top + height - bottom) / 2}) rotate(-90)`}
            textAnchor="middle"
            className="series-axis-label"
          >
            Requests / second
          </text>
          <text
            x={(left + width - right) / 2}
            y={height - 8}
            textAnchor="middle"
            className="series-axis-label"
          >
            Time (local)
          </text>
          {tickIndexes.map((i) => (
            <text
              key={i}
              x={x(Date.parse(ordered[i].bucket))}
              y={height - bottom + 21}
              textAnchor="middle"
              className="series-tick"
            >
              {timeLabel(ordered[i].bucket)}
            </text>
          ))}
          <polyline points={line} className="series-line" fill="none" />
          {ordered.map((point, index) => (
            <circle
              key={point.bucket}
              cx={x(Date.parse(point.bucket))}
              cy={y(rates[index])}
              r="3"
              className="series-point"
            >
              <title>{`${formatTimestamp(point.bucket)}: ${point.events.toLocaleString()} requests; ${rateLabel(rates[index])} requests/second`}</title>
            </circle>
          ))}
        </svg>
      </div>
    </section>
  );
}
