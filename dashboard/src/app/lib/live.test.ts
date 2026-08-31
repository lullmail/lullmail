// @vitest-environment jsdom
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

vi.mock("./actions", () => ({
  refreshCounts: vi.fn(),
  refreshAccounts: vi.fn(),
  reload: vi.fn(),
}));

import { onSyncHint } from "./live";
import { refreshAccounts, refreshCounts, reload } from "./actions";

describe("live sync hints", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    vi.useFakeTimers();
  });
  afterEach(() => {
    vi.useRealTimers();
  });

  it("coalesces a burst of hints into one re-read", () => {
    onSyncHint();
    onSyncHint();
    onSyncHint();
    expect(refreshCounts).not.toHaveBeenCalled();
    vi.advanceTimersByTime(200);
    expect(refreshCounts).toHaveBeenCalledTimes(1);
    expect(refreshAccounts).toHaveBeenCalledTimes(1);
    expect(reload).toHaveBeenCalledTimes(1);
  });

  it("does not re-read before the debounce window closes", () => {
    onSyncHint();
    vi.advanceTimersByTime(100);
    expect(reload).not.toHaveBeenCalled();
    vi.advanceTimersByTime(100);
    expect(reload).toHaveBeenCalledTimes(1);
  });
});
