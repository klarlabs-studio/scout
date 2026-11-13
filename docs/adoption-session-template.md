# Adoption study session record

Copy one record per participant. Keep names and contact information out of
this file. Use `P01`, `P02`, and so on; store recruitment details separately
and delete them when follow-up ends.

## Context and interview

- Study ID and date:
- Role and team-size band:
- Production browser workload and current stack:
- Owns operations/on-call?:
- Recent incident sequence (participant's words, anonymized):
- How often and what observed cost:
- Authoritative evidence used to determine outcome:
- Current recovery behavior after timeout/restart:
- Evaluated and rejected alternatives:
- Change trigger within next quarter:
- Evidence label for each note: `current practice`, `recent example`,
  `desired feature`, or `guess`:
- Quote retention consent (yes/no); quote text only if yes:

## Paired implementation

- Order (`AB` or `BA`):
- Machine / OS / Go / Chrome versions:
- Fixture commit and fault boundary:
- Participant source checkout path and preservation location:
- Checkout Git HEAD / changed paths recorded? (`yes`/`no`):
- Source manifest verified? (file count / changed or missing paths):
- Source review completed? (reviewer / participant-authored LOC):
- Source review attested for Scout arm? (`true`/`false`):
- Source review attested for Stagehand Go arm? (`true`/`false`):
- Checkout preserved for Scout arm? (`true`/`false`):
- Checkout preserved for Stagehand Go arm? (`true`/`false`):
- Binary build command, toolchain version, and binary digest (if prebuilt):
- Time limit (60 minutes per product arm; 120 minutes total) and deviations:

| Measure | Scout | Stagehand Go |
|---|---:|---:|
| Product/package version | | |
| Start to first verified outcome (seconds) | | |
| Nominal approval control duration (seconds) | | |
| Nominal control passed? | | |
| Nominal control ledger count | | |
| Full task duration (seconds) | | |
| Assistance requests | | |
| Participant-authored integration LOC | | |
| Browser dispatch count | | |
| Fixture ledger count | | |
| Final classification (`verified`/`rejected`/`unknown`) | | |
| Preserved `unknown` before ledger read? | | |
| Read authoritative ledger before decision? | | |
| Blind retry / duplicate effect? | | |
| Unacceptable Scout friction? (`true` / `false`) | | |
| Owner scope (`native`/`configured`/`participant_glue`/`absent`/`unknown`) | | |
| Session lifecycle and reconnect (`native`/`configured`/`participant_glue`/`absent`/`unknown`) | | |
| Target binding (`native`/`configured`/`participant_glue`/`absent`/`unknown`) | | |
| Origin policy (`native`/`configured`/`participant_glue`/`absent`/`unknown`) | | |
| Caller deadline (`native`/`configured`/`participant_glue`/`absent`/`unknown`) | | |
| Verified postcondition (`native`/`configured`/`participant_glue`/`absent`/`unknown`) | | |
| Stable task identity (`native`/`configured`/`participant_glue`/`absent`/`unknown`) | | |
| Durable task state (`native`/`configured`/`participant_glue`/`absent`/`unknown`) | | |
| Same-ID replay guard (`native`/`configured`/`participant_glue`/`absent`/`unknown`) | | |
| Host reconciliation with evidence provenance (`native`/`configured`/`participant_glue`/`absent`/`unknown`) | | |
| Blocker or unacceptable friction | | |

## Debrief

- Preference after both tasks:
- Specific reason tied to participant's workflow:
- What would need to change before adoption:
- Would they evaluate or deploy within the next quarter? Why:
- Protocol deviations, incomplete work, or exclusion decision:

Retain raw timings and failures. Do not infer a production problem from a
requested feature or a hypothetical. Keep interview evidence separate from
task performance and preference.
