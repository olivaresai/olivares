# Optional idle session stops

Session auto-stop is an instance setting in Community, off by default. A system
administrator can change it in **Settings → Session memory** or with:

```sh
olivares config session-idle get
olivares config session-idle set --minutes 6
olivares config session-idle set --minutes 0
```

The value is `0` (off) or `6` to `10080` minutes **after** the existing five-minute
idle window. With `6`, the earliest stop is eleven minutes after the last output
or attempted message, plus up to thirty seconds for the lifecycle sweep. Saving
applies without a restart. The API is `GET`/`PUT /v1/system/session-idle` with
`{"idle_stop_minutes": 6}`; writes require the configured administrator step-up.

Only a finished, resumable session can stop: one that completed a turn and has
been silent for the whole window. An open conversation, attached output stream or
browser preview in use (a request in flight, such as a dev server's live-reload
stream, until the preview URL expires), pending approval, session tool call, running child, background task,
scheduled wakeup, or running linked peer prevents the stop. A newer message,
output or preview request restarts the window. A linked peer protects both endpoints while it is itself
active, including by the same-template rule: a peer in the middle of a turn, or
with an approval or tool call open, is active however long it has been silent,
and two peers that are both finished do not hold each other open. Work-managed
sessions keep their existing fenced lifecycle.

The engine must be able to verify the native activity record. Missing, changing,
partial, unreadable or unsupported activity, a transcript over the read bound, an
unreadable setting, or an incomplete peer read leaves the process running, and a
session that cannot be reclaimed yet is looked at again after five minutes. This
currently permits automatic reclamation of verified Claude Code and Codex
sessions. Codex sessions that spawned a child remain running because their root
record cannot verify child background commands. A resumed session that used a
package registry token also remains running, because its transcript cannot be
proven free of one an earlier launch printed. A session that was resumed and
never used has completed no turn, so it is not reclaimed. Other tools and
remote-control sessions remain running. No credentials or transcript content are
added to the policy audit record.

A policy stop is recorded as a system stop with `stop_cause: idle_policy` in the
audit ledger and `reason: idle_policy` on the session. The console says **Stopped
to free memory.** beside **Resume**. Resume uses the existing action and keeps the
same folder and conversation; it creates no new session and removes no history.
The policy reason is cleared when the session resumes, so a later stop by a person
is not shown as a stop to free memory.
