import React, { createContext, useContext } from "react";
import { isSafetyCheckMessage } from "./safety-state.mjs";
import { useTranslation } from "./interface-language.mjs";
import { localizeBackendMessage } from "./backend-message.mjs";

export const SafetyChecklistContext = createContext(null);

export function SafetyMessage({ message }) {
  const tr = useTranslation();
  const openChecklist = useContext(SafetyChecklistContext);
  const text = localizeBackendMessage(message, tr);
  if (!openChecklist || !isSafetyCheckMessage(message)) return text;
  return <>
    <span className="safety-message-text">{text}</span>
    <button type="button" className="text-button safety-checklist-link" onClick={openChecklist}>
      {tr("查看检查清单")}
    </button>
  </>;
}
