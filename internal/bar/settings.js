// settings.js is the ⚙ popover: it builds the panel DOM, wires the controls,
// and talks back to bar.js only through callbacks. It owns no status or network
// concerns — bar.js hands it the current effective prefs, the live knob catalog
// (getCatalog), plus apply/reset callbacks, so the panel stays a pure reflection
// of state driven by sync(). The catalog supplies every value list and label, so
// the panel derives its choices from the status payload (via bar.js) rather than
// hardcoding them — a knob added to the Go catalog surfaces here with no edit.
//
// Accessibility is acceptance criteria: the gear is a real button, the panel is
// a keyboard-operable disclosure (Escape closes and returns focus to the gear,
// outside-pointer closes), position is a real radiogroup of native radios (so
// arrow-key selection works for free), focus is visible, and the entrance
// animation is disabled under prefers-reduced-motion.

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
  display: flex;
  flex-direction: column;
  gap: calc(10px * var(--mq-scale, 1));
  min-width: calc(220px * var(--mq-scale, 1));
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
  white-space: nowrap;
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
.settings-pills {
  display: flex;
  flex-direction: column;
  gap: 2px;
}
.settings-pill {
  display: flex;
  align-items: center;
  gap: 6px;
  padding: 3px 4px;
  border-radius: 5px;
}
.settings-pill:hover {
  background: rgba(0, 0, 0, 0.08);
}
.settings-pill-label {
  flex: 1;
  display: flex;
  align-items: center;
  gap: 6px;
  white-space: nowrap;
  cursor: pointer;
}
.settings-pill-label input {
  margin: 0;
}
.settings-pill-move {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  width: 20px;
  height: 20px;
  padding: 0;
  border: 1px solid var(--mq-border);
  border-radius: 5px;
  background: transparent;
  color: inherit;
  font: inherit;
  line-height: 1;
  cursor: pointer;
}
.settings-pill-move:disabled {
  opacity: 0.4;
  cursor: default;
}
.settings-pill-label input:focus-visible,
.settings-pill-move:focus-visible {
  outline: 2px solid #3b82f6;
  outline-offset: 2px;
}
.settings-reset {
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
  .settings-size:hover,
  .settings-pill:hover {
    background: rgba(255, 255, 255, 0.14);
  }
  .settings-reset:hover,
  .settings-reset:focus-visible {
    background: rgba(255, 255, 255, 0.16);
  }
}
`;

// choiceKey fingerprints an ordered {id, label} list so a section rebuilds only
// when the catalog's ids or labels actually change — a background status poll
// re-rendering the bar never tears down controls the user is interacting with.
function choiceKey(choices) {
  return choices.map((c) => c.id + "=" + c.label).join(";");
}

// createSettingsPanel wires the gear button and the (empty) panel container that
// bar.js supplies in its template. It returns { sync, onOutsidePointer }:
// bar.js calls sync() after each render so each control reflects the effective
// prefs and the live catalog, and forwards its document pointerdown to
// onOutsidePointer so the panel closes on an outside click without bar.js
// knowing the panel's DOM.
export function createSettingsPanel({ button, panel, host, getEffective, getCatalog, onPosition, onSize, onTheme, onPills, onReset }) {
  let open = false;

  // Position is a real radiogroup of native radios (arrow-key selection for
  // free). The change listener lives on the group, so rebuilding the radios when
  // the catalog changes keeps the wiring intact.
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
  section.append(label, group);
  let radios = new Map();
  let positionsKey = null;
  function syncPositions(choices) {
    const key = choiceKey(choices);
    if (key === positionsKey) return;
    positionsKey = key;
    radios = new Map();
    group.replaceChildren();
    for (const { id, label: text } of choices) {
      const radioLabel = document.createElement("label");
      radioLabel.className = "settings-radio";
      const input = document.createElement("input");
      input.type = "radio";
      input.name = "marquee-position";
      input.value = id;
      const span = document.createElement("span");
      span.textContent = text;
      radioLabel.append(input, span);
      group.appendChild(radioLabel);
      radios.set(id, input);
    }
  }

  // Size is a toggle-button group rather than radios: a few short, equal choices
  // read best as a compact segmented control. Each button carries the full label
  // as its accessible name while showing only its initial, and aria-pressed
  // marks the active preset so the state is exposed without native radio roles.
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
  sizeSection.append(sizeLabel, sizeGroup);
  let sizeButtons = new Map();
  let sizesKey = null;
  function syncSizes(choices) {
    const key = choiceKey(choices);
    if (key === sizesKey) return;
    sizesKey = key;
    sizeButtons = new Map();
    sizeGroup.replaceChildren();
    for (const { id, label: text } of choices) {
      const sizeButton = document.createElement("button");
      sizeButton.type = "button";
      sizeButton.className = "settings-size";
      sizeButton.dataset.size = id;
      sizeButton.textContent = text ? text[0].toUpperCase() : id;
      sizeButton.setAttribute("aria-label", text || id);
      sizeButton.setAttribute("aria-pressed", "false");
      sizeButton.addEventListener("click", () => onSize(id));
      sizeGroup.appendChild(sizeButton);
      sizeButtons.set(id, sizeButton);
    }
  }

  // Theme is a native <select>: a small, mutually exclusive list where the
  // option text names each theme, and a real select gives keyboard and
  // screen-reader support for free plus a visible current value. The change
  // listener lives on the select, so only its <option>s rebuild on a catalog
  // change.
  const themeSection = document.createElement("div");
  themeSection.className = "settings-section";
  const themeLabel = document.createElement("label");
  themeLabel.className = "settings-label";
  themeLabel.textContent = "Theme";
  const themeSelect = document.createElement("select");
  themeSelect.className = "settings-theme";
  themeSelect.id = "marquee-settings-theme";
  themeLabel.htmlFor = themeSelect.id;
  themeSelect.addEventListener("change", () => onTheme(themeSelect.value));
  themeSection.append(themeLabel, themeSelect);
  let themesKey = null;
  function syncThemes(choices) {
    const key = choiceKey(choices);
    if (key === themesKey) return;
    themesKey = key;
    themeSelect.replaceChildren();
    for (const { id, label: text } of choices) {
      const option = document.createElement("option");
      option.value = id;
      option.textContent = text;
      themeSelect.appendChild(option);
    }
  }

  // The Pills section is a per-id list: each row is a checkbox (shown/hidden)
  // with the pill's label plus ↑/↓ buttons to reorder. It reflects the effective
  // order, so hidden ids trail the visible ones; changing a checkbox or moving a
  // row applies live and persists through the same prefs plumbing as the other
  // knobs.
  const pillsSection = document.createElement("div");
  pillsSection.className = "settings-section";
  const pillsLabel = document.createElement("span");
  pillsLabel.className = "settings-label";
  pillsLabel.id = "marquee-settings-pills-label";
  pillsLabel.textContent = "Pills";
  const pillsGroup = document.createElement("div");
  pillsGroup.className = "settings-pills";
  pillsGroup.setAttribute("role", "group");
  pillsGroup.setAttribute("aria-labelledby", pillsLabel.id);
  pillsSection.append(pillsLabel, pillsGroup);

  // pendingFocus survives the rebuild in syncPills so keyboard reorder keeps
  // focus on the control the user just pressed. It names the pill and the
  // button that moved; syncPills refocuses that button, falling back to the
  // row's other button (when the pressed one becomes disabled at an end) and
  // then the checkbox, so keyboard operation never dead-ends.
  let pendingFocus = null;
  let lastPillsKey = null;

  function emitPills(order, visibleSet) {
    onPills(order.filter((id) => visibleSet.has(id)));
  }

  // syncPills rebuilds the rows only when the effective order, visibility, or
  // catalog labels actually changed, so a background status poll re-rendering the
  // bar never tears down rows the user is interacting with (which would drop
  // focus).
  function syncPills(choices) {
    const labelOf = new Map(choices.map((c) => [c.id, c.label]));
    const allIds = choices.map((c) => c.id);
    const visible = getEffective().pills;
    const visibleSet = new Set(visible);
    const order = [...visible, ...allIds.filter((id) => !visibleSet.has(id))];
    const key = choiceKey(choices) + "|" + order.map((id) => (visibleSet.has(id) ? "+" : "-") + id).join(",");
    if (key === lastPillsKey) {
      pendingFocus = null;
      return;
    }
    lastPillsKey = key;
    const rows = new Map();
    pillsGroup.replaceChildren();
    order.forEach((id, index) => {
      const row = document.createElement("div");
      row.className = "settings-pill";

      const rowLabel = document.createElement("label");
      rowLabel.className = "settings-pill-label";
      const checkbox = document.createElement("input");
      checkbox.type = "checkbox";
      checkbox.checked = visibleSet.has(id);
      checkbox.addEventListener("change", () => {
        const next = new Set(visibleSet);
        if (checkbox.checked) next.add(id);
        else next.delete(id);
        pendingFocus = { id, dir: "checkbox" };
        emitPills(order, next);
      });
      const text = document.createElement("span");
      text.textContent = labelOf.get(id) || id;
      rowLabel.append(checkbox, text);

      const up = document.createElement("button");
      up.type = "button";
      up.className = "settings-pill-move";
      up.textContent = "↑︎";
      up.setAttribute("aria-label", `Move ${labelOf.get(id) || id} up`);
      up.disabled = index === 0;
      up.addEventListener("click", () => {
        const next = [...order];
        [next[index - 1], next[index]] = [next[index], next[index - 1]];
        pendingFocus = { id, dir: "up" };
        emitPills(next, visibleSet);
      });

      const down = document.createElement("button");
      down.type = "button";
      down.className = "settings-pill-move";
      down.textContent = "↓︎";
      down.setAttribute("aria-label", `Move ${labelOf.get(id) || id} down`);
      down.disabled = index === order.length - 1;
      down.addEventListener("click", () => {
        const next = [...order];
        [next[index], next[index + 1]] = [next[index + 1], next[index]];
        pendingFocus = { id, dir: "down" };
        emitPills(next, visibleSet);
      });

      row.append(rowLabel, up, down);
      pillsGroup.appendChild(row);
      rows.set(id, { checkbox, up, down });
    });

    if (pendingFocus) {
      const controls = rows.get(pendingFocus.id);
      const dir = pendingFocus.dir;
      pendingFocus = null;
      if (controls && dir === "checkbox") {
        controls.checkbox.focus();
      } else if (controls) {
        const preferred = dir === "up" ? controls.up : controls.down;
        const other = dir === "up" ? controls.down : controls.up;
        const target = !preferred.disabled ? preferred : !other.disabled ? other : controls.checkbox;
        target.focus();
      }
    }
  }

  const resetButton = document.createElement("button");
  resetButton.type = "button";
  resetButton.className = "settings-reset";
  resetButton.textContent = "Reset";

  panel.replaceChildren(section, sizeSection, themeSection, pillsSection, resetButton);

  function sync() {
    const effective = getEffective();
    const catalog = getCatalog();
    syncPositions(catalog.positions);
    syncSizes(catalog.sizes);
    syncThemes(catalog.themes);
    for (const [position, input] of radios) {
      input.checked = position === effective.position;
    }
    for (const [size, sizeButton] of sizeButtons) {
      sizeButton.setAttribute("aria-pressed", String(size === effective.size));
    }
    themeSelect.value = effective.theme;
    syncPills(catalog.pills);
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
