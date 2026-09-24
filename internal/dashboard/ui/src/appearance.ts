export type Appearance = "system" | "light" | "dark";

const key = "crew-assistant.appearance";

export function appearanceOf(value?: string): Appearance {
  return value === "light" || value === "dark" ? value : "system";
}

/**
 * Applies the owner's appearance and remembers it in this browser, so the
 * next load, and the pairing screen that cannot read settings, start in it.
 */
export function applyAppearance(value?: string) {
  const choice = appearanceOf(value);
  const root = document.documentElement;
  if (choice === "system") delete root.dataset.theme;
  else root.dataset.theme = choice;
  try {
    localStorage.setItem(key, choice);
  } catch {
    // Storage can be unavailable; the appearance still applies for now.
  }
}

export function rememberedAppearance(): Appearance {
  try {
    return appearanceOf(localStorage.getItem(key) ?? undefined);
  } catch {
    return "system";
  }
}
