// SPDX-License-Identifier: GPL-2.0-or-later
import { prepareSessions } from "./session";

export default async function globalSetup() {
  if (!(await prepareSessions()))
    console.log("screen tests skipped: set E2E_SETUP_TOKEN or E2E_ADMIN_PASSWORD");
}
