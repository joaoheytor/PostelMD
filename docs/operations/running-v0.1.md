# Running PostelMD v0.1

The runbook for one person running the v0.1 MVP with the README's `docker run`, on Linux or
WSL2. It covers backups, logs, upgrades and the things that can go wrong. Every command
assumes the README's setup: the container is called `postelmd`, its data directory is
`~/postelmd` on the host, and it runs as your user (`--user "$(id -u):$(id -g)"`).

Set `IMAGE` once to the image you run, and use it below:

```sh
IMAGE=ghcr.io/joaoheytor/postelmd:main      # candidate from main (signed, not for production)
# IMAGE=ghcr.io/joaoheytor/postelmd:0.1.0-rc.1   # a release candidate, once published
# IMAGE=docker.io/joaoheytor/postelmd:0.1        # after the v0.1.0 release
```

`:main` moves with every merge. Note the digest you run, so you can go back to it:
`docker inspect -f '{{.Image}}' postelmd` (the local image ID) and
`docker image inspect -f '{{index .RepoDigests 0}}' "$IMAGE"` (the registry digest). How to
verify an image's signature: [verify-images](verify-images.md).

## What to back up

All of `~/postelmd`. Nothing important lives anywhere else:

| Path | What it is |
|---|---|
| `vaults/notes/` | Your notes, as `.md` files |
| `vaults/notes/.git/` | The vault's git history (your commits) |
| `vaults/notes/.postelmd-trash/` | Deleted notes and folders (not in git) |
| `.postelmd/` | Credentials (password hash), the session secret, `config.yaml` if you wrote one |

If you adopted a folder with `-v ~/notes:/data/vaults/notes`, back up `~/notes` too: it
holds the notes, `.git` and `.postelmd-trash`, and `~/postelmd` holds the rest.

**Git is not a backup in v0.1.** Commits are manual (there is no auto-commit), nothing is
pushed anywhere, and the trash isn't in git. Anything saved since your last commit, and
everything you deleted, exists only in these files.

## Backup

The simplest consistent copy stops the server for a few seconds:

```sh
docker stop postelmd && tar -C ~/postelmd -cpf ~/postelmd-backup-$(date +%F).tar . && docker start postelmd
```

With an adopted folder mounted at `/data/vaults/notes`, copy it in the same stop:

```sh
docker stop postelmd && tar -C ~/postelmd -cpf ~/postelmd-backup-$(date +%F).tar . \
  && tar -C ~/notes -cpf ~/notes-backup-$(date +%F).tar . && docker start postelmd
```

`docker stop` waits for in-flight saves (up to the `--stop-timeout 60` you ran it with). The
browser keeps unsaved edits as drafts and retries, so a short stop loses nothing.

For a nightly copy, point restic or borg at `~/postelmd`. The files belong to your user
(because of `--user`), so the backup tool needs no root. A copy taken while the server runs
is usually fine for the notes, but stop the container first if you want a guaranteed
consistent `.git`.

**A named volume** (Docker Desktop on macOS, or `-v postelmd-data:/data`) isn't a folder you
can tar from the host, and the image has no shell. Use a helper container:

```sh
docker stop postelmd
docker run --rm -v postelmd-data:/data:ro -v "$HOME:/backup" busybox:1.37 \
  tar -C /data -cpf "/backup/postelmd-backup-$(date +%F).tar" .
docker start postelmd
sudo chown "$(id -u):$(id -g)" ~/postelmd-backup-$(date +%F).tar   # busybox ran as root
```

## Restore drill

Do this once at the start of the four weeks, and after any change to how you back up. It
proves the backup is complete without touching the running instance:

```sh
mkdir ~/postelmd-drill
tar -C ~/postelmd-drill -xpf ~/postelmd-backup-YYYY-MM-DD.tar
docker run -d --name postelmd-drill -p 127.0.0.1:8081:8080 \
  --user "$(id -u):$(id -g)" -v ~/postelmd-drill:/data "$IMAGE"
```

With an adopted folder, restore it too and mount it as the drill's vault:

```sh
mkdir -p ~/postelmd-drill/vaults/notes ~/notes-drill
tar -C ~/notes-drill -xpf ~/notes-backup-YYYY-MM-DD.tar
docker run -d --name postelmd-drill -p 127.0.0.1:8081:8080 --user "$(id -u):$(id -g)" \
  -v ~/postelmd-drill:/data -v ~/notes-drill:/data/vaults/notes "$IMAGE"
```

Open http://127.0.0.1:8081 **in a private window** (both instances are on `127.0.0.1`, and
browsers don't separate cookies by port, so logging in to one would log you out of the
other). Log in with your usual password, open a note, check the history of a note you
committed. Then remove it:

```sh
docker rm -f postelmd-drill && rm -rf ~/postelmd-drill ~/notes-drill
```

A real restore keeps the current data aside instead of deleting it (`rm -rf ~/postelmd/*`
would also miss `.postelmd`):

```sh
docker stop postelmd && mv ~/postelmd ~/postelmd.old && mkdir ~/postelmd \
  && tar -C ~/postelmd -xpf ~/postelmd-backup-YYYY-MM-DD.tar && docker start postelmd
```

For an adopted folder, do the same with `~/notes` and its backup before `docker start`.
Delete `~/postelmd.old` once the restored instance looks right.

## Logs

The logs are JSON lines on the container's stdout. Docker deletes them with the container, so
**export them before every `docker rm`**:

```sh
docker logs postelmd > ~/postelmd-logs-$(date +%F).jsonl 2>&1
```

Every write to the API (PUT, POST, DELETE, PATCH under `/api/v1/`) leaves one line: the
method, the route **pattern** (never the note's path or content), the status and the time it
took. Reads leave none.

```json
{"time":"…","level":"INFO","msg":"request","method":"PUT","route":"PUT /api/v1/notes/{path...}","status":204,"duration_ms":3.1}
{"time":"…","level":"WARN","msg":"request","method":"PUT","route":"PUT /api/v1/notes/{path...}","status":412,"duration_ms":1.4}
```

`INFO` is below 400, `WARN` is 4xx, `ERROR` is 5xx. What counts as a failure for the
four-week test (ADR-0020's success criteria):

- **Any 5xx on a save** (`PUT /api/v1/notes/{path...}`). The `ERROR` line just before it
  (`"msg":"notes API"`) has the cause.
- **A 412 you didn't resolve.** A 412 is a conflict: the note changed since the browser
  loaded it, and the conflict view opened. It's a failure only if you couldn't resolve it or
  lost text. The log has no paths, so check each 412 against your memory of the day.

Count them (the setup lines aren't JSON, hence `fromjson?`):

```sh
f=~/postelmd-logs-$(date +%F).jsonl
jq -cR 'fromjson? | select(.msg == "request" and .status >= 500 and .status != 503)' "$f"
jq -cR 'fromjson? | select(.msg == "request" and .status == 412)' "$f" | wc -l
```

A 503 with `Retry-After` (the vault was busy) is retried by the browser and isn't a failure.

## Health

```sh
docker inspect -f '{{.State.Health.Status}}' postelmd   # healthy, starting or unhealthy
docker ps --filter name=postelmd                         # restarts show in STATUS
```

## Reaching it by a host name

Reaching PostelMD by a host name? Add it to `allowed_hosts` in
`~/postelmd/.postelmd/config.yaml` (`allowed_hosts: [notes.example.org]`) or
`POSTELMD_ALLOWED_HOSTS`, and restart. Until then that name gets `421 Misdirected Request`
and the log a `WARN` naming the setting; `localhost` and IP addresses always work. Give
PostelMD its own host name: its session cookie is scoped to the host. Behind a TLS proxy,
see v0.2's trusted-proxy setting.

## Upgrade and rollback

v0.1 has no data migrations: any v0.1 build reads the same `~/postelmd`. The data's format
is recorded in `~/postelmd/.postelmd/format` (v0.1 writes `1` at its first start; ADR-0025).
A later release that changes the format raises that number, and an older build then refuses
to start with `this data was written by a newer PostelMD (format N); run that version or
newer`, before it changes anything. So a **rollback across a format change** means restoring
the backup you took before the upgrade, not only the older image; within the same format,
the digest swap below is enough. Never edit `format` to get past the check.

```sh
docker image inspect -f '{{index .RepoDigests 0}}' "$IMAGE"   # note the current digest
docker pull "$IMAGE"
docker stop postelmd                                          # waits up to 60 s for saves
docker logs postelmd > ~/postelmd-logs-$(date +%F).jsonl 2>&1
docker rm postelmd
docker run -d --name postelmd --restart unless-stopped --stop-timeout 60 \
  -p 127.0.0.1:8080:8080 --user "$(id -u):$(id -g)" -v ~/postelmd:/data "$IMAGE"
```

Use exactly the flags you started with (add `-v ~/notes:/data/vaults/notes` if you adopted a
folder). **Rollback** is the same sequence with the digest you noted
(`ghcr.io/joaoheytor/postelmd@sha256:…`) in place of `$IMAGE`.

## Setup token lost or expired

Before setup, the server prints a one-time token at every start. It is valid for 30 minutes,
and a restart replaces it:

```sh
docker restart postelmd
docker logs postelmd 2>&1 | grep -F '(valid' | tail -n 1
```

Then open the address the same message prints (http://localhost:8080/setup, or your `-p`
host port) and paste it. After setup no token is printed.

## Forgotten password

`reset-password` needs the server stopped (it takes the same instance lock):

```sh
docker stop postelmd
docker run --rm -it --user "$(id -u):$(id -g)" -v ~/postelmd:/data "$IMAGE" reset-password
docker start postelmd
```

It asks for the new password twice (12 characters or more) and logs out every session.

## "A git lock is blocking commits"

The commit dialog shows this banner when `.git/index.lock` or the branch's ref lock exists.
A crash during a commit can leave one behind; so can a git command you're running on the host
against `~/postelmd/vaults/notes`. **Remove the lock** is safe only when no git command is
running there: no open `git commit`, `git rebase` or editor waiting for a commit message, no
IDE with a git integration polling the folder. Check, then tick the box and remove it:

```sh
pgrep -a git    # nothing for the vault: safe
```

`pgrep` only sees Linux processes: it can't see a Windows `git.exe` or an IDE working on the
vault through `\\wsl$`. Close those first.

## Restoring a deleted note

Deleting moves the note or folder into `~/postelmd/vaults/notes/.postelmd-trash/`, into a
folder named after the UTC time of the delete, under its own name:
`20261110T143005.123456789Z/runbook.md` (a second delete in the same instant uses a folder
ending in `-1`). Deletes made before this layout are named `20261110T143005.123456789Z-runbook.md`
instead. The trash doesn't record the folder the note was in.

In the web app, **Trash** (at the top) lists what was deleted, newest first; **Restore** asks
where to put the item (the vault root under its own name by default; `ops/` keeps the name in
the `ops` folder) and never overwrites: if the name is taken, type another one
([ADR-0024](../adr/0024-restore-from-the-trash.md)).

Without the web app, move it back by hand under a name that doesn't exist, then reload the
page:

```sh
ls -lt ~/postelmd/vaults/notes/.postelmd-trash/*/ | head -20
mv -n ~/postelmd/vaults/notes/.postelmd-trash/20261110T143005.123456789Z/runbook.md \
  ~/postelmd/vaults/notes/runbook.md
```

An empty time-named folder in the trash is what a crash during a delete or a restore leaves:
the note is wherever the trash view and the tree show it, and the folder can be removed. The trash is never emptied
automatically. Delete old entries by hand when you're sure.

## Disk full, or permission errors

Symptom: the note never reaches "Saved"; the browser keeps retrying.
The log has an `ERROR` `request` line with status 500, and before it a `notes API` line whose
`err` says `no space left on device` or `permission denied`.

Your edits are safe meanwhile: the browser keeps them as drafts (IndexedDB) and retries.
Don't close the tab or clear site data until a save succeeds. Then:

- **Disk full:** free space on the filesystem holding `~/postelmd` (`df -h ~/postelmd`). The
  next retry saves.
- **Permission denied:** something changed ownership, usually a `sudo` command or a `docker
  run` without `--user`. Fix it with `sudo chown -R "$(id -u):$(id -g)" ~/postelmd`.

## Commit regularly

v0.1 doesn't commit for you (auto-commit comes in v0.2). Open **Commit…** at the end of a
working session, or whenever you've done something you'd want to undo later. History and
"restore this version" only see committed versions.

## WSL2 and Docker Desktop

- **Keep `~/postelmd` on the Linux filesystem** (your WSL home), never under `/mnt/c`.
  Windows folders reach containers as 9p, which can't rename without replacing and isn't
  safe for the app state; PostelMD refuses to start on it (ADR-0009, ADR-0014). The same goes
  for an adopted notes folder.
- **Autostart.** With Docker Desktop, enable *Start Docker Desktop when you sign in*; the
  container's `--restart unless-stopped` brings PostelMD back. With Docker Engine inside WSL,
  enable systemd (`[boot]` `systemd=true` in `/etc/wsl.conf`) and
  `sudo systemctl enable docker`; the distribution must be running, so open a WSL terminal
  after a reboot.
- **No volume, no data.** PostelMD refuses to start when nothing is mounted at `/data`,
  because the container's own layer is deleted by the next `docker rm` (every upgrade).
  `POSTELMD_ALLOW_EPHEMERAL_DATA=true` overrides it for a throwaway try.
