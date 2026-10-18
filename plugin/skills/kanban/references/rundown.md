# Rundown example

About 150 words, built from `kanban status` and `kanban list`, nothing written:

```text
Look at first: CHECKOUT-3DS, P0 in review since Oct 10; the provider declines EU cards from 2026-11-01.

In flight (3):
- CHECKOUT-3DS, P0, review: 3-D Secure for EU cards, dev-sam. Needs a reviewer, then a verifier.
- API-TOKENS, P1, in progress: personal access tokens, dev-ana.
- SEARCH-TYPO, P2, ready: typo-tolerant search, unclaimed.

Waiting:
- API-TOKENS depends on INFRA-7 (staging migrations 0040-0045), still in backlog: its migration cannot be verified until INFRA-7 runs. Small, P1, worth pulling now.
- No tickets in blocked.

Done: 1. Epoch 1, running.
```
