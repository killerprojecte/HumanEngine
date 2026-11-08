# Native source-learning runtime

## Delivered scope

- `learningruntime` implements the actual ephemeral source coordinator: admission,
  quiet/max/min timing, foreground and overdue scheduling, retries and failures,
  manual retry/discard, trusted edit/recall invalidation and scene off/on handling.
  In-flight admission fences prevent a concurrent callback from restoring an old
  source after recall or scene cancellation. Current source snapshots are checked
  at admission and again before persistence. No messages are replayed at startup.
- `learningbatch` implements extraction, the shared optional empty-review/repair
  pass, scoped reconciliation and habit-first persistence. It uses native model
  transport, model-read tickets and ContextProjection. Extractor arguments cannot
  grant revision authority. Applying intent, pending outcome and saved/rejected/
  ignored totals remain separate; a zero saved count never permits replay after
  unknown or partial effects. Stage observations retain cancellation and partial
  effects without logging source text or changing business results.
- `learningowner` binds real settings/source/assertion, memory, habit, projection,
  model-read, model/budget and foreground owners. Source proposal attribution and
  pronoun checks precede the real memory command. Public review and embedding work
  follow the memory commit and never await a model inside its writer transaction.
- Native search preserves source lexical/vector ranking and target policies.
  Single-record indexing preserves bootstrap, encoder identity, support refresh,
  policy/content revalidation and durable vector publication. Public review uses
  exact native tool arguments, current evidence and a guarded promotion command.
- Borrowed physical scopes include cancellation-ignoring transport callbacks,
  role-timeout policy runners and the actual model-call SQL observer. Joining one
  learning operation does not close the shared model service.

Source limits are unchanged, including source batching, candidate/extra-pass
budgets, target selection limits and UTF-16 query slices. Pinned Zod lengths use
code points. Node-derived Unicode classes, source case-fold/word boundaries,
whitespace and WHATWG URLs preserve the relevant JavaScript behavior. There are
no newly introduced protective workload caps.

## Composition boundary

Create `learningowner.New` with the already-open owner graph and a pinned
`topiccollation.Collator` in `Dependencies.Options.Collator`. In the complete
application, use the real `SemanticRuntime.Memory` rather than constructing an
independent memory owner or no-op notification receiver. The constructor verifies
owner/database/authority bindings and adds an extension without replacing existing
Meme or other extensions. Call `SettingsChanged` after a committed settings update.
Call `CloseAndWait` before closing borrowed stores, model owners or their database.

This component does not install the complete application graph, replace HTTP
handlers, publish HTTP101, manage plugin startup, or implement administrative
embedding rebuild orchestration. Those are separate composition responsibilities.
No TypeScript server proxy or successful no-op production adapter is provided.

## Evidence and acceptance

Source generators execute the actual TypeScript coordinator, batch stages,
contracts, memory targets, public review and embedding/index owners with synthetic
inputs. The Unicode generator executes Node v24.22.1 property escapes directly.
All six generators have `--check` modes in the differential target. Fixtures are
synthetic, not live databases or user conversations.

Local checkpoint evidence before the final Unicode compatibility correction:

- r5: native race passed coordinator, batch, modelservice, providertransport,
  quiescence and memoryservice, including real support invalidation in both root
  and existing writer scopes. Owner failures were retained and corrected.
- r6: native owner race and eight-package vet passed. Tests include successful
  `New`, trusted foreground ingress, real batch persistence/indexing, extension
  coexistence, public promotion, writer close/reopen and no provider replay.
- r7: default-profile race and vet passed all fourteen affected/adjacent packages:
  assertions, habitservice, learningbatch, learningowner, learningruntime,
  memorylexical, memoryservice, modelservice, providertransport, quiescence,
  botruntime, sourceevents, modelread and projections. ICU-dependent cases alone
  are skipped in this profile; ordinary owner/SQL/public/index/reopen tests run.

The final r8 delta corrects source non-Unicode public-review case-folding,
Unicode pronoun word boundaries, quote-character classification and durable-signal
whitespace/line terminators. Its frozen manifest records the exact source tree.
The accompanying validation report records the narrow default/native rerun.
Full native affected-package and exact-head repository CI remain required before
claiming complete acceptance; prior checkpoints are not substitutes for those
checks. Native runs use `REQUIRE_ICU=1`, `sqlite_fts5,humanengine_icu` and the pinned
ICU build. All validation databases use isolated synthetic sibling main/authority
paths, with recovery readiness, integrity and foreign-key checks.
