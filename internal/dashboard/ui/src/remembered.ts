import { useState } from "react";

function storedChoice(key: string) {
  try {
    return localStorage.getItem(key) ?? "";
  } catch {
    return "";
  }
}

/** A choice kept in this browser, so the page opens the way it was left. */
export function useRemembered<T extends string>(
  key: string,
  read: (value: string) => T,
) {
  const [value, setValue] = useState(() => read(storedChoice(key)));
  const remember = (next: T) => {
    setValue(next);
    try {
      localStorage.setItem(key, next);
    } catch {
      // Storage can be unavailable; the choice still holds until a reload.
    }
  };
  return [value, remember] as const;
}
