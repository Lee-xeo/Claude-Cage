import { InlineSelect } from "./inline-select";
import React, { useEffect, useRef, useState } from "react";
import {
  Check,
  ChevronDown,
  Download,
  LoaderCircle,
  Monitor,
  Moon,
  Sun,
  Upload,
  RotateCcw,
} from "lucide-react";
import { api, native, nativeAvailable } from "./api";
import { useAppearance } from "./appearance";
import { isLinux } from "./platform.mjs";
import {
  colorGroups,
  colorTokens,
  terminalColorGroups,
  terminalTheme,
  customThemeLimit,
  themeFileLimit,
  parseThemeDocument,
  themeDocument,
  validateThemeName,
  sameTheme,
  presets,
  presetNames,
  presetInfo,
  presetGroups,
  themeCatalog,
  applyThemePreset,
  systemUIFont,
  systemCodeFont,
  themeVariables,
} from "./appearance-state.mjs";
import { useTranslation } from "./interface-language.mjs";

const codeFontSuggestions = ["SF Mono", "Menlo", "Monaco", "JetBrains Mono"];
const linuxCodeFontSuggestions = [
  "Sarasa Mono SC", "Noto Sans Mono CJK SC", "Noto Sans Mono",
  "JetBrains Mono", "Ubuntu Mono", "DejaVu Sans Mono",
];

async function installedLinuxCodeFonts() {
  // A local-only source fails for a missing face, even when CSS can render a fallback.
  const results = await Promise.all(linuxCodeFontSuggestions.map(async (name) => {
    try {
      await new FontFace(name, `local("${name}")`).load();
      return name;
    } catch {
      return null;
    }
  }));
  return results.filter(Boolean);
}

function SettingRow({ label, description, children }) {
  return (
    <div className="appearance-row">
      <div>
        <span>{label}</span>
        {description && <p>{description}</p>}
      </div>
      <div className="appearance-control">{children}</div>
    </div>
  );
}
function FontSizeInput({ label, value, min, onChange }) {
  const [text, setText] = useState(String(value));
  useEffect(() => setText(String(value)), [value]);
  return (
    <div className="font-size-control">
      <input
        aria-label={label}
        type="number"
        min={min}
        max="24"
        value={text}
        onChange={(event) => {
          setText(event.target.value);
          const number = Number(event.target.value);
          if (Number.isInteger(number) && number >= min && number <= 24)
            onChange(number);
        }}
        onBlur={() => setText(String(value))}
      />
      <span>px</span>
    </div>
  );
}
function ColorField({ theme, name, value, onChange, onReset, compact = false }) {
  const tr = useTranslation();
  const [text, setText] = useState(value);
  useEffect(() => setText(value), [value]);
  const invalid = !/^#[0-9a-fA-F]{6}$/.test(text);
  const commit = () => {
    if (!invalid && text !== value) onChange(text.toLowerCase());
  };
  if (compact) {
    return (
      <div className="theme-swatch-item">
        <label className="theme-swatch-chip" title={`${name} ${value}`}>
          <input
            type="color"
            aria-label={tr("选取{0}的{1}", theme, name)}
            value={value}
            onChange={(e) => onChange(e.target.value)}
          />
          <span>{name}</span>
        </label>
        <input
          type="text"
          className="theme-swatch-hex"
          aria-label={tr("{0}的{1}", theme, name)}
          aria-invalid={invalid}
          value={text}
          maxLength={7}
          spellCheck={false}
          onChange={(e) => setText(e.target.value)}
          onBlur={commit}
          onKeyDown={(e) => {
            if (e.key === "Enter") {
              e.preventDefault();
              commit();
            }
            if (e.key === "Escape") {
              e.stopPropagation();
              setText(value);
            }
          }}
        />
        {invalid && (
          <small className="theme-color-error">{tr("请输入 # 加六位十六进制颜色")}</small>
        )}
      </div>
    );
  }
  return (
    <div className="theme-color-field">
      <span>{name}</span>
      <div className="theme-color-value">
        <input
          type="color"
          aria-label={tr("选取{0}的{1}", theme, name)}
          value={value}
          onChange={(e) => onChange(e.target.value)}
        />
        <input
          type="text"
          aria-label={tr("{0}的{1}", theme, name)}
          aria-invalid={invalid}
          value={text}
          maxLength={7}
          spellCheck={false}
          onChange={(e) => setText(e.target.value)}
          onBlur={commit}
          onKeyDown={(e) => {
            if (e.key === "Enter") {
              e.preventDefault();
              commit();
            }
            if (e.key === "Escape") {
              e.stopPropagation();
              setText(value);
            }
          }}
        />
        {onReset && (
          <button
            className="icon-button"
            aria-label={tr("将{0}的{1}恢复为自动颜色", theme, name)}
            title={tr("恢复自动颜色")}
            onClick={onReset}
          >
            <RotateCcw size={13} />
          </button>
        )}
      </div>
      {invalid && (
        <small className="theme-color-error">{tr("请输入 # 加六位十六进制颜色")}</small>
      )}
    </div>
  );
}
function ThemeCard({ variant, value, active, onChange, codeFonts }) {
  const tr = useTranslation();
  const [advanced, setAdvanced] = useState(false);
  const label = variant === "light" ? tr("浅色主题") : tr("深色主题");
  const options = presets[variant];
  const patch = (delta) => onChange({ ...value, preset: "custom", ...delta });
  const vars = themeVariables(value, variant);
  const terminal = terminalTheme(value, variant);
  const presetDesc = presetInfo[value.preset]?.description
    ? tr(presetInfo[value.preset].description)
    : tr("自定义配色，可另存为主题以便下次使用。");
  const setColor = (field, key, color) => {
    const next = { ...value[field] };
    if (color === undefined) delete next[key];
    else next[key] = color;
    patch({ [field]: next });
  };
  return (
    <section
      className={`appearance-theme-card ${active ? "current" : ""}`}
      aria-label={label}
    >
      <div
        className="theme-miniature"
        aria-hidden="true"
        title={presetDesc}
        style={{ ...vars, background: value.surface, color: value.ink }}
      >
        <div className="mini-sidebar" style={{ background: vars["--sidebar"] }}>
          <div className="mini-status-dots">
            <em style={{ background: vars["--danger"] }} />
            <em style={{ background: vars["--warning"] }} />
            <em style={{ background: vars["--success"] }} />
          </div>
          <i />
          <i />
          <i />
        </div>
        <div className="mini-content">
          {active && <span className="theme-active-badge">{tr("当前使用")}</span>}
          <span className="mini-bar mini-bar--title" style={{ background: vars["--strong"] }} />
          <span className="mini-bar mini-bar--sub" />
          <div
            className="mini-input"
            style={{ borderColor: vars["--line"], background: vars["--input"] }}
          >
            <i />
            <b style={{ background: value.accent }} />
          </div>
        </div>
      </div>
      <div className="theme-card-heading" title={presetDesc}>
        <strong>{label}</strong>
        <InlineSelect
          aria-label={tr("{0}预设", label)}
          value={value.preset}
          onValueChange={(next) =>
            onChange(applyThemePreset(value, variant, next))
          }
        >
          {presetGroups.map((group) => (
            <optgroup key={group} label={tr(group)}>
              {themeCatalog
                .filter((item) => item.group === group && options[item.id])
                .map((item) => (
                  <option key={item.id} value={item.id}>
                    {tr(item.name)}
                  </option>
                ))}
            </optgroup>
          ))}
          {!options[value.preset] && (
            <option value={value.preset} disabled>
              {presetNames[value.preset] ? tr(presetNames[value.preset]) : tr("自定义")}
            </option>
          )}
        </InlineSelect>
      </div>
      <div className="theme-swatch-bar">
        {[
          ["accent", tr("强调色")],
          ["surface", tr("背景")],
          ["ink", tr("文字")],
        ].map(([key, name]) => (
          <ColorField
            key={key}
            compact
            theme={label}
            name={name}
            value={value[key]}
            onChange={(color) => patch({ [key]: color })}
          />
        ))}
      </div>
      <button
        className="theme-customize"
        onClick={() => setAdvanced(!advanced)}
        aria-expanded={advanced}
      >
        {tr("自定义")}
        <ChevronDown size={14} className={advanced ? "rotated" : ""} />
      </button>
      {advanced && (
        <div className="theme-options">
          <p className="theme-options-hint">
            {tr("未单独设置的颜色随基础配色自动调整。")}
          </p>
          {colorGroups.map((group) => (
            <details className="theme-color-group" key={group.label}>
              <summary>{tr(group.label)}</summary>
              {group.entries.map(([key, name]) => (
                <ColorField
                  key={key}
                  theme={label}
                  name={tr(name)}
                  value={vars[colorTokens[key]]}
                  onChange={(color) => setColor("colors", key, color)}
                  onReset={
                    value.colors?.[key]
                      ? () => setColor("colors", key)
                      : undefined
                  }
                />
              ))}
            </details>
          ))}
          {terminalColorGroups.map((group) => (
            <details className="theme-color-group" key={group.label}>
              <summary>{tr(group.label)}</summary>
              {group.entries.map(([key, name]) => (
                <ColorField
                  key={key}
                  theme={label}
                  name={tr("终端{0}", tr(name))}
                  value={terminal[key]}
                  onChange={(color) => setColor("terminal", key, color)}
                  onReset={
                    value.terminal?.[key]
                      ? () => setColor("terminal", key)
                      : undefined
                  }
                />
              ))}
            </details>
          ))}
          <div
            className="theme-terminal-preview"
            style={{
              background: terminal.background,
              color: terminal.foreground,
            }}
            aria-label={tr("{0}终端预览", label)}
          >
            <span style={{ color: terminal.green }}>~/cage</span> ${" "}
            <span style={{ color: terminal.blue }}>echo</span> hello
            <i style={{ background: terminal.cursor }} />
          </div>
          <button
            className="theme-reset-colors"
            disabled={
              !Object.keys(value.colors || {}).length &&
              !Object.keys(value.terminal || {}).length
            }
            onClick={() => patch({ colors: {}, terminal: {} })}
          >
            <RotateCcw size={13} />
            {tr("恢复全部自动颜色")}
          </button>
          <label>
            {tr("对比度") + " "}<output>{value.contrast}</output>
            <input
              aria-label={tr("{0}对比度", label)}
              type="range"
              min="0"
              max="100"
              value={value.contrast}
              onChange={(e) => patch({ contrast: Number(e.target.value) })}
            />
          </label>
          <label>
            {tr("界面字体")}
            <InlineSelect editable
              aria-label={tr("{0}界面字体", label)}
              value={value.fonts.ui}
              placeholder={tr("系统默认")}
              onValueChange={(next) =>
                patch({ fonts: { ...value.fonts, ui: next } })
              }
            >
              <option value="system-ui" />
              <option value="PingFang SC" />
              <option value="Helvetica Neue" />
              <option value="Arial" />
            </InlineSelect>
          </label>
          <label>
            {tr("代码字体")}
            <InlineSelect editable
              aria-label={tr("{0}代码字体", label)}
              value={value.fonts.code}
              placeholder={tr("系统等宽字体")}
              onValueChange={(next) =>
                patch({ fonts: { ...value.fonts, code: next } })
              }
            >
              {codeFonts.map((name) => <option key={name} value={name} />)}
            </InlineSelect>
          </label>
          <label className="appearance-switch">
            <span>{tr("半透明侧栏")}</span>
            <input
              type="checkbox"
              role="switch"
              aria-label={tr("{0}半透明侧栏", label)}
              checked={!value.opaqueWindows}
              onChange={(e) => patch({ opaqueWindows: !e.target.checked })}
            />
          </label>
        </div>
      )}
    </section>
  );
}
function ThemeLibrary({ appearance, update, saving, onError }) {
  const tr = useTranslation();
  const themes = appearance.customThemes;
  const matching = themes.find((item) => sameTheme(item, appearance));
  const [selectedId, setSelectedId] = useState(matching?.id || "");
  const [name, setName] = useState(matching?.name || "");
  const [busy, setBusy] = useState(false);
  const [notice, setNotice] = useState("");
  const input = useRef(null);
  const selected = themes.find((item) => item.id === selectedId);
  const current = selected || matching;
  const dirty = current && !sameTheme(current, appearance);
  const run = async (task) => {
    setBusy(true);
    setNotice("");
    onError("");
    try {
      await task();
    } catch (e) {
      onError(e.message);
    } finally {
      setBusy(false);
    }
  };
  const apply = (item) => {
    setSelectedId(item.id);
    setName(item.name);
    setNotice("");
    onError("");
    void update((previous) => ({
      ...previous,
      lightTheme: structuredClone(item.lightTheme),
      darkTheme: structuredClone(item.darkTheme),
    }));
  };
  const save = (replace) =>
    run(async () => {
      const title = validateThemeName(name);
      if (!replace && themes.length >= customThemeLimit)
        throw new Error(tr("最多保存 50 个自定义主题，请先删除不需要的主题"));
      const document = themeDocument(title, appearance);
      const item = {
        id: replace ? current.id : crypto.randomUUID(),
        name: title,
        lightTheme: document.lightTheme,
        darkTheme: document.darkTheme,
      };
      if (
        await update((previous) => ({
          ...previous,
          customThemes: replace
            ? previous.customThemes.map((old) =>
                old.id === item.id ? item : old,
              )
            : [...previous.customThemes, item],
        }))
      ) {
        setSelectedId(item.id);
        setName(title);
        setNotice("主题已保存");
      }
    });
  const importText = async (text) => {
    const document = parseThemeDocument(text);
    if (themes.length >= customThemeLimit)
      throw new Error(tr("最多保存 50 个自定义主题，请先删除不需要的主题"));
    const item = {
      id: crypto.randomUUID(),
      name: document.name,
      lightTheme: document.lightTheme,
      darkTheme: document.darkTheme,
    };
    if (
      await update((previous) => ({
        ...previous,
        lightTheme: item.lightTheme,
        darkTheme: item.darkTheme,
        customThemes: [...previous.customThemes, item],
      }))
    ) {
      setSelectedId(item.id);
      setName(item.name);
      setNotice("主题已导入并应用");
    }
  };
  const importFile = () => {
    if (!nativeAvailable()) {
      input.current.click();
      return;
    }
    void run(async () => {
      const text = await native("importTheme");
      if (text) await importText(text);
    });
  };
  const exportFile = () =>
    run(async () => {
      const title = name.trim() || matching?.name || tr("我的主题");
      const text =
        JSON.stringify(themeDocument(title, appearance), null, 2) + "\n";
      const filename =
        title
          .replace(/[<>:"/\\|?*\x00-\x1f]/g, "_")
          .replace(/[. ]+$/, "")
          .slice(0, 80) + ".cage-theme.json";
      if (nativeAvailable()) {
        if (await native("exportTheme", { text, filename }))
          setNotice("主题已导出");
      } else {
        const url = URL.createObjectURL(
          new Blob([text], { type: "application/json" }),
        );
        const link = document.createElement("a");
        link.href = url;
        link.download = filename;
        document.body.append(link);
        link.click();
        link.remove();
        setTimeout(() => URL.revokeObjectURL(url), 1000);
        setNotice("已开始下载主题文件");
      }
    });
  return (
    <section className="theme-library" aria-label={tr("我的主题")}>
      <div className="theme-library-heading">
        <h4>{tr("我的主题")}</h4>
        <div className="theme-file-actions">
          <button disabled={busy || saving} onClick={importFile}>
            <Upload size={14} />
            {tr("导入主题")}
          </button>
          <button disabled={busy} onClick={exportFile}>
            <Download size={14} />
            {tr("导出主题")}
          </button>
        </div>
      </div>
      <input
        ref={input}
        type="file"
        accept=".json,application/json"
        hidden
        aria-label={tr("选择主题文件")}
        onChange={(e) => {
          const file = e.target.files?.[0];
          e.target.value = "";
          if (file)
            void run(async () => {
              if (file.size > themeFileLimit)
                throw new Error(tr("主题文件不能超过 256 KB"));
              await importText(await file.text());
            });
        }}
      />
      <div className="theme-library-fields">
        <InlineSelect
          aria-label={tr("已保存的主题")}
          value={current?.id || ""}
          disabled={busy || saving}
          onValueChange={(next) => {
            const item = themes.find((theme) => theme.id === next);
            if (item) apply(item);
          }}
        >
          <option value="" disabled>
            {themes.length ? tr("选择已保存的主题") : tr("尚无自定义主题")}
          </option>
          {themes.map((item) => (
            <option key={item.id} value={item.id}>
              {item.name}
              {item.id === current?.id && dirty ? " · " + tr("已修改") : ""}
            </option>
          ))}
        </InlineSelect>
        <input
          aria-label={tr("主题名称")}
          placeholder={tr("为当前配色命名")}
          value={name}
          maxLength={80}
          onChange={(e) => setName(e.target.value)}
        />
        <button
          disabled={busy || saving || !name.trim()}
          onClick={() => save(false)}
        >
          {tr("另存为主题")}
        </button>
      </div>
      {current && (
        <div className="theme-library-manage">
          <span>
            {dirty ? tr("当前配色有未存入主题的修改") : tr("当前配色与已保存主题一致")}
          </span>
          {dirty && (
            <button disabled={busy || saving} onClick={() => apply(current)}>
              {tr("恢复已保存配色")}
            </button>
          )}
          <button
            disabled={busy || saving || !name.trim()}
            onClick={() => save(true)}
          >
            {tr("更新此主题")}
          </button>
          <button
            disabled={busy || saving}
            onClick={() =>
              run(async () => {
                if (!confirm(tr("删除主题“{0}”？当前配色会保留。", current.name)))
                  return;
                if (
                  await update((previous) => ({
                    ...previous,
                    customThemes: previous.customThemes.filter(
                      (item) => item.id !== current.id,
                    ),
                  }))
                ) {
                  setSelectedId("");
                  setName("");
                  setNotice("主题已删除，当前配色已保留");
                }
              })
            }
          >
            {tr("删除")}
          </button>
        </div>
      )}
      {notice && (
        <p className="theme-library-notice" role="status">
          {tr(notice)}
        </p>
      )}
    </section>
  );
}
export function AppearanceSettings() {
  const tr = useTranslation();
  const { appearance, theme, variant, loading, saving, error, update, retry } =
    useAppearance();
  const [codeFonts, setCodeFonts] = useState(isLinux ? [] : codeFontSuggestions);
  useEffect(() => {
    if (isLinux) void installedLinuxCodeFonts().then(setCodeFonts);
  }, []);
  const [syncing, setSyncing] = useState(false),
    [syncError, setSyncError] = useState(""),
    [synced, setSynced] = useState(false),
    [themeError, setThemeError] = useState("");
  const patch = (delta) => update((previous) => ({ ...previous, ...delta }));
  const sync = async () => {
    setSyncing(true);
    setSyncError("");
    setSynced(false);
    try {
      const value = await api("/appearance/codex");
      if (
        await update((previous) => ({
          ...value,
          customThemes: previous.customThemes,
          syncGuestTheme: previous.syncGuestTheme,
        }))
      )
        setSynced(true);
    } catch (e) {
      setSyncError(e.message);
    } finally {
      setSyncing(false);
    }
  };
  return (
    <div className="appearance-settings">
      <div className="section-title appearance-title-row">
        <h3>{tr("外观")}</h3>
        <div className="appearance-save-state" role="status">
          {saving ? (
            <>
              <LoaderCircle size={13} className="spin" />
              {tr("正在保存…")}
            </>
          ) : error ? (
            <>
              <span>{tr("未保存")}</span>
              <button className="text-button" onClick={retry}>
                {tr("重试保存")}
              </button>
            </>
          ) : (
            <>
              <Check size={13} />
              {synced ? tr("已同步 Codex 外观") : tr("更改自动保存，即时生效")}
            </>
          )}
        </div>
      </div>
      <fieldset disabled={loading || syncing} className="appearance-fields">
        <div className="settings-card">
          <SettingRow label={tr("主题")}>
            <div className="appearance-mode" role="group" aria-label={tr("主题模式")}>
              {[
                ["light", "浅色", Sun],
                ["dark", "深色", Moon],
                ["system", "跟随系统", Monitor],
              ].map(([mode, label, Icon]) => (
                <button
                  key={mode}
                  aria-pressed={appearance.mode === mode}
                  onClick={() => patch({ mode })}
                >
                  <Icon size={15} />
                  {tr(label)}
                </button>
              ))}
            </div>
          </SettingRow>
          <SettingRow
            label={tr("同步环境内部主题")}
            description={tr("将环境内的浏览器、编辑器、桌面应用与 Claude Code 同步为相同的浅色或深色主题。")}
          >
            <label className="appearance-switch">
              <input
                type="checkbox"
                role="switch"
                aria-label={tr("同步环境内部主题")}
                checked={appearance.syncGuestTheme}
                onChange={(e) => patch({ syncGuestTheme: e.target.checked })}
              />
            </label>
          </SettingRow>
        </div>
        <div className="theme-card-grid">
          {["light", "dark"].map((key) => (
            <ThemeCard
              key={key}
              variant={key}
              codeFonts={codeFonts}
              value={appearance[key + "Theme"]}
              active={variant === key}
              onChange={(value) => patch({ [key + "Theme"]: value })}
            />
          ))}
        </div>
        <ThemeLibrary
          key={String(loading)}
          appearance={appearance}
          update={update}
          saving={saving}
          onError={setThemeError}
        />
        {themeError && (
          <div className="error-message" role="alert">
            {tr(themeError)}
          </div>
        )}
        <h4>{tr("字体")}</h4>
        <div className="settings-card">
          <SettingRow label={tr("界面字号")}>
            <FontSizeInput
              label={tr("界面字号")}
              value={appearance.uiFontSize}
              min={12}
              onChange={(value) => patch({ uiFontSize: value })}
            />
          </SettingRow>
          <SettingRow label={tr("代码字号")}>
            <FontSizeInput
              label={tr("代码字号")}
              value={appearance.codeFontSize}
              min={10}
              onChange={(value) => patch({ codeFontSize: value })}
            />
          </SettingRow>
          <div
            className="appearance-sample"
            style={{ fontFamily: theme.fonts.ui || systemUIFont }}
          >
            <span>{tr("在熟悉的工作区，专注下一步。")}</span>
            <code
              style={{
                fontFamily: theme.fonts.code || systemCodeFont,
                fontSize: appearance.codeFontSize,
              }}
            >
              const workspace = "Cage";
            </code>
          </div>
        </div>
        <h4>{tr("显示与交互")}</h4>
        <div className="settings-card">
          <SettingRow label={tr("使用指针光标")}>
            <label className="appearance-switch">
              <input
                type="checkbox"
                role="switch"
                aria-label={tr("使用指针光标")}
                checked={appearance.pointerCursors}
                onChange={(e) => patch({ pointerCursors: e.target.checked })}
              />
            </label>
          </SettingRow>
          <SettingRow label={tr("字体平滑")}>
            <label className="appearance-switch">
              <input
                type="checkbox"
                role="switch"
                aria-label={tr("字体平滑")}
                checked={appearance.fontSmoothing}
                onChange={(e) => patch({ fontSmoothing: e.target.checked })}
              />
            </label>
          </SettingRow>
          <SettingRow label={tr("减少动态效果")}>
            <InlineSelect
              aria-label={tr("减少动态效果")}
              value={appearance.reducedMotion}
              onValueChange={(next) => patch({ reducedMotion: next })}
            >
              <option value="system">{tr("跟随系统")}</option>
              <option value="on">{tr("开启")}</option>
              <option value="off">{tr("关闭")}</option>
            </InlineSelect>
          </SettingRow>
          <div className="appearance-row appearance-import">
            <div>
              <span>{tr("沿用 Codex 的外观")}</span>
            </div>
            <div className="appearance-control">
              <button onClick={sync}>
                {syncing ? (
                  <LoaderCircle size={15} className="spin" />
                ) : (
                  <Download size={15} />
                )}
                {tr("从本机 Codex 同步")}
              </button>
            </div>
          </div>
        </div>
      </fieldset>
      {(error || syncError) && (
        <div className="error-message" role="alert">
          {syncError || error}
        </div>
      )}
    </div>
  );
}
