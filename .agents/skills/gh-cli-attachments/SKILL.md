---
name: gh-cli-attachments
description: Attach local screenshots, images, or videos to GitHub issues and pull requests using the gh CLI's native --attach flag. Use whenever an issue or PR being created or commented on should include a screenshot, image, or video — bug reports with screenshots, before/after comparisons, visual evidence. Never commit image files to the repository to display them in an issue or PR.
---

# GitHub issue/PR attachments with gh --attach

GitHub's CLI (gh v2.99.0+, released 2026-09) uploads files to the issue or
PR itself with `--attach` and returns `github.com/user-attachments` URLs —
the same kind the web UI produces by drag-and-drop. That is the only way
files should get into an issue or PR from the command line.

## Hard rule

**Never commit an image or other attachment file to any branch of the
repository in order to reference it from an issue or PR.** No screenshot
commits, no "assets" folder or branch in the code repo, no external image
hosts. If `--attach` cannot do the job, stop and say why (see Limits).

## Workflow

1. Check the version once: `gh --version`. If it is older than 2.99.0,
   tell the user to upgrade gh — do not fall back to another upload route.
2. Before uploading, look at each image: no secrets, tokens, private data,
   or unrelated windows. Uploaded URLs inherit the repository's
   visibility — on a public repo, the image is public.
3. Attach on the same command that posts the text:

```sh
gh issue create --title "Checkout button overlaps on mobile" \
  --body-file /tmp/issue.md \
  --attach './before.png#Before' --attach './after.png#After'

gh issue comment 42 --body-file /tmp/note.md --attach './shot.png'
gh pr create --title "Fix header layout" --body-file /tmp/pr.md \
  --attach './ui.png#New header'
```

`--attach` also works on `gh issue edit`, `gh pr edit`, and `gh pr comment`.
Repeat the flag per file; the same file cannot be attached twice. The
`'#Alt text'` suffix sets the alt text — without it, the file name is
used. Videos take no alt text.

4. In the body, reference attached files by their local Markdown path —
   `![Before](./before.png)` — and gh rewrites it to the uploaded URL.
   The body's own alt text wins; a flag's `#alt` applies only to files
   the body never references, which are appended at the end in the order
   the flags were passed. A video reference renders as a player only
   when it is the sole content of its paragraph; inline in a sentence
   it renders as a link.

## Failure handling

A partial upload failure can still create the issue or PR: gh prints its
URL but exits non-zero. **Never blindly retry a create** — that produces a
duplicate. Read the URL from the output, open the created issue/PR, and add
whatever is missing with a comment instead:

```sh
gh issue comment <number> --attach './missing.png'
gh pr comment <number> --attach './missing.png'
```

## Limits — stop and tell the user when one applies

- Images and media only: `.png`, `.gif`, `.jpg`, `.jpeg`, `.svg`,
  `.mp4`, `.mov`, `.webm`. Other files (PDFs, logs, zips) cannot be
  attached this way; say so instead of hosting them somewhere else.
- Size caps: images and GIFs up to 10 MB; videos up to 10 MB on
  free-plan repositories, 100 MB on paid-plan repositories.
- Attaching requires push access to the repository (GitHub.com or GitHub
  Enterprise Cloud; not GitHub Enterprise Server).
- Inside GitHub Actions, `--attach` rejects the workflow `GITHUB_TOKEN`;
  attachments from CI are not possible this way.
