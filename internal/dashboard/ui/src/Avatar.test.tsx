// @vitest-environment jsdom
import { afterEach, describe, expect, it, vi } from "vitest";
import { cleanup, render, renderHook, waitFor } from "@testing-library/react";
import { Avatar, avatarURL, useFavicon } from "./Avatar";

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
});

const svg = '<svg xmlns="http://www.w3.org/2000/svg"/>';
const image = "0123456789abcdef0123456789abcdef";
const shown = (container: HTMLElement) => container.querySelector("img");

describe("Avatar", () => {
  it("shows the drawn picture at the size that suits it, with every size to choose from", () => {
    const { container } = render(
      <Avatar of={{ avatar: { image }, avatar_svg: svg }} size={20} />,
    );
    const img = shown(container);
    expect(img?.getAttribute("src")).toBe(`/api/avatars/${image}/small`);
    expect(img?.getAttribute("srcset")).toBe(
      `/api/avatars/${image}/small 48w, /api/avatars/${image}/medium 128w, /api/avatars/${image}/large 512w`,
    );
    expect(img?.getAttribute("sizes")).toBe("20px");
    expect(img?.getAttribute("alt")).toBe("");
    expect(img?.getAttribute("width")).toBe("20");
  });

  it("uses the medium and large pictures for bigger faces", () => {
    const medium = render(<Avatar of={{ avatar: { image } }} size={64} />);
    expect(shown(medium.container)?.getAttribute("src")).toBe(
      `/api/avatars/${image}/medium`,
    );
    const large = render(<Avatar of={{ avatar: { image } }} size={96} />);
    expect(shown(large.container)?.getAttribute("src")).toBe(
      `/api/avatars/${image}/large`,
    );
  });

  it("falls back to the vector sketch until there is a picture", () => {
    const { container } = render(
      <Avatar of={{ avatar: { shape: "orb" }, avatar_svg: svg }} size={40} />,
    );
    const img = shown(container);
    expect(img?.getAttribute("src")).toBe(
      `data:image/svg+xml,${encodeURIComponent(svg)}`,
    );
    expect(img?.hasAttribute("srcset")).toBe(false);
  });

  it("shows nothing without either", () => {
    const { container } = render(<Avatar of={{}} size={40} />);
    expect(shown(container)).toBeNull();
  });

  it("has no picture address without a picture", () => {
    const { container } = render(
      <Avatar of={{ avatar: { shape: "orb" } }} size={20} />,
    );
    expect(shown(container)).toBeNull();
  });
});

describe("drawn avatars", () => {
  it("picks the stored size that stays sharp at twice the pixels shown", () => {
    const at = (px: number) => avatarURL(image, px);
    expect(at(16)).toBe(`/api/avatars/${image}/small`);
    expect(at(24)).toBe(`/api/avatars/${image}/small`);
    expect(at(25)).toBe(`/api/avatars/${image}/medium`);
    expect(at(64)).toBe(`/api/avatars/${image}/medium`);
    expect(at(65)).toBe(`/api/avatars/${image}/large`);
    expect(at(96)).toBe(`/api/avatars/${image}/large`);
  });
});

describe("the favicon", () => {
  const withIcon = () => {
    const icon = document.createElement("link");
    icon.rel = "icon";
    icon.href = "data:,";
    document.head.append(icon);
    return icon;
  };
  const serve = (picture: { status: number; body?: Blob }) => {
    const fetch = vi.fn(async (_path: string, _options?: RequestInit) => ({
      ok: picture.status === 200,
      status: picture.status,
      blob: async () => picture.body,
    }));
    vi.stubGlobal("fetch", fetch);
    return fetch;
  };

  it("uses the drawn picture, fetched with the session", async () => {
    const icon = withIcon();
    const fetch = serve({
      status: 200,
      body: new Blob(["png"], { type: "image/png" }),
    });
    renderHook(() => useFavicon({ avatar: { image }, avatar_svg: svg }));
    await waitFor(() =>
      expect(icon.getAttribute("href")).toBe(
        `data:image/png;base64,${btoa("png")}`,
      ),
    );
    const fetched = fetch.mock.calls.find(
      ([path]) => path === `/api/avatars/${image}/small`,
    );
    expect(fetched?.[1]?.credentials).toBe("same-origin");
    icon.remove();
  });

  it("keeps the sketch when the picture can't be fetched", async () => {
    const icon = withIcon();
    const fetch = serve({ status: 404 });
    renderHook(() => useFavicon({ avatar: { image }, avatar_svg: svg }));
    await waitFor(() => expect(fetch).toHaveBeenCalled());
    await waitFor(() =>
      expect(icon.getAttribute("href")).toBe(
        `data:image/svg+xml,${encodeURIComponent(svg)}`,
      ),
    );
    icon.remove();
  });
});
