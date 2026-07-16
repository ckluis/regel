import { sleep } from "std/taak";
import { send } from "std/mail";

// followup — a REAL std/taak workflow (ADR-10 §6): a durable `sleep` checkpoint
// (await-as-checkpoint — the continuation is parked as a row and survives a kill),
// then a `mail.send` follow-up reminder. mail.send is capability-gated
// (`mail.send`, declared at admission + granted to the runner) and effect class
// `external`: the step transaction writes ONE outbox intent the dispatcher delivers
// effectively-once through the FileSink spool (dedup-keyed). The return value is a
// plain string so a completed run is observable via /continuation/{id}.
export function followup(account: string): string {
  sleep(100);
  send("owner@acme.example", "Follow up with " + account);
  return "reminder-sent";
}
