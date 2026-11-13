// SPDX-License-Identifier: GPL-2.0-or-later
import { StrictMode } from "react";
import { createRoot } from "react-dom/client";
import AdminApp from "./App";

createRoot(document.getElementById("root")!).render(
  <StrictMode>
    <AdminApp />
  </StrictMode>,
);
