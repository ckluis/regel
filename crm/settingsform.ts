import { card, stack, heading, text, label, field, select, button } from "std/ui";

// SettingsForm — the point-and-click tenant "add a field" surface (STAGE-F R2),
// hand-authored as rows (ADR-10 §7 tier-1, ADR-11 §1/§7 D3 lowering) exactly like
// AccountCard: it composes the closed 25-element vocabulary (card > stack > heading
// + label/field/select + button) into a Settings form. At admission it lowers to a
// `component_template` derived_artifact — the SAME static-skeleton / dynamic-leaf
// split every derived form rides — and mounts via `?component=app/crm/SettingsForm`
// through the identical RenderFirstPaint + Diff session path.
//
// It is PRESENTATION over the EXISTING admission gate: the operator picks a field
// name and type here; SUBMITTING walks the SAME HTTP /admit door scenario-a proves
// (re-admit the resource def under optimistic concurrency). The gate — not this
// form — decides what admits: a type outside the 13-type roster is refused at
// admission, never by ad-hoc form validation. There is no raw-HTML escape hatch; a
// name outside the roster does not resolve and never admits.
export function SettingsForm(props: { fieldName: string; fieldType: string }) {
  return card({}, [
    stack({}, [
      heading({ value: "Add a field to Account" }),
      label({ value: "Field name" }),
      field({ value: props.fieldName }),
      label({ value: "Field type" }),
      select({ value: props.fieldType }),
      text({ value: "Submitting re-admits Account through the /admit gate" }),
      button({ label: "Admit field-add" }),
    ]),
  ]);
}
