# Zip-extraction audit: all four sites in this tree

Recorded 2026-09-27, 2026-09-28. Four places in this repo extract a zip entry
name into a filesystem path. Two were vulnerable to path traversal and are
fixed (stash#7240). Two are not, for reasons that are not obvious from reading
the code, so the reasoning is written down rather than left to be re-derived.

Re-run the survey with:

```bash
cd ~/code-local/worktrees/m6
rg -n 'zip.OpenReader|zip.NewReader' --glob '*.go' . | rg -v _test.go
```

| Site | Writes an entry name? | Verdict |
|---|---|---|
| `internal/manager/task_import.go` `unzipFile` | `filepath.Join(BaseDir, f.Name)` | **was vulnerable** — fixed |
| `pkg/pkg/store.go` `writeFile` via `pkg/pkg/manager.go` `installPackage` | `filepath.Clean(f.Name)` then `Join` | **was vulnerable** — fixed |
| `internal/manager/task/download_ffmpeg.go` `unzip` | `FileInfo().Name()` | safe, see below |
| `pkg/file/zip.go` | rewrites `f.Name` for charset only | safe, does not extract |

## Why the ffmpeg downloader is safe

This one looks like the other two and is not:

```go
filename := f.FileInfo().Name()
if filename != "ffprobe" && filename != "ffmpeg" &&
   filename != "ffprobe.exe" && filename != "ffmpeg.exe" {
    continue
}
unzippedPath := filepath.Join(s.ConfigDirectory, filename)
```

`FileInfo().Name()` returns the **base name** of the entry, not the entry path.
Verified against a real zip rather than assumed:

```text
entry="../../etc/evil"     FileInfo().Name()="evil"
entry="a/b/ffmpeg"         FileInfo().Name()="ffmpeg"
entry="sub/../ffprobe"     FileInfo().Name()="ffprobe"
```

So the join is always `ConfigDirectory/<basename>`, which cannot escape. The
four-name allowlist is a second barrier: an entry named `../../evil` is skipped
outright before the join, because the basename `evil` is not one of the four.

Worth stating explicitly because the two bugs and this one are the same shape
in the file list, and a future reader auditing by grep would reasonably wonder
why one was changed and two were not.

## Why pkg/file/zip.go is safe

It rewrites `f.Name` to fix the charset of non-UTF8 archives, then hands the
reader to a caller. It performs no extraction and constructs no path from the
name. Its callers are the sites in this table.

## The pattern worth naming

The two bugs were the same mistake in two forms:

- `task_import.go` used `filepath.Join` and believed that cleaned the path.
- `pkg/pkg/manager.go` used `filepath.Clean` and believed that contained it.

Neither `Join` nor `Clean` contains anything. `Join` is lexical and resolves
`..` through the joined path; `Clean` collapses `a/../b` but leaves `../` in
place. Containment is a separate test — a `filepath.Rel` against the base and a
check that the result does not begin with `..` — which is what `fsutil.SafeJoin`
does.

**The rule this yields:** any path built from a name that came out of an
archive must go through `fsutil.SafeJoin`, and the check belongs in the function
that performs the write rather than in the function that parsed the entry, so a
new caller cannot skip it.
