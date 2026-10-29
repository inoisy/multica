// @vitest-environment jsdom

import { cleanup, screen } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { paths } from "@multica/core/paths";
import { renderWithI18n } from "../test/i18n";
import { SectionTabs, sectionHrefsFor } from "./section-tabs";

vi.mock("@multica/core/paths", async (importOriginal) => {
  const actual = await importOriginal<typeof import("@multica/core/paths")>();
  return { ...actual, useWorkspacePaths: () => actual.paths.workspace("acme") };
});
vi.mock("../navigation", () => ({
  AppLink: ({ href, children, className }: { href: string; children?: React.ReactNode; className?: string }) => (
    <a href={href} className={className}>{children}</a>
  ),
}));

afterEach(cleanup);

describe("SectionTabs", () => {
  it("titles the current page and links its siblings", () => {
    renderWithI18n(<SectionTabs section="agents" current="modelProfiles" />);
    expect(screen.getByRole("heading", { name: "Model profiles" })).toHaveAttribute("aria-current", "page");
    expect(screen.getByRole("link", { name: "Agents" })).toHaveAttribute("href", "/acme/agents");
  });

  it("shows the current page's count only", () => {
    renderWithI18n(<SectionTabs section="agents" current="agents" count={3} />);
    const heading = screen.getByRole("heading", { name: "Agents" });
    expect(heading).toHaveTextContent("3");
    expect(screen.getByRole("link", { name: /Model profiles/ })).toHaveAttribute("href", "/acme/profiles");
  });
});

describe("sectionHrefsFor", () => {
  const p = paths.workspace("acme");

  it("adds a section's fork pages to its sidebar entry", () => {
    expect(sectionHrefsFor(p, "/acme/issues")).toEqual(["/acme/issues"]);
    expect(sectionHrefsFor(p, "/acme/agents")).toEqual(["/acme/agents", "/acme/profiles"]);
    expect(sectionHrefsFor(p, "/acme/runtimes")).toEqual(["/acme/runtimes"]);
    expect(sectionHrefsFor(p, "/acme/usage")).toEqual(["/acme/usage"]);
  });

  it("leaves other entries alone", () => {
    expect(sectionHrefsFor(p, "/acme/projects")).toEqual(["/acme/projects"]);
  });
});
