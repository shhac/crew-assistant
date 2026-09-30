/**
 * jsdom lays nothing out, so every element is 0 high and never scrolls.
 * This gives each element a height of 40px per child and a window of
 * `shown` pixels, with a scrollTop kept within them as a browser keeps it,
 * so tests can tell where a box was scrolled to. It returns the undo.
 */
export function layOutScrolling(shown = 200) {
  const proto = HTMLElement.prototype;
  const tops = new WeakMap<Element, number>();
  const height = (el: Element) => el.childElementCount * 40;
  Object.defineProperties(proto, {
    scrollHeight: {
      configurable: true,
      get(this: Element) {
        return height(this);
      },
    },
    clientHeight: {
      configurable: true,
      get() {
        return shown;
      },
    },
    scrollTop: {
      configurable: true,
      get(this: Element) {
        return tops.get(this) ?? 0;
      },
      set(this: Element, value: number) {
        const most = Math.max(0, height(this) - shown);
        tops.set(this, Math.min(Math.max(0, value), most));
      },
    },
  });
  // What jsdom gives every element is inherited again once these go.
  return () => {
    for (const name of ["scrollHeight", "clientHeight", "scrollTop"])
      delete (proto as unknown as Record<string, unknown>)[name];
  };
}

/** Whether a box is scrolled all the way down. */
export const atBottom = (el: Element) =>
  el.scrollTop > 0 && el.scrollTop === el.scrollHeight - el.clientHeight;
