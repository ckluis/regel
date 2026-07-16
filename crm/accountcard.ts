import { card, stack, heading, text, badge } from "std/ui";

// AccountCard — a hand-authored component (ADR-10 §7 tier-1, ADR-11 §1 D3
// lowering): it composes the closed 25-element vocabulary (card > stack > heading
// + text, plus a badge) around Account fields. At admission it lowers to a
// `component_template` derived_artifact — the same static-skeleton / dynamic-leaf
// split a derived detail rides — and mounts over a resource row via
// `?component=app/crm/AccountCard`, overlaying the derived detail slot with the
// SAME masking-aware, diffable, live render path. There is no raw-HTML escape
// hatch: a name outside the 25 roster does not resolve and never admits.
export function AccountCard(props: { name: string; industry: string; stage: string }) {
  return card({}, [
    stack({}, [
      heading({ value: props.name }),
      text({ value: props.industry }),
    ]),
    badge({ value: props.stage }),
  ]);
}
