import { useEffect, useMemo, useState } from "react";

export function EnvironmentClock({ timezone }) {
  const [now, setNow] = useState(Date.now);
  const formatter = useMemo(() => {
    if (!timezone) return null;
    try {
      return new Intl.DateTimeFormat("en-GB", {
        timeZone: timezone,
        weekday: "short",
        hour: "2-digit",
        minute: "2-digit",
        hourCycle: "h23",
      });
    } catch { return null; }
  }, [timezone]);

  useEffect(() => {
    let timer;
    const update = () => {
      clearTimeout(timer);
      const instant = Date.now();
      setNow(instant);
      timer = setTimeout(update, 60000 - instant % 60000);
    };
    update();
    window.addEventListener("focus", update);
    document.addEventListener("visibilitychange", update);
    return () => {
      clearTimeout(timer);
      window.removeEventListener("focus", update);
      document.removeEventListener("visibilitychange", update);
    };
  }, []);

  const label = formatter ? `当地时间（${timezone}）` : "当地时间：时区不可用";
  return <time className="status-clock" dateTime={new Date(now).toISOString()} title={label} aria-label={label}>
    {formatter ? formatter.format(now) : "--:--"}
  </time>;
}
