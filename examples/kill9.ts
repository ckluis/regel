import { sleep, send } from "std/wf";
// Stage-B kill-9 demo workflow: four steps, each a nontrivial compute loop plus
// a recorded effect plus a durable sleep. The result aggregates across steps so
// a wrong resume produces a wrong value; the four sends are the exactly-once
// outbox trace (UNIQUE (continuation_id, step_seq, ordinal)).
export function w(): number {
  let acc = 0;
  for (let i = 0; i < 4; i++) {
    let c = 0;
    for (let j = 0; j < 300000; j++) { c = c + 1; }
    acc = acc + (i + 1) * 1000 + (c - 300000);
    send("kstep", acc);
    sleep(100);
  }
  return acc;
}
