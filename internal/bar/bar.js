import { DEFAULTS, PILL_IDS, effectiveCatalog, makeValidators, merge, validate, load, save, reset } from "./prefs.js";
import { PANEL_CSS, createSettingsPanel } from "./settings.js";

const STORAGE_KEY = "marquee-bar-collapsed";
const POLL_INTERVAL_MS = 5000;
// While a switch runs the bar polls status fast to learn the moment the new
// worktree is being served; the timeout is generous because the switch hook can
// run a full bundle/pnpm install before the child even restarts.
const SWITCH_POLL_INTERVAL_MS = 500;
const SWITCH_READY_TIMEOUT_MS = 150000;

const CSS = `
:host {
  position: fixed;
  bottom: 8px;
  left: 8px;
  z-index: 2147483000;
  /* --mq-scale is the single knob the size presets drive; the bar's height,
     font, chip dimensions, padding, and radii all multiply through it, so one
     data-size attribute resizes the whole bar coherently. The 8px corner
     offset above stays fixed so the bar hugs the same corner at every size. */
  --mq-scale: 1;
  /* --mq-bg/-fg/-border/-chip-bg are the bar's chrome palette; every themable
     surface (bar, menu, toggle, switch, settings panel) reads them, so a theme
     is just a new value set. These base :host values are the default theme's
     light look and the dark-scheme override below completes it — together they
     are the fail-open fallback that renders before the first status fetch (or if
     the payload carries no catalog). The curated themes' palettes come from the
     knob catalog in the status payload; #applyThemeStyles emits a per-theme
     :host([data-theme=…]) rule from that data into a separate <style>, so those
     rules win over this fallback with higher specificity and, being un-mediated,
     stay scheme-independent. The branch chip is deliberately excluded — it keeps
     its hash color and its own contrast guarantee. */
  --mq-bg: #f5f5f4;
  --mq-fg: #1c1c1c;
  --mq-border: #d0d0ce;
  --mq-chip-bg: rgba(0, 0, 0, 0.07);
  font: 12px/1.2 -apple-system, BlinkMacSystemFont, "Segoe UI", system-ui, sans-serif;
  font-size: calc(12px * var(--mq-scale, 1));
}
@media (prefers-color-scheme: dark) {
  :host {
    --mq-bg: #262626;
    --mq-fg: #ededed;
    --mq-border: #4d4d4d;
    --mq-chip-bg: rgba(255, 255, 255, 0.12);
  }
}
:host([data-size="small"]) {
  --mq-scale: 0.85;
}
:host([data-size="medium"]) {
  --mq-scale: 1;
}
:host([data-size="large"]) {
  --mq-scale: 1.2;
}
:host([data-position="bottom-left"]) {
  top: auto;
  bottom: 8px;
  left: 8px;
  right: auto;
}
:host([data-position="bottom-right"]) {
  top: auto;
  bottom: 8px;
  left: auto;
  right: 8px;
}
:host([data-position="top-left"]) {
  top: 8px;
  bottom: auto;
  left: 8px;
  right: auto;
}
:host([data-position="top-right"]) {
  top: 8px;
  bottom: auto;
  left: auto;
  right: 8px;
}
[hidden] {
  display: none !important;
}
.wrap {
  position: relative;
  /* The bar rides above the switch scrim (z-index 1 below) so it stays crisp and
     legible in its corner while the rest of the page is dimmed — the bar is what
     tells the user which worktree is active, so a switch must never bury it. */
  z-index: 2;
  display: flex;
  align-items: center;
  gap: calc(6px * var(--mq-scale, 1));
}
.bar {
  display: flex;
  align-items: center;
  gap: calc(6px * var(--mq-scale, 1));
  height: calc(28px * var(--mq-scale, 1));
  padding: 0 calc(8px * var(--mq-scale, 1));
  border-radius: calc(8px * var(--mq-scale, 1));
  background: var(--mq-bg);
  color: var(--mq-fg);
  border: 1px solid var(--mq-border);
  box-shadow: 0 1px 4px rgba(0, 0, 0, 0.18);
}
.chip {
  display: inline-flex;
  align-items: center;
  height: calc(18px * var(--mq-scale, 1));
  padding: 0 calc(7px * var(--mq-scale, 1));
  border-radius: calc(9px * var(--mq-scale, 1));
  white-space: nowrap;
  background: var(--mq-chip-bg);
}
a.chip {
  color: inherit;
  text-decoration: underline;
}
/* The PR slot holds a fixed width whether it shows the resolved PR chip or the
   pending skeleton, so the switcher and toggle to its right never change
   x-position when the async PR poll resolves after first paint. */
.pr {
  box-sizing: border-box;
  width: calc(var(--mq-pr-width, 118px) * var(--mq-scale, 1));
}
.pr-text {
  min-width: 0;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.pr.skeleton {
  background-image: linear-gradient(
    100deg,
    rgba(0, 0, 0, 0) 30%,
    rgba(0, 0, 0, 0.12) 50%,
    rgba(0, 0, 0, 0) 70%
  );
  background-repeat: no-repeat;
  background-size: 200% 100%;
}
@media (prefers-reduced-motion: no-preference) {
  .pr.skeleton {
    animation: marquee-shimmer 1.4s linear infinite;
  }
}
@keyframes marquee-shimmer {
  from {
    background-position: 200% 0;
  }
  to {
    background-position: -200% 0;
  }
}
.toggle {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  width: calc(20px * var(--mq-scale, 1));
  height: calc(20px * var(--mq-scale, 1));
  padding: 0;
  border: 1px solid var(--mq-border);
  border-radius: 50%;
  background: var(--mq-bg);
  color: inherit;
  font: inherit;
  cursor: pointer;
}
.wrap:not(.collapsed) .toggle::before {
  content: "\\2013";
}
.wrap.collapsed .toggle {
  width: calc(14px * var(--mq-scale, 1));
  height: calc(14px * var(--mq-scale, 1));
}
.toggle:focus-visible,
a:focus-visible {
  outline: 2px solid #3b82f6;
  outline-offset: 2px;
}
@media (prefers-color-scheme: dark) {
  .pr.skeleton {
    background-image: linear-gradient(
      100deg,
      rgba(255, 255, 255, 0) 30%,
      rgba(255, 255, 255, 0.18) 50%,
      rgba(255, 255, 255, 0) 70%
    );
  }
}
@media (prefers-reduced-motion: no-preference) {
  .toggle {
    transition: transform 0.15s ease;
  }
  .toggle:hover {
    transform: scale(1.15);
  }
}
.visually-hidden {
  position: absolute;
  width: 1px;
  height: 1px;
  margin: -1px;
  overflow: hidden;
  clip-path: inset(50%);
  white-space: nowrap;
}
.switcher {
  position: relative;
  display: inline-flex;
}
/* The switch and the gear are the bar's two controls; unlike the read-only
   info pills they carry a solid border and a glyph (the ▾ caret here, the ⚙ on
   the gear) so they visibly read as operable buttons rather than status chips. */
.switch {
  display: inline-flex;
  align-items: center;
  gap: calc(4px * var(--mq-scale, 1));
  height: calc(18px * var(--mq-scale, 1));
  padding: 0 calc(7px * var(--mq-scale, 1));
  border-radius: calc(9px * var(--mq-scale, 1));
  border: 1px solid var(--mq-border);
  background: var(--mq-chip-bg);
  color: inherit;
  font: inherit;
  cursor: pointer;
  white-space: nowrap;
}
.switch:focus-visible {
  outline: 2px solid #3b82f6;
  outline-offset: 2px;
}
/* A failed switch tints the control so the failure is visible in the bar
   itself, not only in the console. The accent is a fixed red rather than a
   themed custom property because it signals an exceptional state, and it clears
   the moment the next switch attempt starts. */
.switch.switch-error {
  color: #b91c1c;
  border-color: #b91c1c;
}
@media (prefers-color-scheme: dark) {
  .switch.switch-error {
    color: #fca5a5;
    border-color: #fca5a5;
  }
}
.switch-caret {
  font-size: calc(9px * var(--mq-scale, 1));
  line-height: 1;
  opacity: 0.75;
}
.menu {
  position: absolute;
  left: 0;
  right: auto;
  bottom: calc(100% + 6px);
  top: auto;
  min-width: calc(160px * var(--mq-scale, 1));
  padding: calc(4px * var(--mq-scale, 1));
  margin: 0;
  border-radius: calc(8px * var(--mq-scale, 1));
  background: var(--mq-bg);
  color: var(--mq-fg);
  border: 1px solid var(--mq-border);
  box-shadow: 0 2px 8px rgba(0, 0, 0, 0.25);
}
:host([data-position$="-right"]) .menu {
  left: auto;
  right: 0;
}
:host([data-position^="top-"]) .menu {
  bottom: auto;
  top: calc(100% + 6px);
}
.menu-search {
  display: block;
  width: 100%;
  box-sizing: border-box;
  margin: 0 0 calc(4px * var(--mq-scale, 1));
  padding: 4px 8px;
  border: 1px solid var(--mq-border);
  border-radius: 5px;
  background: transparent;
  color: inherit;
  font: inherit;
}
.menu-search:focus-visible {
  outline: 2px solid #3b82f6;
  outline-offset: -2px;
}
.menu-list {
  max-height: 240px;
  overflow-y: auto;
}
.menu-empty {
  padding: 5px 8px;
  white-space: nowrap;
  opacity: 0.7;
}
.match {
  font-weight: 700;
}
.menu button {
  display: flex;
  align-items: center;
  gap: 6px;
  width: 100%;
  padding: 5px 8px;
  border: none;
  border-radius: 5px;
  background: transparent;
  color: inherit;
  font: inherit;
  text-align: left;
  cursor: pointer;
}
.menu button:hover,
.menu button:focus-visible {
  background: rgba(0, 0, 0, 0.1);
  outline: none;
}
.menu button[aria-current="true"] {
  font-weight: 600;
}
.item-text {
  display: flex;
  flex-direction: column;
  gap: 1px;
  min-width: 0;
}
.item-branch,
.item-slug {
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.item-slug {
  font-size: 11px;
  opacity: 0.7;
}
.switch-label {
  display: inline-flex;
  align-items: center;
  gap: 4px;
}
.spinner {
  flex: none;
  display: inline-block;
  width: 10px;
  height: 10px;
  border: 2px solid currentColor;
  border-right-color: transparent;
  border-radius: 50%;
}
@media (prefers-reduced-motion: no-preference) {
  .spinner {
    animation: marquee-spin 0.7s linear infinite;
  }
}
@keyframes marquee-spin {
  to {
    transform: rotate(360deg);
  }
}
/* The switch overlay is a viewport-filling scrim that covers the whole page
   while a worktree switch runs (hook, restart, health wait). It is a fixed
   child of the shadow root — not of the corner-anchored .wrap — so it spans the
   viewport regardless of where the bar sits, and it swallows every click until
   the switch resolves or fails. Its z-index sits below .wrap (2) so the scrim
   dims and blocks the page while the bar itself stays lifted above it, visible
   and legible throughout the switch. */
.overlay {
  position: fixed;
  inset: 0;
  z-index: 1;
  display: flex;
  align-items: center;
  justify-content: center;
  padding: 16px;
  background: rgba(0, 0, 0, 0.45);
  backdrop-filter: blur(3px);
  -webkit-backdrop-filter: blur(3px);
}
.overlay-card {
  display: flex;
  flex-direction: column;
  align-items: center;
  gap: calc(12px * var(--mq-scale, 1));
  max-width: 80vw;
  padding: calc(20px * var(--mq-scale, 1)) calc(28px * var(--mq-scale, 1));
  border-radius: calc(12px * var(--mq-scale, 1));
  background: var(--mq-bg);
  color: var(--mq-fg);
  border: 1px solid var(--mq-border);
  box-shadow: 0 4px 20px rgba(0, 0, 0, 0.35);
  font-size: calc(13px * var(--mq-scale, 1));
  text-align: center;
}
.overlay-spinner {
  flex: none;
  width: calc(28px * var(--mq-scale, 1));
  height: calc(28px * var(--mq-scale, 1));
  border: 3px solid var(--mq-border);
  border-top-color: #3b82f6;
  border-radius: 50%;
}
/* On readiness the spinner gives way to a settled check so the card reads as a
   deliberate "done, reloading" beat instead of a spinner frozen mid-navigation.
   The check is drawn with borders (no glyph) so it themes via color and never
   risks emoji presentation. */
.overlay-check {
  flex: none;
  position: relative;
  width: calc(28px * var(--mq-scale, 1));
  height: calc(28px * var(--mq-scale, 1));
  border-radius: 50%;
  background: #16a34a;
}
.overlay-check::after {
  content: "";
  position: absolute;
  left: 36%;
  top: 20%;
  width: 22%;
  height: 44%;
  border: solid #fff;
  border-width: 0 calc(2.5px * var(--mq-scale, 1)) calc(2.5px * var(--mq-scale, 1)) 0;
  transform: rotate(45deg);
}
@media (prefers-color-scheme: dark) {
  .overlay-check {
    background: #22c55e;
  }
}
@media (prefers-reduced-motion: no-preference) {
  .overlay-spinner {
    animation: marquee-spin 0.7s linear infinite;
  }
  .overlay {
    animation: marquee-overlay-in 0.2s ease both;
  }
  .overlay-card {
    animation: marquee-card-in 0.2s ease both;
  }
  .overlay-check {
    animation: marquee-pop 0.25s ease both;
  }
}
@keyframes marquee-overlay-in {
  from {
    opacity: 0;
  }
  to {
    opacity: 1;
  }
}
@keyframes marquee-card-in {
  from {
    opacity: 0;
    transform: translateY(calc(4px * var(--mq-scale, 1))) scale(0.98);
  }
  to {
    opacity: 1;
    transform: none;
  }
}
@keyframes marquee-pop {
  from {
    opacity: 0;
    transform: scale(0.6);
  }
  to {
    opacity: 1;
    transform: none;
  }
}
.overlay-text {
  overflow-wrap: anywhere;
}
@media (prefers-color-scheme: dark) {
  .menu button:hover,
  .menu button:focus-visible {
    background: rgba(255, 255, 255, 0.16);
  }
}
`;

const TEMPLATE = `
<style>${CSS}${PANEL_CSS}</style>
<style class="mq-themes"></style>
<div class="wrap" hidden>
  <div class="bar" role="status" aria-label="Development branch information">
    <span class="chip branch"></span>
    <span class="dirty" hidden><span aria-hidden="true">●︎</span><span class="visually-hidden">uncommitted changes</span></span>
    <span class="chip worktree" hidden></span>
    <a class="chip pr" hidden target="_blank" rel="noreferrer"><span class="pr-text"></span></a>
    <span class="switcher" hidden>
      <button type="button" class="switch" aria-haspopup="menu" aria-expanded="false" aria-label="Switch worktree">
        <span class="switch-label"></span>
        <span class="switch-caret" aria-hidden="true">▾︎</span>
      </button>
      <div class="menu" hidden>
        <input class="menu-search" type="text" placeholder="Search worktrees" aria-label="Search worktrees" autocomplete="off" spellcheck="false" />
        <div class="menu-list" role="menu" aria-label="Worktrees"></div>
      </div>
    </span>
  </div>
  <span class="settings">
    <button type="button" class="gear" aria-haspopup="true" aria-expanded="false" aria-controls="marquee-settings-menu" aria-label="Bar settings">⚙︎</button>
    <div class="settings-menu" id="marquee-settings-menu" role="group" aria-label="Bar settings" hidden></div>
  </span>
  <button type="button" class="toggle"></button>
</div>
<div class="overlay" role="alert" aria-live="assertive" hidden>
  <div class="overlay-card">
    <span class="overlay-spinner" aria-hidden="true"></span>
    <span class="overlay-check" aria-hidden="true" hidden></span>
    <span class="overlay-text"></span>
  </div>
</div>
`;

// cssValue gates one palette entry before it reaches the generated <style>: it
// must be a non-empty string free of braces, semicolons, and control
// characters, so a malformed palette can neither emit "undefined" as a custom
// property value nor break out of its declaration block. The values normally
// come verbatim from marquee's own embedded knob catalog, but a stale or
// malformed payload must degrade to the static fallback, never to broken CSS.
function cssValue(value) {
  return typeof value === "string" && value.trim() !== "" && !/[{};\u0000-\u001f]/.test(value);
}

// validPalette accepts a palette only when all four themable custom properties
// carry a usable CSS value; a palette missing any field yields no rule, so the
// static default fallback wins for that theme (fail-open).
function validPalette(palette) {
  return Boolean(palette) && [palette.bg, palette.fg, palette.border, palette.chipBg].every(cssValue);
}

// paletteDecls renders an already-validated catalog palette as the four
// themable custom-property declarations.
function paletteDecls(palette) {
  return `--mq-bg: ${palette.bg}; --mq-fg: ${palette.fg}; --mq-border: ${palette.border}; --mq-chip-bg: ${palette.chipBg};`;
}

// themeRule builds one theme's CSS from its catalog entry: a base
// :host([data-theme=id]) rule from the light palette, plus a dark-scheme media
// override only when the theme carries a valid dark palette (the default theme
// is scheme-aware; curated themes are fixed). The id must be a plain word so it
// cannot escape the attribute selector, and both palettes pass validPalette
// before any rule is emitted — a malformed entry yields an empty string so the
// static default fallback still covers the bar (fail-open).
function themeRule(theme) {
  if (!theme || typeof theme.id !== "string" || !/^[\w-]+$/.test(theme.id) || !validPalette(theme.light)) return "";
  const selector = `:host([data-theme="${theme.id}"])`;
  let rule = `${selector} { ${paletteDecls(theme.light)} }`;
  if (validPalette(theme.dark)) {
    rule += `\n@media (prefers-color-scheme: dark) { ${selector} { ${paletteDecls(theme.dark)} } }`;
  }
  return rule;
}

function fnv1a(text) {
  let hash = 0x811c9dc5;
  for (let i = 0; i < text.length; i++) {
    hash ^= text.charCodeAt(i);
    hash = Math.imul(hash, 0x01000193);
  }
  return hash >>> 0;
}

function hslToRgb(hue, saturation, lightness) {
  const s = saturation / 100;
  const l = lightness / 100;
  const a = s * Math.min(l, 1 - l);
  const f = (n) => {
    const k = (n + hue / 30) % 12;
    return l - a * Math.max(-1, Math.min(k - 3, 9 - k, 1));
  };
  return [f(0), f(8), f(4)];
}

function relativeLuminance([r, g, b]) {
  const lin = (c) => (c <= 0.03928 ? c / 12.92 : ((c + 0.055) / 1.055) ** 2.4);
  return 0.2126 * lin(r) + 0.7152 * lin(g) + 0.0722 * lin(b);
}

// Whichever of black/white contrasts better always clears 4.5:1
// (the worst case is ~4.58:1 at luminance ~0.18).
function branchColors(branch, dark) {
  const hue = fnv1a(branch) % 360;
  const lightness = dark ? 32 : 82;
  const luminance = relativeLuminance(hslToRgb(hue, 60, lightness));
  const blackContrast = (luminance + 0.05) / 0.05;
  const whiteContrast = 1.05 / (luminance + 0.05);
  return {
    background: `hsl(${hue} 60% ${lightness}%)`,
    text: blackContrast >= whiteContrast ? "#000" : "#fff",
  };
}

function delay(ms) {
  return new Promise((resolve) => setTimeout(resolve, ms));
}

function textSpan(text) {
  const span = document.createElement("span");
  span.textContent = text;
  return span;
}

// fuzzyScore rates query against candidate as a case-insensitive subsequence,
// matched greedily left to right. A non-match returns null; a match returns
// the matched character positions for highlighting plus a score where
// consecutive runs and word boundaries (start of string, or after "/", "-",
// "_") earn bonuses, with an earlier first match as the fractional tiebreak.
function fuzzyScore(query, candidate) {
  const q = query.toLowerCase();
  const c = candidate.toLowerCase();
  const positions = [];
  let score = 0;
  let from = 0;
  for (const char of q) {
    const at = c.indexOf(char, from);
    if (at === -1) return null;
    if (positions.length > 0 && at === positions[positions.length - 1] + 1) score += 2;
    if (at === 0 || "/-_".includes(c[at - 1])) score += 3;
    score += 1;
    positions.push(at);
    from = at + 1;
  }
  return { score: score - positions[0] / (c.length + 1), positions };
}

// highlightedSpan renders text with the characters at the given positions
// bolded, composing per-character spans assigned via textContent only.
function highlightedSpan(className, text, positions) {
  const container = document.createElement("span");
  container.className = className;
  if (positions.length === 0) {
    container.textContent = text;
    return container;
  }
  const matched = new Set(positions);
  for (let i = 0; i < text.length; i++) {
    const span = textSpan(text[i]);
    if (matched.has(i)) span.className = "match";
    container.appendChild(span);
  }
  return container;
}

function safeHttpUrl(value) {
  try {
    const url = new URL(value, location.href);
    return url.protocol === "https:" || url.protocol === "http:" ? url.href : null;
  } catch {
    return null;
  }
}

// localStore returns the per-origin preference store, or null when even reading
// window.localStorage throws (some sandboxed contexts do). prefs.js treats a
// null store as absent, so settings persistence fails open to the CLI defaults.
function localStore() {
  try {
    return window.localStorage;
  } catch {
    return null;
  }
}

class MarqueeBar extends HTMLElement {
  #wrap;
  #bar;
  #branch;
  #dirty;
  #worktree;
  #pr;
  #prText;
  #toggle;
  #switcher;
  #switch;
  #switchLabel;
  #menu;
  #menuSearch;
  #menuList;
  #gear;
  #settingsMenu;
  #overlay;
  #overlaySpinner;
  #overlayCheck;
  #overlayText;
  #themeStyle;
  #themeKey = "";
  #settings;
  #storedPrefs = {};
  #status = null;
  #statusPolls = 0;
  #timer = 0;
  #warned = false;
  #collapsed = false;
  #token = "";
  #switching = false;
  #switchError = "";
  #menuOpen = false;
  #searchQuery = "";
  #dark = window.matchMedia("(prefers-color-scheme: dark)");
  #onSchemeChange = () => this.#render();
  #onDocPointerDown = (event) => this.#onOutsidePointer(event);

  constructor() {
    super();
    this.attachShadow({ mode: "open" });
    this.shadowRoot.innerHTML = TEMPLATE;
    this.#wrap = this.shadowRoot.querySelector(".wrap");
    this.#bar = this.shadowRoot.querySelector(".bar");
    this.#branch = this.shadowRoot.querySelector(".branch");
    this.#dirty = this.shadowRoot.querySelector(".dirty");
    this.#worktree = this.shadowRoot.querySelector(".worktree");
    this.#pr = this.shadowRoot.querySelector(".pr");
    this.#prText = this.shadowRoot.querySelector(".pr-text");
    this.#toggle = this.shadowRoot.querySelector(".toggle");
    this.#switcher = this.shadowRoot.querySelector(".switcher");
    this.#switch = this.shadowRoot.querySelector(".switch");
    this.#switchLabel = this.shadowRoot.querySelector(".switch-label");
    this.#menu = this.shadowRoot.querySelector(".menu");
    this.#menuSearch = this.shadowRoot.querySelector(".menu-search");
    this.#menuList = this.shadowRoot.querySelector(".menu-list");
    this.#gear = this.shadowRoot.querySelector(".gear");
    this.#settingsMenu = this.shadowRoot.querySelector(".settings-menu");
    this.#overlay = this.shadowRoot.querySelector(".overlay");
    this.#overlaySpinner = this.shadowRoot.querySelector(".overlay-spinner");
    this.#overlayCheck = this.shadowRoot.querySelector(".overlay-check");
    this.#overlayText = this.shadowRoot.querySelector(".overlay-text");
    this.#themeStyle = this.shadowRoot.querySelector(".mq-themes");
    this.#storedPrefs = load(localStore());
    this.#settings = createSettingsPanel({
      button: this.#gear,
      panel: this.#settingsMenu,
      host: this,
      getEffective: () => this.#effective(),
      getCatalog: () => effectiveCatalog(this.#status || {}),
      onPosition: (position) => this.#applyPref({ position }),
      onSize: (size) => this.#applyPref({ size }),
      onTheme: (theme) => this.#applyPref({ theme }),
      onPills: (pills) => this.#applyPref({ pills }),
      onReset: () => this.#resetPrefs(),
    });
    this.#toggle.addEventListener("click", () => this.#onToggle());
    this.#switch.addEventListener("click", () => this.#toggleMenu());
    this.#menu.addEventListener("keydown", (event) => this.#onMenuKeydown(event));
    this.#menuSearch.addEventListener("input", () => {
      this.#searchQuery = this.#menuSearch.value;
      this.#rebuildMenu();
    });
    this.#switch.addEventListener("keydown", (event) => this.#onTriggerKeydown(event));
    // The token is delivered only through the injected element attribute on a
    // same-origin page; it is never read from any network response.
    this.#token = this.getAttribute("token") || "";
    try {
      this.#collapsed = sessionStorage.getItem(STORAGE_KEY) === "1";
    } catch {
      this.#collapsed = false;
    }
    this.#applyCollapsed();
  }

  connectedCallback() {
    this.#dark.addEventListener("change", this.#onSchemeChange);
    document.addEventListener("pointerdown", this.#onDocPointerDown);
    this.#poll();
    this.#timer = setInterval(() => this.#poll(), POLL_INTERVAL_MS);
  }

  disconnectedCallback() {
    clearInterval(this.#timer);
    this.#dark.removeEventListener("change", this.#onSchemeChange);
    document.removeEventListener("pointerdown", this.#onDocPointerDown);
  }

  async #poll() {
    try {
      const response = await fetch("/__marquee/status", { cache: "no-store" });
      if (!response.ok) throw new Error(`status ${response.status}`);
      this.#status = await response.json();
      this.#statusPolls++;
      this.#warned = false;
      this.#render();
    } catch (error) {
      this.#status = null;
      this.#render();
      if (!this.#warned) {
        this.#warned = true;
        console.warn("marquee: status unavailable, hiding bar", error);
      }
    }
  }

  #render() {
    const status = this.#status;
    if (!status || typeof status.branch !== "string" || status.branch === "") {
      this.#wrap.hidden = true;
      return;
    }
    this.#wrap.hidden = false;
    // The theme palettes ride the status payload; build the per-theme CSS from
    // them before applying data-theme so the effective theme's custom properties
    // are in place the moment the attribute selects them.
    this.#applyThemeStyles();
    // Stored panel prefs overlay the CLI/status defaults; a missing or invalid
    // value at either layer falls back, so the bar is never left unanchored or
    // unscaled.
    const effective = this.#effective();
    this.setAttribute("data-position", effective.position);
    this.setAttribute("data-size", effective.size);
    this.setAttribute("data-theme", effective.theme);
    this.#settings.sync();
    // A pill shows only if it is both in the effective order AND its own
    // condition says show; the two rules compose so hiding a pill via the list
    // never overrides its self-hide (worktree on main, pr absent, dirty clean).
    const shown = new Set(effective.pills);
    const colors = branchColors(status.branch, this.#dark.matches);
    this.#branch.hidden = !shown.has("branch");
    this.#branch.textContent = status.branch;
    this.#branch.style.background = colors.background;
    this.#branch.style.color = colors.text;
    this.#dirty.hidden = !(shown.has("dirty") && status.dirty);
    const worktree = status.worktree;
    const showWorktree = shown.has("worktree") && Boolean(worktree && worktree.slug && worktree.isMain === false);
    this.#worktree.hidden = !showWorktree;
    if (showWorktree) this.#worktree.textContent = worktree.slug;
    if (shown.has("pr")) {
      const pr = status.pr;
      const prHref = pr && pr.url ? safeHttpUrl(pr.url) : null;
      this.#renderPr(pr, prHref);
    } else {
      this.#hidePr();
    }
    this.#orderPills(effective.pills);
    this.#renderSwitcher(status);
    this.#toggle.style.background = this.#collapsed ? colors.background : "";
  }

  // #applyThemeStyles builds each theme's CSS custom-property set from the knob
  // catalog in the status payload and writes it into the dedicated <style>. It
  // rebuilds only when the theme data changes, and a missing or malformed
  // catalog leaves the static default fallback untouched — fail-open, so the bar
  // never loses its palette.
  #applyThemeStyles() {
    const themes = this.#status && this.#status.catalog ? this.#status.catalog.themes : null;
    const choices = themes && Array.isArray(themes.choices) ? themes.choices : null;
    if (!choices) return;
    const key = JSON.stringify(choices);
    if (key === this.#themeKey) return;
    this.#themeKey = key;
    this.#themeStyle.textContent = choices.map(themeRule).join("\n");
  }

  // #effective layers the stored panel prefs over the CLI/status defaults,
  // validating both against the value lists derived from the payload catalog (or
  // the built-in fallback when the payload has none). Every layer fails open: a
  // bad value anywhere yields a valid effective pref.
  #effective() {
    const status = this.#status || {};
    const validators = makeValidators(effectiveCatalog(status));
    const defaults = merge(
      DEFAULTS,
      {
        position: status.position,
        size: status.size,
        theme: status.theme,
        pills: status.pills,
      },
      validators,
    );
    return merge(defaults, this.#storedPrefs, validators);
  }

  // #applyPref sanitizes and persists a panel change, then re-renders so the
  // overlay takes effect immediately and survives reload.
  #applyPref(patch) {
    const validators = makeValidators(effectiveCatalog(this.#status || {}));
    this.#storedPrefs = validate({ ...this.#storedPrefs, ...patch }, DEFAULTS, validators);
    save(localStore(), this.#storedPrefs);
    this.#render();
  }

  // #resetPrefs clears the stored overlay, returning every knob to the CLI
  // default, and forgets it from storage.
  #resetPrefs() {
    this.#storedPrefs = {};
    reset(localStore());
    this.#render();
  }

  // #renderPr keeps the PR slot occupying a fixed width so the switcher and
  // toggle never shift when the async PR poll resolves. The slot has three
  // states: a shimmer skeleton while the PR is still unknown (the first
  // successful status can't tell "GitHub not yet queried" from "no PR"), the
  // resolved PR chip, or — once a later poll still reports none — collapsed.
  #renderPr(pr, prHref) {
    const state = this.#prSlotState(prHref);
    if (state === "present") {
      this.#pr.hidden = false;
      this.#pr.classList.remove("skeleton");
      this.#pr.removeAttribute("aria-hidden");
      this.#pr.removeAttribute("tabindex");
      this.#pr.href = prHref;
      const label = `#${pr.number} ${pr.title}`;
      this.#prText.textContent = label;
      this.#pr.title = label;
      return;
    }
    if (state === "unknown") {
      this.#pr.hidden = false;
      this.#pr.classList.add("skeleton");
      this.#pr.setAttribute("aria-hidden", "true");
      this.#pr.setAttribute("tabindex", "-1");
      this.#pr.removeAttribute("href");
      this.#pr.removeAttribute("title");
      this.#prText.textContent = "";
      return;
    }
    this.#hidePr();
  }

  // #hidePr collapses the PR slot and clears every state it can carry, so a PR
  // that is genuinely absent — or a pill list that omits "pr" — leaves no
  // reserved width, skeleton, or stale link behind.
  #hidePr() {
    this.#pr.hidden = true;
    this.#pr.classList.remove("skeleton");
    this.#pr.removeAttribute("aria-hidden");
    this.#pr.removeAttribute("tabindex");
    this.#pr.removeAttribute("href");
    this.#pr.removeAttribute("title");
    this.#prText.textContent = "";
  }

  // #orderPills lays the four pill elements out in the effective order within
  // .bar, ahead of the fixed controls (switcher, gear, collapse toggle). Ids
  // absent from the list trail the ordered ones; they are already hidden by
  // #render, so their position is inert but kept deterministic.
  #orderPills(order) {
    const els = {
      branch: this.#branch,
      dirty: this.#dirty,
      worktree: this.#worktree,
      pr: this.#pr,
    };
    const full = [...order, ...PILL_IDS.filter((id) => !order.includes(id))];
    for (const id of full) {
      const el = els[id];
      if (el) this.#bar.insertBefore(el, this.#switcher);
    }
  }

  #prSlotState(prHref) {
    if (prHref !== null) return "present";
    // No PR in this payload. On the first successful status the PR poll may
    // still be resolving, so reserve the slot; if a later poll still reports
    // none, treat it as genuinely absent and collapse.
    return this.#statusPolls <= 1 ? "unknown" : "absent";
  }

  // #renderSwitcher shows the worktree dropdown only when there is a token
  // (so switching is actually available) and more than one worktree to choose
  // between. All worktree text is written via textContent, never innerHTML.
  #renderSwitcher(status) {
    const worktrees = Array.isArray(status.worktrees) ? status.worktrees : [];
    const canSwitch = this.#token !== "" && worktrees.length > 1;
    this.#switcher.hidden = !canSwitch;
    if (!canSwitch) {
      this.#closeMenu();
      return;
    }
    if (this.#switching) {
      this.#switch.setAttribute("aria-busy", "true");
      this.#switch.disabled = true;
      this.#switchLabel.replaceChildren(this.#spinnerNode(), textSpan("Switching…"));
      return;
    }
    this.#switch.removeAttribute("aria-busy");
    this.#switch.disabled = false;
    // A failed switch surfaces its message on the still-operable control so the
    // user sees it in the bar and can retry; the accent and text clear when the
    // next attempt begins. A healthy switcher just reads "Worktrees".
    if (this.#switchError) {
      this.#switch.classList.add("switch-error");
      this.#switch.title = this.#switchError;
      this.#switchLabel.textContent = this.#switchError;
    } else {
      this.#switch.classList.remove("switch-error");
      this.#switch.removeAttribute("title");
      this.#switchLabel.textContent = "Worktrees";
    }
    this.#buildMenu(worktrees, status.worktree);
  }

  #spinnerNode() {
    const span = document.createElement("span");
    span.className = "spinner";
    span.setAttribute("aria-hidden", "true");
    return span;
  }

  #rebuildMenu() {
    const status = this.#status || {};
    const worktrees = Array.isArray(status.worktrees) ? status.worktrees : [];
    this.#buildMenu(worktrees, status.worktree);
  }

  // #buildMenu renders the worktree list into the menu, filtered and ranked by
  // the search query when one is set. An empty query keeps git order; a
  // non-empty query keeps only fuzzy matches, best score first (the stable
  // sort preserves git order between equal scores).
  #buildMenu(worktrees, current) {
    const currentSlug = current && current.slug ? current.slug : "";
    const query = this.#searchQuery.trim();
    const entries = [];
    for (const worktree of worktrees) {
      const slug = String(worktree.slug || "");
      const branch = String(worktree.branch || "");
      let branchMatch = null;
      let slugMatch = null;
      if (query !== "") {
        branchMatch = branch === "" ? null : fuzzyScore(query, branch);
        slugMatch = slug === "" ? null : fuzzyScore(query, slug);
        if (!branchMatch && !slugMatch) continue;
      }
      const score = Math.max(branchMatch ? branchMatch.score : -Infinity, slugMatch ? slugMatch.score : -Infinity);
      entries.push({ slug, branch, branchMatch, slugMatch, score });
    }
    if (query !== "") entries.sort((a, b) => b.score - a.score);
    if (entries.length === 0) {
      const empty = document.createElement("div");
      empty.className = "menu-empty";
      empty.setAttribute("role", "menuitem");
      empty.setAttribute("aria-disabled", "true");
      empty.textContent = "No matches";
      this.#menuList.replaceChildren(empty);
      return;
    }
    const items = entries.map(({ slug, branch, branchMatch, slugMatch }) => {
      const item = document.createElement("button");
      item.type = "button";
      item.setAttribute("role", "menuitem");
      item.dataset.slug = slug;
      const isCurrent = slug === currentSlug;
      if (isCurrent) item.setAttribute("aria-current", "true");
      const text = document.createElement("span");
      text.className = "item-text";
      const primaryMatch = branch !== "" ? branchMatch : slugMatch;
      const primary = highlightedSpan("item-branch", branch || slug, primaryMatch ? primaryMatch.positions : []);
      if (isCurrent) primary.appendChild(textSpan(" (current)"));
      text.appendChild(primary);
      if (branch && branch !== slug) {
        text.appendChild(highlightedSpan("item-slug", slug, slugMatch ? slugMatch.positions : []));
      }
      item.replaceChildren(text);
      const description = branch ? `Branch ${branch}, worktree ${slug}` : `Worktree ${slug}`;
      item.setAttribute("aria-label", isCurrent ? `${description}, current` : description);
      item.addEventListener("click", () => {
        this.#closeMenu();
        if (!isCurrent) this.#switchTo(slug, false);
      });
      return item;
    });
    this.#menuList.replaceChildren(...items);
  }

  #toggleMenu() {
    if (this.#menuOpen) {
      this.#closeMenu();
    } else {
      this.#openMenu();
    }
  }

  #openMenu() {
    if (this.#switching || this.#switcher.hidden) return;
    this.#menuOpen = true;
    this.#menu.hidden = false;
    this.#switch.setAttribute("aria-expanded", "true");
    this.#menuSearch.focus();
  }

  #closeMenu(returnFocus) {
    if (!this.#menuOpen) {
      this.#menu.hidden = true;
      return;
    }
    this.#menuOpen = false;
    this.#menu.hidden = true;
    this.#switch.setAttribute("aria-expanded", "false");
    // The query dies with the menu, so every open starts fresh with the full
    // list instead of a stale filter.
    if (this.#searchQuery !== "") {
      this.#searchQuery = "";
      this.#menuSearch.value = "";
      this.#rebuildMenu();
    }
    if (returnFocus) this.#switch.focus();
  }

  #onTriggerKeydown(event) {
    if (event.key === "ArrowDown" || event.key === "Enter" || event.key === " ") {
      event.preventDefault();
      this.#openMenu();
    }
  }

  // #onMenuKeydown wires the input and the result list into one keyboard
  // flow: ArrowDown from the input enters the first result, ArrowUp from the
  // first result returns to the input, Enter in the input activates the top
  // result through the same click path as the pointer, Escape closes.
  #onMenuKeydown(event) {
    const active = this.shadowRoot.activeElement;
    const items = Array.from(this.#menuList.querySelectorAll("button"));
    if (event.key === "Escape") {
      event.preventDefault();
      this.#closeMenu(true);
      return;
    }
    if (active === this.#menuSearch) {
      if (event.key === "ArrowDown") {
        event.preventDefault();
        if (items[0]) items[0].focus();
      } else if (event.key === "Enter") {
        event.preventDefault();
        if (items[0]) items[0].click();
      }
      return;
    }
    const index = items.indexOf(active);
    if (event.key === "ArrowDown") {
      event.preventDefault();
      const next = items[Math.min(index + 1, items.length - 1)];
      if (next) next.focus();
    } else if (event.key === "ArrowUp") {
      event.preventDefault();
      if (index <= 0) {
        this.#menuSearch.focus();
      } else {
        items[index - 1].focus();
      }
    }
  }

  #onOutsidePointer(event) {
    if (this.#menuOpen && !event.composedPath().includes(this)) {
      this.#closeMenu();
    }
    this.#settings.onOutsidePointer(event);
  }

  async #switchTo(slug, confirmed) {
    if (this.#token === "" || this.#switching) return;
    this.#switching = true;
    // A fresh attempt clears any error left by the previous one, so the bar
    // shows the live busy state instead of a stale "Switch failed".
    this.#switchError = "";
    // The overlay goes up the instant the switch starts and stays up through the
    // whole hook/restart/health window, so the user sees a deliberate loading
    // state instead of a frozen page that later "sort of reloads".
    this.#showOverlay(this.#worktreeLabel(slug));
    this.#render();
    // The dirty-worktree confirm is deferred until AFTER this try/finally
    // unwinds, never recursed into from inside it. Returning #switchTo(…, true)
    // from within the try would run this finally — flipping #switching back to
    // false — while the confirmed retry is still in flight, so the next 5s poll
    // repainted the idle switcher and the long confirmed switch looked like it
    // did nothing. Deciding here and retrying below keeps the confirmed switch
    // a clean, fresh call with its own busy lifecycle.
    let needsConfirm = false;
    try {
      const body = confirmed ? { slug, confirm: true } : { slug };
      const response = await fetch("/__marquee/switch", {
        method: "POST",
        cache: "no-store",
        headers: {
          "Content-Type": "application/json",
          "X-Marquee-Token": this.#token,
        },
        body: JSON.stringify(body),
      });
      if (response.ok) {
        // The switch endpoint returns once marquee has restarted and
        // health-probed the child, but the status poller may still be
        // repointing to the new worktree. Hold the overlay and poll status until
        // it reports the target worktree running, so the reload lands on a warm
        // server instead of hanging while it boots. The overlay stays up across
        // the reload's unload; return before the finally so nothing repaints.
        if (await this.#waitForWorktreeReady(slug)) {
          this.#markOverlayReady();
          location.reload();
          return;
        }
        this.#switchError = "Switch failed";
        console.warn("marquee: switch did not become ready before the timeout");
      } else if (response.status === 409) {
        const data = await response.json().catch(() => null);
        if (data && data.error === "dirty" && !confirmed) {
          needsConfirm = true;
        } else {
          this.#switchError = "Switch rejected";
          console.warn("marquee: switch rejected", data);
        }
      } else {
        this.#switchError = "Switch failed";
        console.warn("marquee: switch failed", response.status);
      }
    } catch (error) {
      this.#switchError = "Switch failed";
      console.warn("marquee: switch request failed", error);
    } finally {
      this.#switching = false;
    }
    // Every path that reaches here is not reloading — the switch was refused,
    // failed, timed out, or is waiting on the dirty confirm — so tear the
    // overlay down. Fail-open: the overlay must never leave the page blocked.
    this.#hideOverlay();
    if (needsConfirm && window.confirm(`This worktree has uncommitted changes. Switch to ${slug} anyway?`)) {
      // A confirmed retry is a brand-new switch: it re-enters with #switching
      // false, shows its own busy state, and reloads on success — identical to
      // a switch from a clean worktree.
      this.#switchTo(slug, true);
      return;
    }
    // Refresh promptly so the bar recovers the idle switcher when the switch was
    // refused, or shows the failure accent that #switchError set above so the
    // user can retry.
    this.#poll();
  }

  // #waitForWorktreeReady polls status fast until it reports the target
  // worktree running, or gives up at the timeout. Readiness is two signals from
  // the status payload: the poller has repointed to the target slug (only
  // happens after a fully successful switch) AND the child process is running,
  // so the reload never lands on a half-restarted server. Transient fetch
  // failures during the child restart are expected and simply retried.
  async #waitForWorktreeReady(slug) {
    const deadline = Date.now() + SWITCH_READY_TIMEOUT_MS;
    while (Date.now() < deadline) {
      try {
        const response = await fetch("/__marquee/status", {
          cache: "no-store",
          headers: { "X-Marquee-Token": this.#token },
        });
        if (response.ok && this.#worktreeReady(await response.json(), slug)) {
          return true;
        }
      } catch {
        // The child is mid-restart; keep polling until the deadline.
      }
      await delay(SWITCH_POLL_INTERVAL_MS);
    }
    return false;
  }

  #worktreeReady(status, slug) {
    return Boolean(
      status &&
        status.worktree &&
        status.worktree.slug === slug &&
        status.child &&
        status.child.state === "running",
    );
  }

  // #worktreeLabel prefers the target's branch name for the overlay text and
  // falls back to the slug, so the user reads "Switching to <branch>…".
  #worktreeLabel(slug) {
    const worktrees =
      this.#status && Array.isArray(this.#status.worktrees) ? this.#status.worktrees : [];
    const match = worktrees.find((worktree) => worktree && worktree.slug === slug);
    return match && match.branch ? match.branch : slug;
  }

  #showOverlay(label) {
    // A fresh switch always opens in the busy state: spinner up, check down, so
    // a card reused after an earlier ready→reload beat never flashes stale.
    this.#overlaySpinner.hidden = false;
    this.#overlayCheck.hidden = true;
    this.#setOverlayText(`Switching to ${label}…`);
    this.#overlay.hidden = false;
  }

  // #markOverlayReady settles the card the instant readiness is confirmed and
  // the reload is fired: the spinner gives way to a check and the copy reads
  // "Ready — reloading…", so the last frame before navigation is a deliberate
  // done state rather than a spinner stuck mid-reload.
  #markOverlayReady() {
    this.#overlaySpinner.hidden = true;
    this.#overlayCheck.hidden = false;
    this.#setOverlayText("Ready — reloading…");
  }

  #setOverlayText(text) {
    this.#overlayText.textContent = text;
  }

  #hideOverlay() {
    this.#overlay.hidden = true;
  }

  #onToggle() {
    this.#collapsed = !this.#collapsed;
    try {
      sessionStorage.setItem(STORAGE_KEY, this.#collapsed ? "1" : "0");
    } catch {
      // sessionStorage can be unavailable; the toggle still works for this page
    }
    this.#applyCollapsed();
    this.#render();
  }

  #applyCollapsed() {
    this.#bar.hidden = this.#collapsed;
    this.#wrap.classList.toggle("collapsed", this.#collapsed);
    this.#toggle.setAttribute("aria-expanded", String(!this.#collapsed));
    this.#toggle.setAttribute(
      "aria-label",
      this.#collapsed ? "Expand branch bar" : "Collapse branch bar",
    );
  }
}

if (window.self === window.top && !customElements.get("marquee-bar")) {
  customElements.define("marquee-bar", MarqueeBar);
}
