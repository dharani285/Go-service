# AI usage disclosure

- **Tool used:** ChatGPT.
- **What I used it for:** Interpreting the assignment, reviewing edge cases and design trade-offs, drafting the initial Go implementation and tests, and helping identify the four defects in the supplied debugging program.
- **Suggestion I rejected or changed:** I kept the service dependency-free and in-memory rather than adding SQLite immediately. The assignment explicitly permits in-memory persistence and prioritizes a working, understood solution in a 3.5–4 hour time box. I documented durability as the first production change. I also chose per-item batch validation rather than rejecting the entire batch when one item is malformed, because the brief explicitly calls out that failure mode.
- **One thing AI helped me understand:** Idempotency must be enforced across the full event-retention scope, and at scale at the durable storage boundary—not reset for each request or batch. Unique opens also require a campaign/contact composite identity.
- **AI-generated code I had to debug:** I compiled and ran the generated service, corrected a stale unused import, formatted the code, and ran the Go race detector and tests. I also independently verified the supplied debugging program against its exact expected output and with `-race`.

All generated code and design decisions should be reviewed and understood by the submitter before the interview. The implementation intentionally documents its in-memory durability limitation.
