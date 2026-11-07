# Open a session draft from a link

Open `/sessions?newSession=1&tool=codex&workspace=%2Fsrv%2Fwork%2Frepo&prompt=Review%20the%20tests` on your console's origin.

The New session form opens with the tool, folder, and first message ready to edit.
Nothing starts until you press **Start** or send with Enter. The usual readiness,
permission, approval, and audit rules apply to that start.

- `newSession=1` opens the form. Other parameters alone do not open it.
- `tool` is a session tool's driver ID, such as `claude` or `codex`. Unknown IDs
  leave the usual tool choice in place.
- `workspace` is a registered workspace reference or a folder path. A registered
  workspace shows its name and path; a path opens the editable folder field.
- `prompt` is the first message, including newlines and Unicode. Encode values
  with `URLSearchParams` when constructing a link.

All three values are optional. The console removes the draft parameters from the
address after opening the form. Opening the form again normally uses your usual
defaults. A change of sign-in or organization drops the draft.

URLs can be stored in browser history or shared with others. Keep credentials and
other secrets out of them.
