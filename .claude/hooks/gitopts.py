"""Long options the guard lets allowed git subcommands take (codeexec.py).

git accepts any unambiguous prefix of a long option (`git grep --op=CMD` is
--open-files-in-pager), so a denylist can't work. A long option passes only when its name
(before `=`) is exactly on the subcommand's list; anything else, abbreviations included,
asks. Arguments after `--` are paths. fetch, pull and push have their own exact lists
(codeexec.fetch_form, outward.check_push). Left off on purpose: --output, --ext-diff,
--textconv, --open-files-in-pager, --exec, --interactive, --template, --strategy.
"""

DIFFLIKE = set("""
    stat shortstat numstat name-only name-status patch no-patch raw summary check exit-code
    quiet color no-color word-diff word-diff-regex color-words color-moved unified
    ignore-space-change ignore-all-space ignore-blank-lines ignore-cr-at-eol diff-filter
    find-renames no-renames find-copies relative full-index binary abbrev no-abbrev minimal
    histogram patience function-context dirstat compact-summary merge-base no-ext-diff
    no-textconv ignore-submodules cached staged text inter-hunk-context stat-width
    format pretty oneline graph decorate no-decorate all branches tags remotes since until
    after before author committer grep invert-grep all-match regexp-ignore-case
    extended-regexp fixed-strings perl-regexp max-count skip reverse follow first-parent
    no-merges merges abbrev-commit date left-right cherry-pick cherry-mark ancestry-path
    topo-order date-order author-date-order source boundary simplify-by-decoration
    show-signature notes no-notes walk-reflogs no-walk full-history parents children count
    log-size mailmap use-mailmap exclude glob not diff-merges remerge-diff cc expand-tabs
    encoding
""".split())

LONG = {
    "diff": DIFFLIKE,
    "log": DIFFLIKE,
    "show": DIFFLIKE,
    "grep": set("""
        line-number count ignore-case word-regexp invert-match files-with-matches name-only
        files-without-match extended-regexp basic-regexp fixed-strings perl-regexp cached
        untracked no-index recurse-submodules heading break show-function function-context
        context after-context before-context max-depth max-count all-match and or not
        full-name null column only-matching quiet color no-color exclude-standard
        no-exclude-standard threads text
    """.split()),
    "status": set("""
        short branch porcelain long verbose untracked-files ignored ignore-submodules column
        no-column ahead-behind no-ahead-behind renames no-renames find-renames show-stash null
    """.split()),
    "add": set("""
        all no-all update verbose dry-run force intent-to-add refresh ignore-errors
        ignore-missing renormalize chmod sparse ignore-removal
    """.split()),
    "commit": set("""
        signoff message amend no-edit edit all file reuse-message reedit-message fixup squash
        author date allow-empty allow-empty-message cleanup verbose quiet dry-run short
        porcelain status no-status only include gpg-sign no-gpg-sign trailer reset-author
        untracked-files
    """.split()),
    "rebase": set("""
        continue abort skip quit onto autosquash no-autosquash autostash no-autostash
        keep-empty empty update-refs signoff committer-date-is-author-date rebase-merges
        fork-point no-fork-point force-rebase quiet verbose stat no-stat root gpg-sign
    """.split()),
}


def bad_long(sub, args):
    """A reason to ask for a long option not on sub's list, or None (or sub isn't judged)."""
    allowed = LONG.get(sub)
    if allowed is None:
        return None
    for a in args:
        if a == "--":
            break
        if a.startswith("--"):
            name = a[2:].partition("=")[0]
            if name not in allowed:
                return (f"git {sub} --{name} is not on the guard's allowlist (git also takes "
                        "abbreviations, e.g. --op for --open-files-in-pager)")
    return None
