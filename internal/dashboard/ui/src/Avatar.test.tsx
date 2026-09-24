// @vitest-environment jsdom
import { afterEach, describe, expect, it } from "vitest";
import { cleanup, render } from "@testing-library/react";
import { Avatar } from "./ui";

afterEach(cleanup);

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
});
