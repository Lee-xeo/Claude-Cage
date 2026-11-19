import React, { useEffect, useState } from "react";
import { ArrowUp, ArrowDown } from "lucide-react";
import { api } from "./api";

function speedLabel(value) {
  if (value == null) return "—";
  if (value < 1024) return `${Math.round(value)} B/s`;
  if (value < 1024 ** 2) return `${(value / 1024).toFixed(1)} KiB/s`;
  if (value < 1024 ** 3) return `${(value / 1024 ** 2).toFixed(1)} MiB/s`;
  return `${(value / 1024 ** 3).toFixed(1)} GiB/s`;
}

export function NetworkSpeed({ enabled }) {
  const [rates, setRates] = useState(null);
  useEffect(() => {
    if (!enabled) return;
    const controller = new AbortController();
    let previous = null, timer;
    const measure = async () => {
      try {
        const result = await api("/proxy/connections", undefined, undefined, {}, {
          signal: controller.signal,
        });
        if (controller.signal.aborted) return;
        const current = {
          up: result.uploadTotal,
          down: result.downloadTotal,
          time: performance.now(),
        };
        if (![current.up, current.down].every(value => Number.isFinite(value) && value >= 0)) {
          throw new Error("流量数据不可用");
        }
        // A restarted core or an unavailable core can reset the counters.
        // Collect a fresh baseline before calculating another rate.
        if (previous && (current.up < previous.up || current.down < previous.down)) {
          previous = null;
          setRates(null);
        } else {
          const seconds = previous && (current.time - previous.time) / 1000;
          setRates(seconds > 0 ? {
            up: (current.up - previous.up) / seconds,
            down: (current.down - previous.down) / seconds,
          } : null);
          previous = current;
        }
      } catch {
        if (controller.signal.aborted) return;
        previous = null;
        setRates(null);
      } finally {
        if (!controller.signal.aborted) timer = setTimeout(measure, 1000);
      }
    };
    measure();
    return () => {
      controller.abort();
      clearTimeout(timer);
    };
  }, [enabled]);

  return (
    <span className="status-network-speed" role="group" aria-label="代理实时速度">
      <span>
        <ArrowUp size={12} role="img" aria-label="上行速度" tabIndex={0} />
        <span>{speedLabel(enabled ? rates?.up : null)}</span>
      </span>
      <span>
        <ArrowDown size={12} role="img" aria-label="下行速度" tabIndex={0} />
        <span>{speedLabel(enabled ? rates?.down : null)}</span>
      </span>
    </span>
  );
}
