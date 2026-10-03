// @vitest-environment jsdom
import { render } from "preact";
import { act } from "preact/test-utils";
import { afterEach, describe, expect, it, vi } from "vitest";
import { Palette } from "./Palette";

vi.mock("../lib/api", () => ({ api: vi.fn().mockResolvedValue([]) }));
vi.mock("../lib/actions", () => ({ openThread: vi.fn() }));

const host = document.createElement("div");
document.body.append(host);
afterEach(() => { act(() => render(null, host)); });

describe("palette focus", () => {
  // Browsers honour the autofocus attribute once per page load (jsdom honours it
  // every time, so focus is observed rather than inferred from activeElement):
  // the input has to be focused explicitly on every opening.
  it("focuses its input each time it opens", async () => {
    const focus = vi.spyOn(HTMLElement.prototype, "focus");
    for (let opening = 1; opening <= 3; opening++) {
      await act(async () => { render(<Palette />, host); });
      const input = host.querySelector(".palette-input");
      expect(document.activeElement).toBe(input);
      expect(focus.mock.contexts.filter((el) => el === input)).toHaveLength(1);
      act(() => render(null, host));
      focus.mockClear();
    }
    focus.mockRestore();
  });
});
