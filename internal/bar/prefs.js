// prefs.js is the pure, DOM-free core of the bar's preferences: the fallback
// catalog, per-key validators derived from a knob catalog, and load/merge/save/
// reset over a Storage-like object. Fail-open is law — every entry point returns
// a safe value and never throws, so a missing, throwing, or corrupt store (or a
// missing/stale status payload) falls back to the defaults rather than breaking
// the bar. bar.js and settings.js are the only consumers; keeping this module
// DOM-free makes it independently testable.
//
// The knob value sets live in one owner: the Go knob catalog, which rides the
// status payload. This module derives its live catalog and validators from that
// payload (effectiveCatalog/makeValidators). FALLBACK_CATALOG below is kept
// solely as the fail-open fallback for when the payload is absent or stale — the
// bar never renders before its first successful status fetch, so in practice the
// payload's catalog wins.

// FALLBACK_CATALOG mirrors the Go knob catalog's ids and labels. It is the
// fallback used only when the status payload carries no usable catalog; a live
// payload overrides it per knob. It carries no theme palettes — bar.js keeps a
// static default palette as its own visual fallback.
export const FALLBACK_CATALOG = {
  positions: [
    { id: "bottom-left", label: "Bottom left" },
    { id: "bottom-right", label: "Bottom right" },
    { id: "top-left", label: "Top left" },
    { id: "top-right", label: "Top right" },
  ],
  sizes: [
    { id: "small", label: "Small" },
    { id: "medium", label: "Medium" },
    { id: "large", label: "Large" },
  ],
  themes: [
    { id: "default", label: "Default" },
    { id: "midnight", label: "Midnight" },
    { id: "sand", label: "Sand" },
    { id: "forest", label: "Forest" },
  ],
  pills: [
    { id: "branch", label: "Branch" },
    { id: "dirty", label: "Uncommitted changes" },
    { id: "worktree", label: "Worktree" },
    { id: "pr", label: "Pull request" },
  ],
};

const ids = (choices) => choices.map((c) => c.id);

// POSITIONS/SIZES/THEMES/PILL_IDS are the fallback id lists. bar.js still reads
// PILL_IDS for its render-order fallback; the value lists otherwise flow through
// effectiveCatalog so the payload can drive them.
export const POSITIONS = ids(FALLBACK_CATALOG.positions);
export const SIZES = ids(FALLBACK_CATALOG.sizes);
export const THEMES = ids(FALLBACK_CATALOG.themes);
export const PILL_IDS = ids(FALLBACK_CATALOG.pills);

// DEFAULTS mirrors the status defaults' shape. It is the fallback used when no
// caller-supplied default and no stored value is valid. merge and validate stay
// generic over its keys.
export const DEFAULTS = {
  position: "bottom-left",
  size: "medium",
  theme: "default",
  pills: ["branch", "dirty", "worktree", "pr"],
};

// choicesOf reads one knob's ordered {id, label} list from a payload catalog,
// falling back to the built-ins for a missing or malformed entry. Never throws:
// a bad payload yields the fallback list, keeping the bar fail-open.
function choicesOf(knob, fallback) {
  const list = knob && Array.isArray(knob.choices) ? knob.choices : null;
  if (!list) return fallback;
  const out = [];
  for (const c of list) {
    if (c && typeof c.id === "string") {
      out.push({ id: c.id, label: typeof c.label === "string" ? c.label : c.id });
    }
  }
  return out.length ? out : fallback;
}

// effectiveCatalog derives the live knob catalog from the status payload,
// falling back per knob to FALLBACK_CATALOG. bar.js validates against it and
// settings.js builds its controls from it, so both derive their value lists and
// labels from the same payload the CLI validated against.
export function effectiveCatalog(status) {
  const cat = status && typeof status === "object" && status.catalog && typeof status.catalog === "object" ? status.catalog : {};
  return {
    positions: choicesOf(cat.positions, FALLBACK_CATALOG.positions),
    sizes: choicesOf(cat.sizes, FALLBACK_CATALOG.sizes),
    themes: choicesOf(cat.themes, FALLBACK_CATALOG.themes),
    pills: choicesOf(cat.pills, FALLBACK_CATALOG.pills),
  };
}

// makeValidators builds the per-key validators from a catalog's value lists. A
// key counts as "known" only when it appears both in the caller's defaults and
// here, so validate/merge stay generic: adding a knob is a new DEFAULTS entry
// plus a new validator, nothing else.
export function makeValidators(catalog) {
  const setOf = (choices) => new Set((choices || []).map((c) => c.id));
  const positions = setOf(catalog.positions);
  const sizes = setOf(catalog.sizes);
  const themes = setOf(catalog.themes);
  const pills = setOf(catalog.pills);
  return {
    position: (value) => positions.has(value),
    size: (value) => sizes.has(value),
    theme: (value) => themes.has(value),
    // pills must be an array of known ids with no duplicates; an empty array is
    // valid and hides every pill. An invalid value is dropped so the default
    // (all four, in order) wins, keeping the bar fail-open.
    pills: (value) => {
      if (!Array.isArray(value)) return false;
      const seen = new Set();
      for (const id of value) {
        if (!pills.has(id) || seen.has(id)) return false;
        seen.add(id);
      }
      return true;
    },
  };
}

// FALLBACK_VALIDATORS gates against the built-in catalog. It is the default for
// validate/merge so callers without a payload (and the module's own tests) still
// get sane validation; bar.js passes payload-derived validators instead.
const FALLBACK_VALIDATORS = makeValidators(FALLBACK_CATALOG);

const STORAGE_KEY = "marquee-bar-prefs";

// validate returns a sanitized copy of prefs containing only known keys whose
// values pass their validator. Unknown keys and invalid values are dropped
// (never coerced), leaving merge to fill the gaps from defaults.
export function validate(prefs, defaults, validators = FALLBACK_VALIDATORS) {
  const out = {};
  if (!prefs || typeof prefs !== "object") return out;
  for (const key of Object.keys(defaults)) {
    if (!Object.prototype.hasOwnProperty.call(prefs, key)) continue;
    const validator = validators[key];
    if (validator && validator(prefs[key])) {
      out[key] = prefs[key];
    }
  }
  return out;
}

// merge overlays valid stored keys onto defaults. Invalid or unknown stored
// keys are dropped by validate, so the default always wins for them.
export function merge(defaults, stored, validators = FALLBACK_VALIDATORS) {
  return { ...defaults, ...validate(stored, defaults, validators) };
}

// load parses the stored prefs object, returning {} for absence, malformed
// JSON, a non-object payload, or any storage error.
export function load(storage) {
  try {
    const raw = storage.getItem(STORAGE_KEY);
    if (!raw) return {};
    const parsed = JSON.parse(raw);
    return parsed && typeof parsed === "object" && !Array.isArray(parsed) ? parsed : {};
  } catch {
    return {};
  }
}

// save persists prefs. A throwing or absent storage (private mode, quota,
// disabled) is a silent no-op — persistence is best-effort, never required.
export function save(storage, prefs) {
  try {
    storage.setItem(STORAGE_KEY, JSON.stringify(prefs));
  } catch {
    // Storage may be unavailable or full; the live pref still applies.
  }
}

// reset clears the stored prefs, returning every knob to the caller's defaults.
export function reset(storage) {
  try {
    storage.removeItem(STORAGE_KEY);
  } catch {
    // Nothing stored, or storage unavailable — either way there is nothing to do.
  }
}
