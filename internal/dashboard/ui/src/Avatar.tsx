import { useEffect } from "react";
import { APIError, type Face } from "./api";

// Mirrors the Go avatars.Sizes, the only pictures the server keeps; change
// both together.
const avatarSizes = [
  { name: "small", px: 48 },
  { name: "medium", px: 128 },
  { name: "large", px: 512 },
] as const;

const avatarPath = (image: string, size: string) =>
  `/api/avatars/${encodeURIComponent(image)}/${size}`;

/**
 * The drawn picture to show at a size on the page. Each size is kept at
 * twice the pixels it is shown at, so it stays sharp on high-density screens.
 */
export function avatarURL(image: string, displayPx: number): string {
  const size = avatarSizes.find((s) => s.px >= displayPx * 2) ?? avatarSizes[2];
  return avatarPath(image, size.name);
}

/** Every size of a drawn picture, for the browser to choose from. */
export const avatarSrcSet = (image: string) =>
  avatarSizes.map((s) => `${avatarPath(image, s.name)} ${s.px}w`).join(", ");

/**
 * The small picture as a data URL, for the tab icon: some browsers fetch
 * icons without the session cookie, and the page's policy allows data
 * images.
 */
export async function avatarDataURL(image: string, signal?: AbortSignal) {
  const response = await fetch(avatarPath(image, "small"), {
    credentials: "same-origin",
    signal,
  });
  if (!response.ok) throw new APIError("No such picture", response.status);
  const blob = await response.blob();
  return new Promise<string>((resolve, reject) => {
    const reader = new FileReader();
    reader.onload = () =>
      typeof reader.result === "string"
        ? resolve(reader.result)
        : reject(new Error("Unreadable picture"));
    reader.onerror = () => reject(reader.error);
    reader.readAsDataURL(blob);
  });
}

/** The address of a vector avatar, for an img or the favicon. */
export const svgURL = (svg: string) =>
  `data:image/svg+xml,${encodeURIComponent(svg)}`;

export const hasFace = (face: Face) =>
  !!(face.avatar?.image || face.avatar_svg);

/**
 * A face: the picture Codex drew, or the vector sketch until there is one.
 * It is only ever shown as an image, so nothing in either can run or reach
 * the page. The name is always beside it, so it has no text of its own.
 */
export function Avatar({ of, size }: { of: Face; size: number }) {
  const image = of.avatar?.image;
  if (image)
    return (
      <img
        className="avatar avatar-drawn"
        src={avatarURL(image, size)}
        srcSet={avatarSrcSet(image)}
        sizes={`${size}px`}
        alt=""
        width={size}
        height={size}
      />
    );
  if (!of.avatar_svg) return null;
  return (
    <img
      className="avatar"
      src={svgURL(of.avatar_svg)}
      alt=""
      width={size}
      height={size}
    />
  );
}

export function useFavicon(assistant?: Face) {
  const sketch = assistant?.avatar_svg;
  const drawn = assistant?.avatar?.image;
  useEffect(() => {
    const icon = document.querySelector<HTMLLinkElement>('link[rel="icon"]');
    if (!icon) return;
    if (sketch) icon.href = svgURL(sketch);
    if (!drawn) return;
    const controller = new AbortController();
    avatarDataURL(drawn, controller.signal)
      .then((url) => {
        icon.href = url;
      })
      .catch(() => {
        // The sketch stays as the icon.
      });
    return () => controller.abort();
  }, [sketch, drawn]);
}
