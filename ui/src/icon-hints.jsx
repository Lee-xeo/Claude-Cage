import { useEffect } from "react";
import "./icon-hints.css";

// One delegated hint covers both workbench surfaces, including controls rendered later.
export function IconHints() {
  useEffect(() => {
    const hint = document.createElement("div");
    hint.className = "icon-hint";
    hint.setAttribute("role", "tooltip");
    hint.hidden = true;
    document.body.append(hint);
    let current = null;
    let savedTitle = null;
    let temporaryLabel = false;
    let hovering = null;
    let focused = null;
    let touchTimer;
    let longPressed = null;

    function candidate(node) {
      const element = node instanceof Element ? node.closest('button, a[href], [role="button"], [role="img"][aria-label], svg[aria-label]') : null;
      if (!element || element.closest(".icon-hint")) return null;
      const isImage = element.matches('[role="img"], svg');
      if (isImage && element.closest('button, a[href], [role="button"]')) return null;
      if (!isImage) {
        const clone = element.cloneNode(true);
        clone.querySelectorAll('svg, img, [aria-hidden="true"], .sr-only, .visually-hidden').forEach(child => child.remove());
        if (clone.textContent.trim()) return null;
        if (!element.querySelector('svg, img, [aria-hidden="true"]')) return null;
      }
      const label = element.getAttribute("title") || element.getAttribute("aria-label") || element.querySelector("img[alt]")?.getAttribute("alt");
      return label?.trim() ? element : null;
    }

    function hide() {
      if (current && savedTitle !== null && !current.hasAttribute("title")) current.setAttribute("title", savedTitle);
      if (current && temporaryLabel && current.getAttribute("aria-label") === savedTitle) current.removeAttribute("aria-label");
      current = null;
      savedTitle = null;
      temporaryLabel = false;
      hint.hidden = true;
    }
    function show(element) {
      if (!element?.isConnected) { hide(); return; }
      if (current === element) return;
      hide();
      current = element;
      savedTitle = element.getAttribute("title");
      hint.textContent = savedTitle || element.getAttribute("aria-label") || element.querySelector("img[alt]")?.getAttribute("alt");
      if (savedTitle !== null) {
        if (!element.hasAttribute("aria-label")) { element.setAttribute("aria-label", savedTitle); temporaryLabel = true; }
        element.removeAttribute("title");
      }
      (element.closest("dialog[open]") || document.body).append(hint);
      hint.hidden = false;
      const box = element.getBoundingClientRect();
      const tip = hint.getBoundingClientRect();
      const left = Math.max(8, Math.min(window.innerWidth - tip.width - 8, box.left + (box.width - tip.width) / 2));
      const top = box.bottom + 8 + tip.height <= window.innerHeight ? box.bottom + 8 : Math.max(8, box.top - tip.height - 8);
      hint.style.left = `${left}px`;
      hint.style.top = `${top}px`;
    }
    function over(event) {
      if (event.pointerType === "touch") return;
      hovering = candidate(event.target);
      if (hovering) show(hovering);
    }
    function out(event) {
      if (event.pointerType === "touch" || !hovering || hovering.contains(event.relatedTarget)) return;
      hovering = null;
      if (focused) show(focused); else hide();
    }
    function focus(event) { focused = candidate(event.target); if (focused) show(focused); }
    function blur(event) { if (focused === event.target) { focused = null; if (hovering) show(hovering); else hide(); } }
    function down(event) {
      if (event.pointerType !== "touch") return;
      clearTimeout(touchTimer);
      longPressed = null;
      hide();
      const element = candidate(event.target);
      if (element) touchTimer = setTimeout(() => { longPressed = element; show(element); }, 500);
    }
    function up() { clearTimeout(touchTimer); }
    function move(event) { if (event.pointerType === "touch") clearTimeout(touchTimer); }
    function click(event) {
      if (longPressed?.contains(event.target)) { event.preventDefault(); event.stopImmediatePropagation(); }
      longPressed = null;
    }
    function contextMenu(event) { if (longPressed?.contains(event.target)) event.preventDefault(); }
    function key(event) { if (event.key === "Escape") { hovering = null; focused = null; hide(); } }
    function dismiss() { clearTimeout(touchTimer); hide(); }
    document.addEventListener("pointerover", over, true);
    document.addEventListener("pointerout", out, true);
    document.addEventListener("focusin", focus, true);
    document.addEventListener("focusout", blur, true);
    document.addEventListener("pointerdown", down, true);
    document.addEventListener("pointerup", up, true);
    document.addEventListener("pointermove", move, true);
    document.addEventListener("pointercancel", dismiss, true);
    document.addEventListener("keydown", key, true);
    document.addEventListener("click", click, true);
    document.addEventListener("contextmenu", contextMenu, true);
    document.addEventListener("scroll", dismiss, true);
    window.addEventListener("resize", dismiss);
    return () => {
      dismiss();
      document.removeEventListener("pointerover", over, true);
      document.removeEventListener("pointerout", out, true);
      document.removeEventListener("focusin", focus, true);
      document.removeEventListener("focusout", blur, true);
      document.removeEventListener("pointerdown", down, true);
      document.removeEventListener("pointerup", up, true);
      document.removeEventListener("pointermove", move, true);
      document.removeEventListener("pointercancel", dismiss, true);
      document.removeEventListener("keydown", key, true);
      document.removeEventListener("click", click, true);
      document.removeEventListener("contextmenu", contextMenu, true);
      document.removeEventListener("scroll", dismiss, true);
      window.removeEventListener("resize", dismiss);
      hint.remove();
    };
  }, []);
  return null;
}
