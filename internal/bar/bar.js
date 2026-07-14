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
`;

const TEMPLATE = `
<style>${CSS}</style>
<div class="wrap" hidden>
  <div class="bar" role="status" aria-label="Development branch information">
    <span class="chip branch"></span>
    <span class="dirty" hidden><span aria-hidden="true">●</span><span class="visually-hidden">uncommitted changes</span></span>
    <span class="chip worktree" hidden></span>
    <a class="chip pr" hidden target="_blank" rel="noreferrer"></a>
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
  #toggle;
  #status = null;
  #timer = 0;
  #warned = false;
  #collapsed = false;
  #dark = window.matchMedia("(prefers-color-scheme: dark)");
  #onSchemeChange = () => this.#render();

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
    this.#toggle = this.shadowRoot.querySelector(".toggle");
    this.#toggle.addEventListener("click", () => this.#onToggle());
    try {
      this.#collapsed = sessionStorage.getItem(STORAGE_KEY) === "1";
    } catch {
      this.#collapsed = false;
    }
    this.#applyCollapsed();
  }

  connectedCallback() {
    this.#dark.addEventListener("change", this.#onSchemeChange);
    this.#poll();
    this.#timer = setInterval(() => this.#poll(), POLL_INTERVAL_MS);
  }

  disconnectedCallback() {
    clearInterval(this.#timer);
    this.#dark.removeEventListener("change", this.#onSchemeChange);
  }

  async #poll() {
    try {
      const response = await fetch("/__marquee/status", { cache: "no-store" });
      if (!response.ok) throw new Error(`status ${response.status}`);
      this.#status = await response.json();
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
    const showPr = prHref !== null;
    this.#pr.hidden = !showPr;
    if (showPr) {
      this.#pr.href = prHref;
      this.#pr.textContent = `#${pr.number} ${pr.title}`;
    }
    this.#toggle.style.background = this.#collapsed ? colors.background : "";
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
