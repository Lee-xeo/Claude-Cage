import { setMessages, translate } from "../message-catalog.mjs";
import { localizeBackendMessage } from "../backend-message.mjs";

export const mobileLocale = typeof location !== "undefined" && location.pathname === "/" &&
  navigator.userAgent.endsWith(" CageMobile/1 CageLocale/ru") ? "ru" : "zh-CN";

if (mobileLocale === "ru") {
  const [{ default: desktopRussian }, { default: mobileRussian }] = await Promise.all([
    import("../locales/ru.json", { with: { type: "json" } }),
    import("./ru.json", { with: { type: "json" } }),
  ]);
  setMessages({ ...desktopRussian, ...mobileRussian });
  document.documentElement.lang = mobileLocale;
}

export const mobileText = translate;

export function mobileBackendText(message) {
  return mobileLocale === "ru" ? localizeBackendMessage(message, mobileText) : message;
}
