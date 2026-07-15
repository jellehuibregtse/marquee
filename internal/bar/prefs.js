// prefs.js is the pure, DOM-free core of the bar's preferences: the default
// table, per-key validators, and load/merge/save/reset over a Storage-like
// object. Fail-open is law — every entry point returns a safe value and never
// throws, so a missing, throwing, or corrupt store falls back to the
// defaults rather than breaking the bar. bar.js and settings.js are the only
// consumers; keeping this module DOM-free makes it independently testable.

export const POSITIONS = ["bottom-left", "bottom-right", "top-left", "top-right"];

export const SIZES = ["small", "medium", "large"];

// DEFAULTS mirrors the status defaults' shape. It is the fallback used when no
// caller-supplied default and no stored value is valid. Later PRs extend it
// with theme/pills; merge and validate stay generic over its keys.
export const DEFAULTS = {
  position: "bottom-left",
  size: "medium",
};

// VALIDATORS gates each known key. A key counts as "known" only when it appears
// both in the caller's defaults and here, so validate/merge stay generic: adding
// a knob is a new DEFAULTS entry plus a new validator, nothing else.
const VALIDATORS = {
  position: (value) => POSITIONS.includes(value),
  size: (value) => SIZES.includes(value),
};

const STORAGE_KEY = "marquee-bar-prefs";

// validate returns a sanitized copy of prefs containing only known keys whose
// values pass their validator. Unknown keys and invalid values are dropped
// (never coerced), leaving merge to fill the gaps from defaults.
export function validate(prefs, defaults) {
  const out = {};
  if (!prefs || typeof prefs !== "object") return out;
  for (const key of Object.keys(defaults)) {
    if (!Object.prototype.hasOwnProperty.call(prefs, key)) continue;
    const validator = VALIDATORS[key];
    if (validator && validator(prefs[key])) {
      out[key] = prefs[key];
    }
  }
  return out;
}

// merge overlays valid stored keys onto defaults. Invalid or unknown stored
// keys are dropped by validate, so the default always wins for them.
export function merge(defaults, stored) {
  return { ...defaults, ...validate(stored, defaults) };
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
