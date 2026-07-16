import { sleep, receive } from "std/taak";
import { write } from "std/log";
// D4 kill-9 demo workflow over the std/taak authoring surface (ADR-10 §6):
//   - taak.sleep(...) is a durable checkpoint;
//   - std/log.write(...) records an EXTERNAL effect (an outbox intent the
//     dispatcher delivers effectively-once across the process boundary);
//   - taak.receive(...) is a message wake that must survive a mid-flight kill.
// The result aggregates across steps so a wrong resume yields a wrong value; the
// four writes are four outbox rows keyed UNIQUE(continuation_id, step_seq, ordinal).
export function w(): number {
  let acc = 0;
  for (let i = 0; i < 4; i++) {
    let c = 0;
    for (let j = 0; j < 300000; j++) { c = c + 1; }
    acc = acc + (i + 1) * 1000 + (c - 300000);
    write("step");
    sleep(100);
  }
  const bonus: number = receive("taakdone");
  return acc + bonus;
}
