const STORAGE_KEY = "marquee-bar-collapsed";
const POLL_INTERVAL_MS = 5000;

const CSS = `
:host {
  position: fixed;
  bottom: 8px;
  left: 8px;
  z-index: 2147483000;
  font: 12px/1.2 -apple-system, BlinkMacSystemFont, "Segoe UI", system-ui, sans-serif;
}
:host([position="top"]) {
  bottom: auto;
  top: 8px;
}
[hidden] {
  display: none !important;
}
.wrap {
  display: flex;
  align-items: center;
  gap: 6px;
}
.bar {
  display: flex;
  align-items: center;
  gap: 6px;
  height: 28px;
  padding: 0 8px;
  border-radius: 8px;
  background: #f5f5f4;
  color: #1c1c1c;
  border: 1px solid #d0d0ce;
  box-shadow: 0 1px 4px rgba(0, 0, 0, 0.18);
}
.chip {
  display: inline-flex;
  align-items: center;
  height: 18px;
  padding: 0 7px;
  border-radius: 9px;
  white-space: nowrap;
  background: rgba(0, 0, 0, 0.07);
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
  width: var(--mq-pr-width, 118px);
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
  width: 20px;
  height: 20px;
  padding: 0;
  border: 1px solid #d0d0ce;
  border-radius: 50%;
  background: #f5f5f4;
  color: inherit;
  font: inherit;
  cursor: pointer;
}
.wrap:not(.collapsed) .toggle::before {
  content: "\\2013";
}
.wrap.collapsed .toggle {
  width: 14px;
  height: 14px;
}
.toggle:focus-visible,
a:focus-visible {
  outline: 2px solid #3b82f6;
  outline-offset: 2px;
}
@media (prefers-color-scheme: dark) {
  .bar,
  .toggle {
    background: #262626;
    color: #ededed;
    border-color: #4d4d4d;
  }
  .chip {
    background: rgba(255, 255, 255, 0.12);
  }
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
.switch {
  display: inline-flex;
  align-items: center;
  gap: 4px;
  height: 18px;
  padding: 0 7px;
  border-radius: 9px;
  border: 1px solid transparent;
  background: rgba(0, 0, 0, 0.07);
  color: inherit;
  font: inherit;
  cursor: pointer;
  white-space: nowrap;
}
.switch:focus-visible {
  outline: 2px solid #3b82f6;
  outline-offset: 2px;
}
.menu {
  position: absolute;
  left: 0;
  bottom: calc(100% + 6px);
  min-width: 160px;
  max-height: 240px;
  overflow-y: auto;
  padding: 4px;
  margin: 0;
  border-radius: 8px;
  background: #f5f5f4;
  color: #1c1c1c;
  border: 1px solid #d0d0ce;
  box-shadow: 0 2px 8px rgba(0, 0, 0, 0.25);
}
:host([position="top"]) .menu {
  bottom: auto;
  top: calc(100% + 6px);
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
@media (prefers-color-scheme: dark) {
  .menu {
    background: #262626;
    color: #ededed;
    border-color: #4d4d4d;
  }
  .switch {
    background: rgba(255, 255, 255, 0.12);
  }
  .menu button:hover,
  .menu button:focus-visible {
    background: rgba(255, 255, 255, 0.16);
  }
}
`;

const TEMPLATE = `
<style>${CSS}</style>
<div class="wrap" hidden>
  <div class="bar" role="status" aria-label="Development branch information">
    <span class="chip branch"></span>
    <span class="dirty" hidden><span aria-hidden="true">●</span><span class="visually-hidden">uncommitted changes</span></span>
    <span class="chip worktree" hidden></span>
    <a class="chip pr" hidden target="_blank" rel="noreferrer"><span class="pr-text"></span></a>
    <span class="switcher" hidden>
      <button type="button" class="switch" aria-haspopup="menu" aria-expanded="false" aria-label="Switch worktree">
        <span class="switch-label"></span>
      </button>
      <div class="menu" role="menu" aria-label="Worktrees" hidden></div>
    </span>
  </div>
  <button type="button" class="toggle"></button>
</div>
`;

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

function textSpan(text) {
  const span = document.createElement("span");
  span.textContent = text;
  return span;
}

function safeHttpUrl(value) {
  try {
    const url = new URL(value, location.href);
    return url.protocol === "https:" || url.protocol === "http:" ? url.href : null;
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
  #status = null;
  #statusPolls = 0;
  #timer = 0;
  #warned = false;
  #collapsed = false;
  #token = "";
  #switching = false;
  #menuOpen = false;
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
    this.#toggle.addEventListener("click", () => this.#onToggle());
    this.#switch.addEventListener("click", () => this.#toggleMenu());
    this.#menu.addEventListener("keydown", (event) => this.#onMenuKeydown(event));
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
    if (status.position === "top") {
      this.setAttribute("position", "top");
    } else {
      this.removeAttribute("position");
    }
    const colors = branchColors(status.branch, this.#dark.matches);
    this.#branch.textContent = status.branch;
    this.#branch.style.background = colors.background;
    this.#branch.style.color = colors.text;
    this.#dirty.hidden = !status.dirty;
    const worktree = status.worktree;
    const showWorktree = Boolean(worktree && worktree.slug && worktree.isMain === false);
    this.#worktree.hidden = !showWorktree;
    if (showWorktree) this.#worktree.textContent = worktree.slug;
    const pr = status.pr;
    const prHref = pr && pr.url ? safeHttpUrl(pr.url) : null;
    this.#renderPr(pr, prHref);
    this.#renderSwitcher(status);
    this.#toggle.style.background = this.#collapsed ? colors.background : "";
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
    this.#pr.hidden = true;
    this.#pr.classList.remove("skeleton");
    this.#pr.removeAttribute("aria-hidden");
    this.#pr.removeAttribute("tabindex");
    this.#pr.removeAttribute("href");
    this.#pr.removeAttribute("title");
    this.#prText.textContent = "";
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
    this.#switchLabel.textContent = "Worktrees";
    this.#buildMenu(worktrees, status.worktree);
  }

  #spinnerNode() {
    const span = document.createElement("span");
    span.className = "spinner";
    span.setAttribute("aria-hidden", "true");
    return span;
  }

  #buildMenu(worktrees, current) {
    const currentSlug = current && current.slug ? current.slug : "";
    const items = worktrees.map((worktree) => {
      const item = document.createElement("button");
      item.type = "button";
      item.setAttribute("role", "menuitem");
      const slug = String(worktree.slug || "");
      const branch = String(worktree.branch || "");
      item.dataset.slug = slug;
      const isCurrent = slug === currentSlug;
      if (isCurrent) item.setAttribute("aria-current", "true");
      const text = document.createElement("span");
      text.className = "item-text";
      const primary = document.createElement("span");
      primary.className = "item-branch";
      primary.textContent = (branch || slug) + (isCurrent ? " (current)" : "");
      text.appendChild(primary);
      if (branch && branch !== slug) {
        const secondary = document.createElement("span");
        secondary.className = "item-slug";
        secondary.textContent = slug;
        text.appendChild(secondary);
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
    this.#menu.replaceChildren(...items);
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
    const first = this.#menu.querySelector("button");
    if (first) first.focus();
  }

  #closeMenu(returnFocus) {
    if (!this.#menuOpen) {
      this.#menu.hidden = true;
      return;
    }
    this.#menuOpen = false;
    this.#menu.hidden = true;
    this.#switch.setAttribute("aria-expanded", "false");
    if (returnFocus) this.#switch.focus();
  }

  #onTriggerKeydown(event) {
    if (event.key === "ArrowDown" || event.key === "Enter" || event.key === " ") {
      event.preventDefault();
      this.#openMenu();
    }
  }

  #onMenuKeydown(event) {
    const items = Array.from(this.#menu.querySelectorAll("button"));
    const index = items.indexOf(this.shadowRoot.activeElement);
    if (event.key === "Escape") {
      event.preventDefault();
      this.#closeMenu(true);
    } else if (event.key === "ArrowDown") {
      event.preventDefault();
      const next = items[Math.min(index + 1, items.length - 1)];
      if (next) next.focus();
    } else if (event.key === "ArrowUp") {
      event.preventDefault();
      if (index <= 0) {
        this.#closeMenu(true);
      } else {
        items[index - 1].focus();
      }
    }
  }

  #onOutsidePointer(event) {
    if (this.#menuOpen && !event.composedPath().includes(this)) {
      this.#closeMenu();
    }
  }

  async #switchTo(slug, confirmed) {
    if (this.#token === "" || this.#switching) return;
    this.#switching = true;
    this.#render();
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
      if (response.status === 409) {
        const data = await response.json().catch(() => null);
        if (data && data.error === "dirty" && !confirmed) {
          this.#switching = false;
          if (window.confirm(`This worktree has uncommitted changes. Switch to ${slug} anyway?`)) {
            return this.#switchTo(slug, true);
          }
          this.#render();
          return;
        }
        console.warn("marquee: switch rejected", data);
      } else if (!response.ok) {
        console.warn("marquee: switch failed", response.status);
      }
    } catch (error) {
      console.warn("marquee: switch request failed", error);
    } finally {
      this.#switching = false;
    }
    // Refresh promptly so the bar reflects the new worktree (or recovers the
    // idle switcher if the switch was refused).
    this.#poll();
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
