import { resource } from "std/resource";
import { orgScoped } from "std/policy";

// Activity — a touch (call/email/meeting) logged against an Account and/or a
// Contact. `kind: select` gives the dashboard a count-per-member tile; `note:
// longtext`, `on: timestamp`, `done: boolean` exercise more of the closed 13-type
// roster. Two belongsTo relations derive account_id + contact_id FK columns.
export const Activity = resource({
  fields: {
    org: "text",
    account: "belongsTo:Account",
    contact: "belongsTo:Contact",
    kind: "select:call|email|meeting",
    note: "longtext",
    on: "timestamp",
    done: "boolean",
  },
  policy: orgScoped,
});
