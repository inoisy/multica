// @vitest-environment jsdom

import { describe, expect, it, vi } from "vitest";
import { fireEvent, screen, waitFor } from "@testing-library/react";
import type { Squad } from "@multica/core/types";
import { renderWithI18n } from "../../test/i18n";
import { SquadRouteTab } from "./squad-detail-page";

const squad = { id: "sq-1", name: "Разработка", route_template: "" } as Squad;

describe("SquadRouteTab", () => {
  it("saves the trimmed route and turns it off", async () => {
    const onSave = vi.fn().mockResolvedValue(undefined);
    const { rerender } = renderWithI18n(<SquadRouteTab squad={squad} canManage onSave={onSave} />);
    const input = screen.getByLabelText("Route");
    fireEvent.change(input, { target: { value: "  dev:A,qa:B,review:C,accept:D,close:E " } });
    fireEvent.click(screen.getByRole("button", { name: "Save" }));
    await waitFor(() => expect(onSave).toHaveBeenCalledWith("dev:A,qa:B,review:C,accept:D,close:E"));

    rerender(<SquadRouteTab squad={{ ...squad, route_template: "dev:A,qa:B,close:E" }} canManage onSave={onSave} />);
    fireEvent.click(screen.getByRole("button", { name: "Turn off" }));
    await waitFor(() => expect(onSave).toHaveBeenLastCalledWith(""));
  });

  it("is read-only for a viewer who cannot manage the squad", () => {
    renderWithI18n(<SquadRouteTab squad={{ ...squad, route_template: "dev:A,qa:B,close:E" }} canManage={false} onSave={vi.fn()} />);
    expect((screen.getByLabelText("Route") as HTMLInputElement).readOnly).toBe(true);
    expect(screen.queryByRole("button", { name: "Save" })).toBeNull();
  });
});
