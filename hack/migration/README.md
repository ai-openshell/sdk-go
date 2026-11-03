# Migration tooling

Scripts that moved the Go SDK out of NVIDIA/OpenShell (`sdk/go`) into this repository.
They stay here so the import is reproducible and the next RFC 0019 move can reuse them.
The migration is tracked in NVIDIA/OpenShell#5031.

| File | Purpose |
|------|---------|
| `extract-history.sh <core-clone> <freeze-sha> <out-dir>` | Filter the `sdk/go` subtree history out of a core clone at the freeze commit. Authors, dates, and trailers are preserved; bare `#N` references are rewritten. |
| `message-rules.txt` | `git filter-repo --replace-message` rules: bare `#N` becomes `NVIDIA/OpenShell#N`. |
| `verify-tree.sh <core-clone> <freeze-sha> <sdk-go-clone> <commit>` | Prove the imported tree is byte-identical to `sdk/go` at the freeze commit, compare commit, author, and sign-off counts, and reject bare references. |
| `port-pr.sh <core-pr-number>` | Port an open NVIDIA/OpenShell PR into this repository with its original commits and authorship (branch `port/<n>`, label `port`). |
| `port-pr-body.md` | PR body template used by `port-pr.sh`. |
| `FREEZE` | The recorded freeze commit (`sha=<sha>`) and the stable core tag at the freeze (`tag=<tag>`). |

## Import procedure

1. Record the freeze commit in `FREEZE`.
2. `extract-history.sh <fresh-core-clone> <freeze-sha> <out-dir>` and note the printed HEAD.
3. `verify-tree.sh <fresh-core-clone> <freeze-sha> <out-dir> HEAD` must exit 0.
4. Push `<out-dir>` `main` as `import/history`, merge it into a branch from `main` with `--no-ff --allow-unrelated-histories`, resolve `README.md` in favor of the SDK's, and open the import PR.
5. Merge the import PR with a merge commit, never squash: the merge commit is what carries the history.

## Porting procedure

1. From the root of a clean clone: `hack/migration/port-pr.sh <core-pr-number>`. It builds `port/<n>` from `origin/main` and prints the PR body. Nothing is pushed yet.
2. On a conflict, resolve it and `git add` the files. Then run `port-pr.sh --fix-paths <n>`, `git am --continue`, and `port-pr.sh --continue <n>`.
3. Review the branch and the body, then run `port-pr.sh --continue --push <n>` to push and open the PR.
4. Port only the top PR of a stack. A PR that contains another PR's commits brings them along.
