import { resource } from "std/resource";
import { orgScoped } from "std/policy";

// Account — the CRM's root entity. A `states` field (stage) makes board(R)
// derivable (ADR-10 §7 tier-2); a `money` field (arr) + a `select` field (tier)
// give the derived dashboard its sum/count stat tiles. `website: url` and the
// closed base kinds keep the def inside the 13-type roster. This whole app is
// admitted as rows through the real gate (crm-setup.sh) — no hand-written DDL.
export const Account = resource({
  fields: {
    org: "text",
    name: "text",
    industry: "text",
    website: "url",
    arr: "money",
    tier: "select:free|pro|enterprise",
    stage: "states:prospect|active|churned",
  },
  policy: orgScoped,
});
