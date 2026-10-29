# Complete-evidence handoff budgets

This slice removes the language-module memory excerpt's 1,200-character truncation. A denial, exception, expiry, disputed status, or “not yet confirmed” at the end of a selected record remains intact. Source availability and valid identifiers still do not prove that a model-generated claim is true.

## Planner candidate messages

`plannerEvidenceMaxBytes` defaults to 131,072 UTF-8 bytes. Before each Planner completion, the implementation measures the JSON serialization of the actual cumulative `ChatMessage[]`, including initial instructions, assistant messages and tool receipts with JSON escaping. This is not a token count, provider bill, or full wire-request bound: tool definitions and provider-specific framing are outside this measurement.

A `memory.search` or `memory.person` result is atomic. The current schema cannot reliably identify every conflicting or qualifying companion, so it never greedily drops individual records from a result. If the full candidate set does not fit, an empty delivered value explicitly reports limited coverage and the number omitted. It does not mean the library is empty. The Planner can choose a narrower query within its existing round/tool caps. Its final selection validator only accepts records actually delivered. Preparation captures the authorized body and exact revision without exposing it as a learning dependency. Only the synchronous delivery boundary after packing commits selected records; stale or altered bodies cannot acquire newer authority. Upstream limited-coverage metadata survives even when no memory is selected for Replyer. Non-memory receipts are not truncated; an oversized receipt or fixed prompt fails explicitly before the next model call.

## Replyer initial handoff

`replyEvidenceMaxBytes` defaults to 65,536 UTF-8 bytes. Selected memories are kept complete, including epistemic and temporal qualifications. Memory receipts jointly supporting a fact form an indivisible group. Omitting that group also removes its dependent facts; a mixed web/memory conclusion cannot retain its conclusion after losing its memory condition. Unrelated web/message/image evidence and actual action outcomes remain. A memory-dependent fact with no selected memory record is not handed off as supported evidence.

Any omission is explicitly reported, removes the optional suggested reply, and replaces free-worded strategy/goals with neutral instructions to use retained evidence. Constraints remain intact. Omission does not authorize the language module to infer missing facts or to claim no memory exists. This closure prevents budget packing from creating a new unsupported conclusion; it is not semantic entailment verification of the Planner's original claims.

The packer measures the final short-reference user-payload JSON, including its coverage metadata, and sends that exact string. It does not silently reserialize a different payload. The fixed envelope must itself fit. The limit covers the initial Replyer user payload, not its system message, provider framing, or subsequent dialogue tool rounds. Canonical execution receipts and current source/version/epoch validation remain separate and authoritative.

## Limits and validation

Defaults are engineering starting points, not empirically optimal values. The UI labels bytes explicitly. The pure selector also accepts a trusted server-injected tokenizer identity and count function, but this slice does not register a production tokenizer or claim calibration. Synthetic tokenizer tests only validate plumbing. Request spending admission remains a separate opt-in ledger and its estimates must not be confused with these exact byte measurements.

Whole-group packing bounds candidate groups and expanded record counts. It cannot confer source access, change learning permissions, enable graph experiments, or execute actions. Real model quality, provider token totals and cost/quality tradeoffs require the later authorized evaluation.

## Remaining approved implementation work

This is one incremental slice, not the completed memory architecture. Remaining work includes source-linked automatic subject attribution (explicit self-statements versus third-person reports and unknown subjects), structured relation candidates and their lifecycle, default-off graph projection/shadow/answer controls, externally evaluated experience candidates, full-model prompt/token measurement with a calibrated target tokenizer, and further domain-level extraction from the memory/learning facades. A read-only person page with manually created records is not proof of automatic profile creation. Synthetic extraction receipts validate contracts, not real model interpretation quality.

## Historical slice note

This document records the evidence-budget slice at its original boundary. Later slices added subject attribution, relation and plan tools, module extraction and complete native-request measurement. For the current implemented/experimental/unimplemented matrix, use [memory-current-status.md](memory-current-status.md); do not read older remaining-work lists as the current delivery status.
