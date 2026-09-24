import {
  useCallback,
  useEffect,
  useRef,
  useState,
  type RefObject,
} from "react";
import { focusedElement } from "./ui";

const chatKey = "crew-assistant.chat";

function rememberedChat() {
  try {
    return localStorage.getItem(chatKey) !== "closed";
  } catch {
    return true;
  }
}

const narrow = () =>
  typeof window.matchMedia === "function" &&
  window.matchMedia("(max-width: 1000px)").matches;

/**
 * Keeps keyboard focus inside `ref` while `active`, and hands it back to
 * whatever had it once the trap lifts.
 */
function useFocusTrap(
  ref: RefObject<HTMLElement | null>,
  active: boolean,
  onEscape: () => void,
) {
  const escape = useRef(onEscape);
  useEffect(() => {
    escape.current = onEscape;
  });
  useEffect(() => {
    if (!active) return;
    const prior = focusedElement();
    const keydown = (event: KeyboardEvent) => {
      if (event.key === "Escape") {
        event.preventDefault();
        escape.current();
      }
      // A Tab the contents already handled (the conversation accepting a
      // suggestion) is not focus movement.
      if (event.key !== "Tab" || event.defaultPrevented) return;
      const focusable = Array.from(
        ref.current?.querySelectorAll<HTMLElement>(
          "button:not([disabled]),textarea:not([disabled]),input:not([disabled]),a[href]",
        ) || [],
      ).filter((el) => el.getClientRects().length > 0);
      const first = focusable[0],
        last = focusable[focusable.length - 1];
      if (event.shiftKey && document.activeElement === first) {
        event.preventDefault();
        last?.focus();
      } else if (!event.shiftKey && document.activeElement === last) {
        event.preventDefault();
        first?.focus();
      }
    };
    document.addEventListener("keydown", keydown);
    return () => {
      document.removeEventListener("keydown", keydown);
      prior?.focus();
    };
  }, [ref, active]);
}

/**
 * The conversation's place on screen. On a wide screen it is a pane beside
 * the work, remembered between visits; on a narrow one it is a drawer over
 * it, closed until asked for.
 */
export function useChatPane() {
  const [paneOpen, setPaneOpen] = useState(rememberedChat);
  const [drawerOpen, setDrawerOpen] = useState(false);
  const [expanded, setExpanded] = useState(false);
  const conversation = useRef<HTMLElement>(null);
  const toggle = useCallback(() => {
    if (narrow()) {
      setDrawerOpen((open) => !open);
      return;
    }
    setPaneOpen((open) => {
      try {
        localStorage.setItem(chatKey, open ? "closed" : "open");
      } catch {
        // The choice still holds for this visit.
      }
      if (open) setExpanded(false);
      return !open;
    });
  }, []);
  useEffect(() => {
    const keydown = (event: KeyboardEvent) => {
      if ((event.metaKey || event.ctrlKey) && event.key.toLowerCase() === "j") {
        event.preventDefault();
        toggle();
      }
    };
    document.addEventListener("keydown", keydown);
    return () => document.removeEventListener("keydown", keydown);
  }, [toggle]);
  useFocusTrap(conversation, drawerOpen, () => setDrawerOpen(false));
  useEffect(() => {
    if (!drawerOpen) return;
    conversation.current
      ?.querySelector<HTMLButtonElement>(".mobile-close")
      ?.focus();
  }, [drawerOpen]);
  useEffect(() => {
    if (typeof window.matchMedia !== "function") return;
    const wide = window.matchMedia("(min-width: 1001px)");
    const changed = () => {
      if (wide.matches) setDrawerOpen(false);
      else setExpanded(false);
    };
    wide.addEventListener("change", changed);
    return () => wide.removeEventListener("change", changed);
  }, []);
  useEffect(() => {
    const navigated = () => {
      setExpanded(false);
      setDrawerOpen(false);
    };
    window.addEventListener("hashchange", navigated);
    return () => window.removeEventListener("hashchange", navigated);
  }, []);
  return {
    paneOpen,
    drawerOpen,
    expanded,
    shown: paneOpen || drawerOpen,
    conversation,
    toggle,
    close: () => (drawerOpen ? setDrawerOpen(false) : toggle()),
    toggleExpanded: () => {
      setExpanded(!expanded);
      requestAnimationFrame(() =>
        document.getElementById("chat-message")?.focus(),
      );
    },
  };
}
