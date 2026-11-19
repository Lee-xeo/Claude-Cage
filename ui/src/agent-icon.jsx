import React from "react";

export function AccountIconNumber({ number, y }) {
  const digits = String(number);
  return <text x="12" y={y} dominantBaseline="central" textAnchor="middle"
    fill="currentColor" stroke="none" fontFamily="var(--ui-font-family, system-ui, sans-serif)"
    fontSize={digits.length === 1 ? 12 : digits.length === 2 ? 10 : 8} fontWeight="700"
    {...(digits.length > 2 ? { textLength: 13, lengthAdjust: "spacingAndGlyphs" } : {})}>{digits}</text>;
}

// Keep the account number legible in conversation rows.
// The outline inherits the chrome color; the face uses the same UI font.
export function AgentIcon({ accountNumber, size = 20 }) {
  const number = Number.isInteger(accountNumber) && accountNumber > 0 ? String(accountNumber) : "";
  return <svg className="agent-icon" width={size} height={size} viewBox="0 0 24 24"
    fill="none" stroke="currentColor" strokeWidth="1.75" strokeLinecap="round" strokeLinejoin="round"
    role="img" aria-label={number ? `Agent · 账号 ${number}` : "Agent"}>
    <path d="M12 3v3M2 11v5m20-5v5" />
    <rect x="4" y="6" width="16" height="15" rx="4" />
    {number ? <AccountIconNumber number={number} y={14} />
      : <><path d="M8.5 11v2m7-2v2M9 17h6" /></>}
  </svg>;
}
