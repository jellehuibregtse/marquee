// settings.js is the ⚙ popover: it builds the panel DOM, wires the controls,
// and talks back to bar.js only through callbacks. It owns no status or network
// concerns — bar.js hands it the current effective prefs plus apply/reset
// callbacks, so the panel stays a pure reflection of state driven by sync().
//
// Accessibility is acceptance criteria: the gear is a real button, the panel is
// a keyboard-operable disclosure (Escape closes and returns focus to the gear,
// outside-pointer closes), position is a real radiogroup of native radios (so
// arrow-key selection works for free), focus is visible, and the entrance
// animation is disabled under prefers-reduced-motion.

import { POSITIONS, SIZES, THEMES } from "./prefs.js";

const POSITION_LABELS = {
  "bottom-left": "Bottom left",
  "bottom-right": "Bottom right",
  "top-left": "Top left",
  "top-right": "Top right",
};

// Size is a toggle-button group rather than radios: three short, equal choices
// read best as a compact S/M/L segmented control. Each button carries the full
// word as its accessible name while showing only the initial, and aria-pressed
// marks the active preset so the state is exposed without native radio roles.
const SIZE_LABELS = {
  small: "Small",
  medium: "Medium",
  large: "Large",
};

const SIZE_ABBR = {
  small: "S",
  medium: "M",
  large: "L",
};

// Theme is a native <select>: the curated set is a small, mutually exclusive
// list where the option text names each theme, and a real select gives keyboard
// and screen-reader support for free plus a visible current value.
const THEME_LABELS = {
  default: "Default",
  midnight: "Midnight",
  sand: "Sand",
  forest: "Forest",
};

// PANEL_CSS is concatenated into bar.js's single shadow-root <style>. The panel
// reuses the corner-aware anchoring from the worktree menu so it flips its
// horizontal edge and vertical direction per corner and never clips off-screen.
export const PANEL_CSS = `
.settings {
  position: relative;
  display: inline-flex;
}
.gear {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  width: 20px;
  height: 20px;
  padding: 0;
  border: 1px solid var(--mq-border);
  border-radius: 50%;
  background: var(--mq-bg);
  color: inherit;
  font: inherit;
  line-height: 1;
  cursor: pointer;
}
.gear:focus-visible {
  outline: 2px solid #3b82f6;
  outline-offset: 2px;
}
.wrap.collapsed .settings {
  display: none;
}
.settings-menu {
  position: absolute;
  left: 0;
  right: auto;
  bottom: calc(100% + 6px);
  top: auto;
  min-width: 180px;
  padding: 8px;
  margin: 0;
  border-radius: 8px;
  background: var(--mq-bg);
  color: var(--mq-fg);
  border: 1px solid var(--mq-border);
  box-shadow: 0 2px 8px rgba(0, 0, 0, 0.25);
}
:host([data-position$="-right"]) .settings-menu {
  left: auto;
  right: 0;
}
:host([data-position^="top-"]) .settings-menu {
  bottom: auto;
  top: calc(100% + 6px);
}
.settings-section {
  display: flex;
  flex-direction: column;
  gap: 4px;
}
.settings-label {
  font-weight: 600;
}
.settings-radios {
  display: grid;
  grid-template-columns: 1fr 1fr;
  gap: 2px 8px;
}
.settings-radio {
  display: flex;
  align-items: center;
  gap: 6px;
  padding: 3px 4px;
  border-radius: 5px;
  cursor: pointer;
}
.settings-radio:hover {
  background: rgba(0, 0, 0, 0.08);
}
.settings-radio input {
  margin: 0;
}
.settings-radio input:focus-visible {
  outline: 2px solid #3b82f6;
  outline-offset: 2px;
}
.settings-sizes {
  display: flex;
  gap: 4px;
}
.settings-size {
  flex: 1;
  padding: 4px 0;
  border: 1px solid var(--mq-border);
  border-radius: 5px;
  background: transparent;
  color: inherit;
  font: inherit;
  line-height: 1;
  cursor: pointer;
}
.settings-size:hover {
  background: rgba(0, 0, 0, 0.08);
}
.settings-size:focus-visible {
  outline: 2px solid #3b82f6;
  outline-offset: 2px;
}
.settings-size[aria-pressed="true"] {
  background: #3b82f6;
  border-color: #3b82f6;
  color: #fff;
  font-weight: 600;
}
.settings-theme {
  width: 100%;
  padding: 4px 6px;
  border: 1px solid var(--mq-border);
  border-radius: 5px;
  background: var(--mq-bg);
  color: var(--mq-fg);
  font: inherit;
  cursor: pointer;
}
.settings-theme:focus-visible {
  outline: 2px solid #3b82f6;
  outline-offset: 2px;
}
.settings-reset {
  margin-top: 8px;
  width: 100%;
  padding: 5px 8px;
  border: 1px solid var(--mq-border);
  border-radius: 5px;
  background: transparent;
  color: inherit;
  font: inherit;
  cursor: pointer;
}
.settings-reset:hover,
.settings-reset:focus-visible {
  background: rgba(0, 0, 0, 0.1);
  outline: none;
}
.settings-reset:focus-visible {
  outline: 2px solid #3b82f6;
  outline-offset: 2px;
}
@media (prefers-reduced-motion: no-preference) {
  .settings-menu:not([hidden]) {
    animation: marquee-panel-in 0.12s ease;
  }
}
@keyframes marquee-panel-in {
  from {
    opacity: 0;
    transform: translateY(4px);
  }
  to {
    opacity: 1;
    transform: none;
  }
}
@media (prefers-color-scheme: dark) {
  .settings-radio:hover,
  .settings-size:hover {
    background: rgba(255, 255, 255, 0.14);
  }
  .settings-reset:hover,
  .settings-reset:focus-visible {
    background: rgba(255, 255, 255, 0.16);
  }
}
`;

// createSettingsPanel wires the gear button and the (empty) panel container that
// bar.js supplies in its template. It returns { sync, onOutsidePointer }:
// bar.js calls sync() after each render so the checked radio reflects the
// effective prefs, and forwards its document pointerdown to onOutsidePointer so
// the panel closes on an outside click without bar.js knowing the panel's DOM.
export function createSettingsPanel({ button, panel, host, getEffective, onPosition, onSize, onTheme, onReset }) {
  let open = false;

  const radios = new Map();
  const section = document.createElement("div");
  section.className = "settings-section";
  const label = document.createElement("span");
  label.className = "settings-label";
  label.id = "marquee-settings-position-label";
  label.textContent = "Position";
  const group = document.createElement("div");
  group.className = "settings-radios";
  group.setAttribute("role", "radiogroup");
  group.setAttribute("aria-labelledby", label.id);
  for (const position of POSITIONS) {
    const radioLabel = document.createElement("label");
    radioLabel.className = "settings-radio";
    const input = document.createElement("input");
    input.type = "radio";
    input.name = "marquee-position";
    input.value = position;
    const text = document.createElement("span");
    text.textContent = POSITION_LABELS[position] || position;
    radioLabel.append(input, text);
    group.appendChild(radioLabel);
    radios.set(position, input);
  }
  section.append(label, group);

  const sizeButtons = new Map();
  const sizeSection = document.createElement("div");
  sizeSection.className = "settings-section";
  const sizeLabel = document.createElement("span");
  sizeLabel.className = "settings-label";
  sizeLabel.id = "marquee-settings-size-label";
  sizeLabel.textContent = "Size";
  const sizeGroup = document.createElement("div");
  sizeGroup.className = "settings-sizes";
  sizeGroup.setAttribute("role", "group");
  sizeGroup.setAttribute("aria-labelledby", sizeLabel.id);
  for (const size of SIZES) {
    const sizeButton = document.createElement("button");
    sizeButton.type = "button";
    sizeButton.className = "settings-size";
    sizeButton.dataset.size = size;
    sizeButton.textContent = SIZE_ABBR[size] || size;
    sizeButton.setAttribute("aria-label", SIZE_LABELS[size] || size);
    sizeButton.setAttribute("aria-pressed", "false");
    sizeButton.addEventListener("click", () => onSize(size));
    sizeGroup.appendChild(sizeButton);
    sizeButtons.set(size, sizeButton);
  }
  sizeSection.append(sizeLabel, sizeGroup);

  const themeSection = document.createElement("div");
  themeSection.className = "settings-section";
  const themeLabel = document.createElement("label");
  themeLabel.className = "settings-label";
  themeLabel.textContent = "Theme";
  const themeSelect = document.createElement("select");
  themeSelect.className = "settings-theme";
  themeSelect.id = "marquee-settings-theme";
  themeLabel.htmlFor = themeSelect.id;
  for (const theme of THEMES) {
    const option = document.createElement("option");
    option.value = theme;
    option.textContent = THEME_LABELS[theme] || theme;
    themeSelect.appendChild(option);
  }
  themeSelect.addEventListener("change", () => onTheme(themeSelect.value));
  themeSection.append(themeLabel, themeSelect);

  const resetButton = document.createElement("button");
  resetButton.type = "button";
  resetButton.className = "settings-reset";
  resetButton.textContent = "Reset";

  panel.replaceChildren(section, sizeSection, themeSection, resetButton);

  function sync() {
    const effective = getEffective();
    for (const [position, input] of radios) {
      input.checked = position === effective.position;
    }
    for (const [size, sizeButton] of sizeButtons) {
      sizeButton.setAttribute("aria-pressed", String(size === effective.size));
    }
    themeSelect.value = effective.theme;
  }

  function focusSelected() {
    for (const input of radios.values()) {
      if (input.checked) {
        input.focus();
        return;
      }
    }
    const first = radios.values().next().value;
    if (first) first.focus();
  }

  function openPanel() {
    if (open) return;
    open = true;
    sync();
    panel.hidden = false;
    button.setAttribute("aria-expanded", "true");
    focusSelected();
  }

  function closePanel(returnFocus) {
    if (!open) {
      panel.hidden = true;
      return;
    }
    open = false;
    panel.hidden = true;
    button.setAttribute("aria-expanded", "false");
    if (returnFocus) button.focus();
  }

  button.addEventListener("click", () => {
    if (open) {
      closePanel(false);
    } else {
      openPanel();
    }
  });

  panel.addEventListener("keydown", (event) => {
    if (event.key === "Escape") {
      event.preventDefault();
      closePanel(true);
    }
  });

  group.addEventListener("change", (event) => {
    const input = event.target;
    if (input && input.name === "marquee-position" && input.checked) {
      onPosition(input.value);
    }
  });

  resetButton.addEventListener("click", () => {
    onReset();
    sync();
  });

  return {
    sync,
    onOutsidePointer(event) {
      if (open && !event.composedPath().includes(host)) {
        closePanel(false);
      }
    },
  };
}
