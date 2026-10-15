import "@testing-library/jest-dom/vitest";
import * as axeMatchers from "vitest-axe/matchers";
import { beforeEach, expect } from "vitest";
import { configure } from "@testing-library/react";

expect.extend(axeMatchers);

// Workspace reads can await a namespace lookup before issuing their own API
// query. Allow that render chain to settle on shared CI workers without sleeps.
configure({ asyncUtilTimeout: 3000 });

// Hash routing is real navigation state: without this, a test that navigated
// to #/keys would leave the next test starting there.
beforeEach(() => {
  window.location.hash = "";
});
