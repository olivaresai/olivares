#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Olivares.AI
# SPDX-License-Identifier: AGPL-3.0-only
# One message policy for commit-msg, the identity gate and PR CI.
set -euo pipefail
export LC_ALL=C

if [[ "${1:-}" == --range ]]; then
  [[ $# == 2 && "$2" != -* ]] || { echo 'usage: check-commit-trailers.sh --range <base>..<head>' >&2; exit 2; }
  commits="$(git --no-replace-objects -c core.useReplaceRefs=false rev-list "$2")" || exit 2
  for commit in $commits; do
    if ! git --no-replace-objects -c core.useReplaceRefs=false show -s --format=%B "$commit" | bash "$0"; then
      echo "commit-trailers: refused commit $commit" >&2
      exit 1
    fi
  done
  exit 0
fi

git_pid=
if [[ "${1:-}" == --commit-msg ]]; then
  [[ $# == 3 && "$2" =~ ^[1-9][0-9]*$ ]] || exit 2
  git_pid=$2
  shift 2
fi
[[ $# -le 1 ]] || { echo 'usage: check-commit-trailers.sh [message-file]' >&2; exit 2; }
message="$(cat -- "${1:--}")" || exit 2
# Parsing is repository-independent. Ignore configurable aliases/separators so
# an Acked-by alias cannot become a DCO sign-off locally but fail in CI.
# commit-msg sees the message before Git's final cleanup. Git does not export
# the effective mode; config alone misses --cleanup overrides. Read only the
# invoking process's NUL-delimited arguments, never evaluate or print them.
cleanup=verbatim
verbose=0
git_command=
if [[ -n "$git_pid" ]]; then
  cleanup=unknown
  if [[ -r /proc/$git_pid/cmdline ]]; then
    argv=()
    mapfile -d '' -t argv < "/proc/$git_pid/cmdline"
    cleanup="$(git config --default default --get commit.cleanup)" || exit 2
    git_command=
    edited=1
    edit_override=
    merge_options_end=0
    for ((i=1; i<${#argv[@]}; i++)); do
      arg=${argv[i]}
      if [[ -z "$git_command" && "$arg" != -* ]]; then
        git_command=$arg
        if [[ "$git_command" == commit ]]; then
          verbose="$(git config --type=bool-or-int --default 0 --get commit.verbose)" || exit 2
          case "$verbose" in true) verbose=1 ;; false) verbose=0 ;; esac
        elif [[ "$git_command" == merge ]]; then
          # Git exports ':' to hooks when no editor will run. Explicit edit
          # options below still win when ':' is the user's chosen editor.
          [[ "${GIT_EDITOR:-}" != : ]] || edited=0
          branch=$(git symbolic-ref --quiet HEAD) || [[ $? == 1 ]] || exit 2
          if [[ "$branch" == refs/heads/* ]]; then
            merge_options=$(git config --default '' --get "branch.${branch#refs/heads/}.mergeOptions") || exit 2
            # Alias expansion uses Git's own split_cmdline, just like merge.
            # The fixed builtin only quotes data; evaluate ONLY its shell-safe
            # --sq-quote output, never the configuration or parent arguments.
            quoted=$(git -c "alias.olivares-trailer-args=rev-parse --sq-quote $merge_options" olivares-trailer-args) || exit 2
            eval "merge_argv=($quoted)"
            argv=("${argv[@]:0:i+1}" "${merge_argv[@]}" "${argv[@]:i+1}")
            merge_options_end=$((i+1+${#merge_argv[@]}))
          fi
        fi
        continue
      fi
      if [[ ( "$git_command" == commit || "$git_command" == merge ) && "$arg" == --?* ]]; then
        # Git permits unique long-option abbreviations. Resolve them before
        # skipping values, so --mes '--cleanup=strip' remains message text.
        name=${arg%%=*}
        matches=()
        options='cleanup message file reuse-message reedit-message fixup squash template author date trailer pathspec-from-file quiet verbose reset-author signoff edit status gpg-sign all include interactive patch only no-verify dry-run short branch ahead-behind porcelain long null amend allow-empty allow-empty-message no-post-rewrite untracked-files pathspec-file-nul no-edit no-status no-gpg-sign no-signoff no-verbose no-ahead-behind'
        if [[ "$git_command" == merge ]]; then
          options='cleanup message file edit no-edit stat no-stat summary no-summary log no-log squash no-squash commit no-commit ff no-ff ff-only rerere-autoupdate no-rerere-autoupdate verify-signatures no-verify-signatures strategy strategy-option into-name quiet verbose no-verbose abort quit continue allow-unrelated-histories progress no-progress gpg-sign no-gpg-sign autostash no-autostash overwrite-ignore no-overwrite-ignore signoff no-signoff no-verify verify'
        fi
        for name_option in $options; do
          if [[ "--$name_option" == "$name" ]]; then matches=("$name_option"); break; fi
          [[ "--$name_option" != "$name"* ]] || matches+=("$name_option")
        done
        # Git has already validated this invocation. An unrelated option,
        # including an automatically supported --no-* form, does not change
        # cleanup. Resolve recognized options and leave other flags alone.
        if [[ ${#matches[@]} == 1 ]]; then
          arg="--${matches[0]}${arg#"$name"}"
        fi
      fi
      case "$arg" in
        --)
          # Config and CLI are parsed separately by Git; a config terminator
          # must not hide command-line cleanup or edit overrides.
          if ((i < merge_options_end)); then i=$((merge_options_end-1)); continue; fi
          break ;;
        --cleanup=*) cleanup=${arg#*=} ;;
        --cleanup) i=$((i+1)); cleanup=${argv[i]:-unknown} ;;
        --edit) edit_override=1 ;;
        --no-edit) edit_override=0 ;;
        --verbose) [[ "$git_command" != commit ]] || verbose=1 ;;
        --no-verbose) verbose=0 ;;
        --message|--file|--reuse-message) edited=0; i=$((i+1)) ;;
        --message=*|--file=*|--reuse-message=*) edited=0 ;;
        --reedit-message) edited=1; i=$((i+1)) ;;
        --reedit-message=*) edited=1 ;;
        --fixup|--fixup=*)
          if [[ "$arg" == --fixup ]]; then i=$((i+1)); value=${argv[i]:-}; else value=${arg#*=}; fi
          edited=0
          [[ "$value" != amend:* && "$value" != reword:* ]] || edited=1 ;;
        --strategy|--strategy-option|--into-name|--template|--author|--date|--trailer|--squash|--pathspec-from-file|--git-dir|--work-tree|--namespace|--config-env)
          [[ "$git_command" == merge && "$arg" == --squash ]] || i=$((i+1)) ;;
        --*) ;;
        -*)
          # Short options may be clustered; a value consumes the rest of the
          # token (or the next token), including option-looking message text.
          short=${arg#-}
          while [[ -n "$short" ]]; do
            option=${short:0:1}; short=${short:1}
            case "$option" in
              e) edit_override=1 ;;
              v) [[ "$git_command" != commit ]] || verbose=1 ;;
              s|X)
                if [[ "$git_command" == merge ]]; then
                  [[ -n "$short" ]] || i=$((i+1)); break
                fi ;;
              m|F|C|c|t)
                if [[ "$git_command" == commit || "$git_command" == merge ]]; then
                  case "$option" in m|F|C) edited=0 ;; c) edited=1 ;; esac
                fi
                [[ -n "$short" ]] || i=$((i+1)); break ;;
              S|u) break ;; # optional values must be attached
            esac
          done ;;
      esac
    done
    [[ -z "$edit_override" ]] || edited=$edit_override
    if [[ "$cleanup" == default ]]; then
      cleanup=whitespace
      [[ "$edited" == 0 ]] || cleanup=strip
    fi
    [[ "$cleanup" != scissors || "$edited" == 1 ]] || cleanup=whitespace
    # An alias or a different Git operation may have its own option semantics.
    # Never interpret an unrecognized invocation as a commit or merge.
    [[ "$git_command" == commit || "$git_command" == merge ]] || cleanup=unknown
  fi
fi
# Keep Git's selected comment character independently of trailer config.
comment_char="$(git config --default '#' --get core.commentChar)" || exit 2
# Unlike commit, merge does not select an automatic marker (Git 2.39).
if [[ "$comment_char" == auto && "$git_command" == merge ]]; then comment_char='#'; fi
if [[ "$comment_char" == auto && -n "$git_pid" ]]; then
  # Git selects auto before editing and does not export the selected character.
  # Prefer its complete C-locale editor hint or scissors marker. With no
  # footer, use Git's first-unused-character rule (before any editor changes).
  comment_char="$(awk -v quote="'" '
    {
      char = substr($0, 1, 1)
      seen[char] = 1
      if (char ~ /^[#;@!$%^&|:]$/ &&
          previous == char " Please enter the commit message for your changes. Lines starting" &&
          (index($0, char " with " quote char quote " will be ignored") == 1 ||
           index($0, char " with " quote char quote " will be kept") == 1))
        marker = char
      if (char ~ /^[#;@!$%^&|:]$/ && substr($0, 2) == " ------------------------ >8 ------------------------")
        marker = char
      previous = $0
    }
    END {
      if (marker) { print marker; exit }
      candidates = "#;@!$%^&|:"
      for (i=1; i<=length(candidates); i++) {
        char = substr(candidates, i, 1)
        if (!(char in seen)) { print char; exit }
      }
      exit 1
    }
  ' <<<"$message")" || exit 2
fi
if [[ "$cleanup" == unknown ]]; then
  # The same editor footer can arrive via --edit or --cleanup=verbatim.
  # Do not infer cleanup from it. Preserve its content and keep the existing
  # trailer paragraph recognizable under either cleanup outcome.
  footer_line="$(awk -v char="$comment_char" -v quote="'" '
    $0 == char " ------------------------ >8 ------------------------" { if (!footer) footer=NR; exit }
    previous == char " Please enter the commit message for your changes. Lines starting" &&
        (index($0, char " with " quote char quote " will be ignored") == 1 ||
         index($0, char " with " quote char quote " will be kept") == 1) { footer=NR-1; invalid=0 }
    previous == char " Please enter a commit message to explain why this merge is necessary," &&
        $0 == char " especially if it merges an updated upstream into a topic branch." { footer=NR-1; invalid=0 }
    footer && $0 !~ /^[[:space:]]*$/ && index($0, char) != 1 { invalid=1 }
    { previous=$0 }
    END { if (footer && !invalid) print footer }
  ' <<<"$message")" || exit 2
  if [[ -n "$footer_line" ]]; then
    # Validate before moving anything: prose after a sign-off is not a DCO,
    # and a footer must never hide attribution or supply a missing sign-off.
    awk -v footer="$footer_line" 'NR < footer' <<<"$message" | bash "$0" || exit 1
    message="$(awk -v footer="$footer_line" \
        -v cut="$comment_char ------------------------ >8 ------------------------" '
      { lines[NR]=$0 }
      $0 == cut { scissors=1 }
      END {
        if (scissors) {
          # A native patch divider keeps DCO before both the editor appendix
          # and scissors. Preserve every input line even if Git retains it.
          for (i=1; i<=NR; i++) {
            if (i == footer) print "---"
            print lines[i]
          }
          exit
        }
        last=footer-1
        while (last > 0 && lines[last] ~ /^[[:space:]]*$/) last--
        first=last
        while (first > 1 && lines[first-1] !~ /^[[:space:]]*$/) first--
        for (i=1; i<first; i++) print lines[i]
        for (i=footer; i<=NR; i++) print lines[i]
        print ""
        for (i=first; i<=last; i++) print lines[i]
      }
    ' <<<"$message")" || exit 2
  fi
  # Check both sides of a possible scissors cut, with and without comment
  # stripping. Git can combine these transformations (verbose + strip).
  cut_message="$(awk -v cut="$comment_char ------------------------ >8 ------------------------" \
    '$0 == cut { exit } { print }' <<<"$message")" || exit 2
  for candidate in "$message" "$cut_message"; do
    bash "$0" <<<"$candidate" || exit 1
    (
      unset GIT_CONFIG_PARAMETERS GIT_COMMON_DIR
      export GIT_CONFIG_COUNT=0 GIT_CONFIG_NOSYSTEM=1 GIT_CONFIG_GLOBAL=/dev/null
      git --git-dir=/dev/null -c core.commentChar="$comment_char" stripspace --strip-comments <<<"$candidate"
    ) | bash "$0" || exit 1
  done
  # Only a fully validated normalization may update the hook input.
  if [[ -n "$footer_line" ]]; then
    printf '%s\n' "$message" > "$1" || exit 2
  fi
  exit 0
fi
# Git truncates verbose diffs and scissors before whitespace/comment cleanup.
# Both policy checks must see this same effective message, including attribution.
if [[ "$verbose" != 0 || "$cleanup" == scissors ]]; then
  message="$(awk -v cut="$comment_char ------------------------ >8 ------------------------" \
    '$0 == cut { exit } { print }' <<<"$message")" || exit 2
fi
message="$(
  unset GIT_CONFIG_PARAMETERS GIT_COMMON_DIR
  export GIT_CONFIG_COUNT=0 GIT_CONFIG_NOSYSTEM=1 GIT_CONFIG_GLOBAL=/dev/null
  if [[ "$cleanup" == strip ]]; then
    git --git-dir=/dev/null -c core.commentChar="$comment_char" stripspace --strip-comments
  elif [[ "$cleanup" == verbatim ]]; then
    cat
  else
    git --git-dir=/dev/null stripspace
  fi <<<"$message"
)" || exit 2
# Scan all lines, not only the final trailer block: a misplaced attribution
# still travels with the commit. Do not print the value (it may be private).
if ! awk '
  tolower($0) ~ /^[[:space:]]*(co-(authored|developed)-by|assisted-by|generated-(by|with))[[:space:]]*:/ { bad=1 }
  END { exit bad }
' <<<"$message"; then
  echo 'commit-trailers: attribution trailers are refused; remove them and keep the DCO sign-off.' >&2
  exit 1
fi
trailers="$(
  unset GIT_CONFIG_PARAMETERS GIT_COMMON_DIR
  export GIT_CONFIG_COUNT=0 GIT_CONFIG_NOSYSTEM=1 GIT_CONFIG_GLOBAL=/dev/null
  # Match Git's native patch boundary before parsing. Git 2.39 can loop when
  # the ignored patch contains a scissors line as well as another divider.
  awk '/^---([[:space:]]|$)/ { exit } { print }' <<<"$message" |
    git --git-dir=/dev/null interpret-trailers --parse
)" || exit 2
if ! awk '
  tolower($0) ~ /^signed-off-by:[[:space:]]+[^<>]+[[:space:]]+<[^<>[:space:]@]+@[^<>[:space:]@]+>[[:space:]]*$/ { signed=1 }
  END { exit !signed }
' <<<"$trailers"; then
  echo 'commit-trailers: a Signed-off-by: Name <email> DCO trailer is required; use git commit -s.' >&2
  exit 1
fi
