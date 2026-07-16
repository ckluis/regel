import { resource } from "std/resource";
import { orgScoped } from "std/policy";

// Contact — a person at an Account. `email`/`phone` are pii-wrapped, so they
// derive NO base-table column (vault-routed only, ADR-10 §4 item 5): the derived
// masking leaves render a mask token and the plaintext never lands in the base or
// history tables. `account: belongsTo:Account` derives an account_id FK column.
export const Contact = resource({
  fields: {
    org: "text",
    account: "belongsTo:Account",
    name: "text",
    email: "pii:email",
    phone: "pii:phone",
    role: "text",
    lastTouch: "timestamp",
  },
  policy: orgScoped,
});
