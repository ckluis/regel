import { put } from "std/files";
import { t } from "std/i18n";
import type { File } from "std/files";

// attach — the reference CRM's document-attachment workflow (STAGE-F R13). It is
// the RED PATH that earns the `std/files` and `std/i18n` batteries their promotion
// from DEFER to SHIP: before promotion both imports fail to resolve and admission
// REFUSES this def; after promotion it admits, runs, and spools a real artifact.
//
// It consumes BOTH batteries in one honest step:
//   - std/i18n.t(bundle, locale, key) LOCALIZES the attachment's file name — the
//     scenario sets `locale` (es → "Contrato.txt", en → "Contract.txt"), proving
//     the pure translation-rows lookup with its fixed fallback chain;
//   - std/files.put(account, name, contentType, content) attaches the document —
//     an external-sink effect whose CONTENT-ADDRESSED handle (id = SHA-256(content))
//     and content are spooled effectively-once through the SAME outbox/FileSink door
//     the follow-up workflow's mail.send rides. The spooled artifact IS the durable,
//     readable attachment (download = read its payload.content).
//
// The return value is the file id (a plain string) so a completed run is also
// observable via /continuation/{id}. No raw-HTML/blob escape hatch: files.put is a
// declared external sink, so V2 rejects a Vault value routed into `content` unmasked.
export function attach(account: string, locale: string, content: string): string {
  const bundle = {
    en: { contract: "Contract" },
    es: { contract: "Contrato" },
  };
  const name = t(bundle, locale, "contract") + ".txt";
  const f: File = put(account, name, "text/plain", content);
  return f.id;
}
