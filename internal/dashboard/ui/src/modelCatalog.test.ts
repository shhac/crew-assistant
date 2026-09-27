import { expect, it } from "vitest";
import { type Catalog, type ModelOption, modelLabel } from "./modelCatalog";

const catalog: Catalog = {
  available: true,
  detail: "",
  engine: "claude",
  models: [],
  current: { model: "", effort: "" },
  default: { model: "sonnet", effort: "" },
};
const option = (o: Partial<ModelOption>): ModelOption => ({
  id: "opus[1m]",
  name: "Opus 5.5",
  efforts: [],
  is_default: false,
  ...o,
});

it("names the concrete model an alias selects beside it", () => {
  expect(modelLabel(option({ resolved: "claude-opus-5-5" }), catalog)).toBe(
    "Opus 5.5 · claude-opus-5-5",
  );
  expect(
    modelLabel(
      option({ id: "sonnet", name: "Sonnet", resolved: "claude-sonnet-5" }),
      catalog,
    ),
  ).toBe("Sonnet · claude-sonnet-5 (recommended)");
});

it("names a model once when there is nothing more to say", () => {
  expect(modelLabel(option({}), catalog)).toBe("Opus 5.5");
  expect(
    modelLabel(
      option({ id: "gpt-6", name: "gpt-6", resolved: "gpt-6" }),
      catalog,
    ),
  ).toBe("gpt-6");
});
