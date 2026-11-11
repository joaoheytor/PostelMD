# Running PostelMD v0.1

The runbook for one person running the v0.1 MVP with the README's `docker run`, on Linux or
WSL2. It covers backups, logs, upgrades and the things that can go wrong. Every command
assumes the README's setup: the container is called `postelmd`, its data directory is
`~/postelmd` on the host, and it runs as your user (`--user "$(id -u):$(id -g)"`).

Set `IMAGE` once to the image you run, and use it below. Until the first release candidate,
that is the candidate from `main` (signed, not for production):

```sh
IMAGE=ghcr.io/joaoheytor/postelmd:main
```

A release candidate, once published, is `ghcr.io/joaoheytor/postelmd:0.1.0-rc.1`; after the
v0.1.0 release, use `docker.io/joaoheytor/postelmd:0.1`. (The blocks in this runbook have no
trailing comments: zsh, by default, would take them as arguments.)

`:main` moves with every merge. Note the digest of the image the container runs, so you can
go back to it:

```sh
docker image inspect -f '{{index .RepoDigests 0}}' "$(docker inspect -f '{{.Image}}' postelmd)"
```

This asks the running container, not `$IMAGE`: after a `docker pull`, or when `$IMAGE` names
a tag you haven't pulled yet, `$IMAGE` is not what runs. For an image you built yourself, the
result depends on Docker's image store: it may print a `postelmd@sha256:…` that no registry
holds (it works only while that image stays on this machine), or fail with `index out of
range` because there is no digest. Either way, keep a tag to go back to instead:
`docker tag postelmd:dev postelmd:before-upgrade`. How to verify an image's signature:
[verify-images](verify-images.md).

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
docker stop postelmd && tar -C ~/postelmd -cpf ~/postelmd-backup-$(date +%F).tar . \
  || echo "BACKUP FAILED" >&2
docker start postelmd
docker ps --filter name=postelmd
```

Run the `docker start` always, even when the backup failed, and check that `docker ps` says
"Up" under STATUS. With an adopted folder mounted at `/data/vaults/notes`, copy it in the same stop:

```sh
docker stop postelmd && tar -C ~/postelmd -cpf ~/postelmd-backup-$(date +%F).tar . \
  && tar -C ~/notes -cpf ~/notes-backup-$(date +%F).tar . || echo "BACKUP FAILED" >&2
docker start postelmd
docker ps --filter name=postelmd
```

`docker start` is a separate command on purpose: chained after `tar` with `&&`, a failed
backup would leave the server stopped, and `--restart unless-stopped` never restarts a
container you stopped yourself. `docker stop` waits for in-flight saves (up to the
`--stop-timeout 60` you ran it with). The browser keeps unsaved edits as drafts and retries,
so a short stop loses nothing.

**On WSL2, copy the backup off the WSL disk.** Your WSL home lives in one virtual disk file;
a backup next to the data is lost with it. A tar file is safe on the Windows side (only the
live data must stay on the Linux filesystem). Any Windows folder works; better, one that is
itself backed up:

```sh
mkdir -p /mnt/c/postelmd-backups
cp ~/postelmd-backup-$(date +%F).tar /mnt/c/postelmd-backups/
```

For a nightly copy, point restic or borg at `~/postelmd`. The files belong to your user
(because of `--user`), so the backup tool needs no root. A copy taken while the server runs
is usually fine for the notes, but stop the container first if you want a guaranteed
consistent `.git`.

**A named volume** (Docker Desktop on macOS, or `-v postelmd-data:/data`) isn't a folder you
can tar from the host, and the image has no shell. Use a helper container (it runs as root,
hence the `chown`), and start the server again even when the copy failed:

```sh
docker stop postelmd
docker run --rm -v postelmd-data:/data:ro -v "$HOME:/backup" busybox:1.37 \
  tar -C /data -cpf "/backup/postelmd-backup-$(date +%F).tar" .
docker start postelmd
docker ps --filter name=postelmd
sudo chown "$(id -u):$(id -g)" ~/postelmd-backup-$(date +%F).tar
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
would also miss `.postelmd`), and **recreates the container**. Never `docker start` the old
container after moving its folder: Docker Desktop keeps the bind mount on the folder it
first mounted, so the container would serve `~/postelmd.old`, and new saves would land
there. The restore moves the current data to `~/postelmd.old` (and `~/notes.old`), and a
rollback moves it back.

**Never delete a `.old` folder that holds a `restore-check` marker** until step 4 says so: it
may be your only current copy.

1. **Leave a marker** in every folder the restore replaces: a fresh token, also kept in
   `~/restore-check.token`, outside the data folders. A folder is your current data only if
   its marker matches that token. Restored data never matches, even when the backup holds a
   marker from an earlier attempt. Step 3 checks the marker in the web app (the note
   `restore-check.md`), and the rollback uses it to tell the two folders apart. Running
   step 1 again writes a new token, which is fine, but it refuses while a `.old` folder
   exists. **README setup:**

   ```sh
   if [ -e ~/postelmd.old ]; then
     echo "STOP: ~/postelmd.old exists. If it holds restore-check, it is your current data: roll back (step 3)." >&2
   else
     date +%s%N > ~/restore-check.token \
       && cp ~/restore-check.token ~/postelmd/vaults/notes/restore-check.md
   fi
   ```

   **Adopted notes folder**, instead of the block above:

   ```sh
   if [ -e ~/postelmd.old ] || [ -e ~/notes.old ]; then
     echo "STOP: ~/postelmd.old or ~/notes.old exists. If it holds restore-check, it is your current data: roll back (step 3)." >&2
   else
     date +%s%N > ~/restore-check.token \
       && cp ~/restore-check.token ~/postelmd/restore-check \
       && cp ~/restore-check.token ~/notes/restore-check.md
   fi
   ```

   The adopted setup's `~/postelmd/restore-check` has no `.md` on purpose: PostelMD refuses
   to start with a Markdown file directly in its data directory.

   Also write down two or three notes you know the backup has.
2. **Restore.** Run **one** of the two blocks below, the one for your setup. Each first checks
   that no `.old` folder exists and that step 1's markers are in place, without touching
   anything if not. Then it stops and removes the container, moves the current data aside,
   extracts the backup, and starts a new container with exactly the flags you started with.
   Every command runs only if the one before it succeeded.

   **README setup** (everything in `~/postelmd`):

   ```sh
   if [ -e ~/postelmd.old ]; then
     echo "STOP: ~/postelmd.old exists. If it holds restore-check, it is your current data: roll back (step 3)." >&2
   elif ! cmp -s ~/restore-check.token ~/postelmd/vaults/notes/restore-check.md; then
     echo "STOP: ~/postelmd has no marker matching ~/restore-check.token: do step 1 first." >&2
   else
     docker stop postelmd \
       && docker logs postelmd > ~/postelmd-logs-$(date +%F-%H%M%S).jsonl 2>&1 \
       && docker rm postelmd \
       && mv -T ~/postelmd ~/postelmd.old && mkdir ~/postelmd \
       && tar -C ~/postelmd -xpf ~/postelmd-backup-YYYY-MM-DD.tar \
       && docker run -d --name postelmd --restart unless-stopped --stop-timeout 60 \
         --log-opt max-size=10m --log-opt max-file=3 \
         -p 127.0.0.1:8080:8080 --user "$(id -u):$(id -g)" -v ~/postelmd:/data "$IMAGE" \
       || echo "RESTORE FAILED: no new container was started. Roll back (step 3)." >&2
   fi
   ```

   **Adopted notes folder** (`-v ~/notes:/data/vaults/notes`), instead of the block above:

   ```sh
   if [ -e ~/postelmd.old ] || [ -e ~/notes.old ]; then
     echo "STOP: ~/postelmd.old or ~/notes.old exists. If it holds restore-check, it is your current data: roll back (step 3)." >&2
   elif ! cmp -s ~/restore-check.token ~/postelmd/restore-check \
     || ! cmp -s ~/restore-check.token ~/notes/restore-check.md; then
     echo "STOP: ~/postelmd or ~/notes has no marker matching ~/restore-check.token: do step 1 first." >&2
   else
     docker stop postelmd \
       && docker logs postelmd > ~/postelmd-logs-$(date +%F-%H%M%S).jsonl 2>&1 \
       && docker rm postelmd \
       && mv -T ~/postelmd ~/postelmd.old && mkdir ~/postelmd \
       && tar -C ~/postelmd -xpf ~/postelmd-backup-YYYY-MM-DD.tar \
       && mv -T ~/notes ~/notes.old && mkdir ~/notes \
       && tar -C ~/notes -xpf ~/notes-backup-YYYY-MM-DD.tar \
       && docker run -d --name postelmd --restart unless-stopped --stop-timeout 60 \
         --log-opt max-size=10m --log-opt max-file=3 \
         -p 127.0.0.1:8080:8080 --user "$(id -u):$(id -g)" \
         -v ~/postelmd:/data -v ~/notes:/data/vaults/notes "$IMAGE" \
       || echo "RESTORE FAILED: no new container was started. Roll back (step 3)." >&2
   fi
   ```

   On success it prints the new container's ID. After `RESTORE FAILED` (a wrong backup name
   gives `tar: …: Cannot open`), the server is down: roll back (step 3) before you try again.
   A `STOP` changed nothing. A `.old` folder whose `restore-check` matches
   `~/restore-check.token` (`cmp ~/restore-check.token ~/postelmd.old/vaults/notes/restore-check.md`,
   or `~/postelmd.old/restore-check` and `~/notes.old/restore-check.md` for an adopted folder)
   is your current data: roll back. One that doesn't match is left from an earlier restore:
   move it to a name without `.old` (`mv -T ~/postelmd.old ~/postelmd-before-YYYY-MM-DD`),
   look inside, and delete it only when you're sure you don't need it.
3. **Check** in the web app (log in again if asked, with the password you had when the
   backup was taken): the note `restore-check.md` must be gone, or hold a different number
   than `cat ~/restore-check.token` prints (a backup taken after an earlier attempt holds an
   old marker), **and** the notes you wrote down must be there. If either is wrong, or step 2
   failed, **roll back** with the block for your setup.

   The rollback checks the markers before it touches the container. A folder whose marker
   matches the token is your current data and stays where it is. Otherwise its `.old` must
   match, or the whole block stops without changing anything (so it never takes the server
   down after a successful restore). The data it replaces is kept as `*.failed-<time>`. When
   nothing needs to move, it only makes sure the server runs. After a failed step 2 there is
   no container, so `docker stop` and `docker rm` say `No such container`.

   **README setup:**

   ```sh
   tok=~/restore-check.token ok=1 move=0
   for p in "$HOME/postelmd:vaults/notes/restore-check.md"; do
     d=${p%%:*} f=${p#*:}
     if cmp -s "$tok" "$d/$f"; then
       echo "$d has the marker: it is your current data, left in place"
     elif cmp -s "$tok" "$d.old/$f"; then
       move=1
     else
       echo "STOP: neither $d nor $d.old has the marker in $tok" >&2; ok=0
     fi
   done
   if [ "$ok" = 1 ] && [ "$move" = 1 ]; then
     docker stop postelmd
     docker logs postelmd > ~/postelmd-logs-$(date +%F-%H%M%S).jsonl 2>&1
     docker rm postelmd
     ! docker inspect postelmd >/dev/null 2>&1 || { echo "STOP: the container still exists" >&2; ok=0; }
     for p in "$HOME/postelmd:vaults/notes/restore-check.md"; do
       d=${p%%:*} f=${p#*:}
       if [ "$ok" = 1 ] && cmp -s "$tok" "$d.old/$f"; then
         { [ ! -e "$d" ] || mv -T "$d" "$d.failed-$(date +%F-%H%M%S)"; } && mv -T "$d.old" "$d" || ok=0
       fi
     done
   fi
   [ "$ok" = 1 ] && { docker start postelmd 2>/dev/null || docker run -d --name postelmd \
     --restart unless-stopped --stop-timeout 60 --log-opt max-size=10m --log-opt max-file=3 \
     -p 127.0.0.1:8080:8080 --user "$(id -u):$(id -g)" -v ~/postelmd:/data "$IMAGE"; } \
     || echo "ROLLBACK STOPPED: see the messages above" >&2
   ```

   **Adopted notes folder**, instead of the block above:

   ```sh
   tok=~/restore-check.token ok=1 move=0
   for p in "$HOME/postelmd:restore-check" "$HOME/notes:restore-check.md"; do
     d=${p%%:*} f=${p#*:}
     if cmp -s "$tok" "$d/$f"; then
       echo "$d has the marker: it is your current data, left in place"
     elif cmp -s "$tok" "$d.old/$f"; then
       move=1
     else
       echo "STOP: neither $d nor $d.old has the marker in $tok" >&2; ok=0
     fi
   done
   if [ "$ok" = 1 ] && [ "$move" = 1 ]; then
     docker stop postelmd
     docker logs postelmd > ~/postelmd-logs-$(date +%F-%H%M%S).jsonl 2>&1
     docker rm postelmd
     ! docker inspect postelmd >/dev/null 2>&1 || { echo "STOP: the container still exists" >&2; ok=0; }
     for p in "$HOME/postelmd:restore-check" "$HOME/notes:restore-check.md"; do
       d=${p%%:*} f=${p#*:}
       if [ "$ok" = 1 ] && cmp -s "$tok" "$d.old/$f"; then
         { [ ! -e "$d" ] || mv -T "$d" "$d.failed-$(date +%F-%H%M%S)"; } && mv -T "$d.old" "$d" || ok=0
       fi
     done
   fi
   [ "$ok" = 1 ] && { docker start postelmd 2>/dev/null || docker run -d --name postelmd \
     --restart unless-stopped --stop-timeout 60 --log-opt max-size=10m --log-opt max-file=3 \
     -p 127.0.0.1:8080:8080 --user "$(id -u):$(id -g)" \
     -v ~/postelmd:/data -v ~/notes:/data/vaults/notes "$IMAGE"; } \
     || echo "ROLLBACK STOPPED: see the messages above" >&2
   ```

   The note `restore-check.md` is listed again, with the token's number. Delete the
   `*.failed-*` folders once you know why the restore failed.
4. Only after the check passes, and only with the recreated container running, delete
   `~/postelmd.old` (and `~/notes.old`), and the note `restore-check.md` if the restored
   data has one.

## Logs

The logs are JSON lines on the container's stdout. Docker deletes them with the container, so
**export them before every `docker rm`**. Docker's default log driver may not rotate; the
README's `docker run` caps it with `--log-opt max-size=10m --log-opt max-file=3`: Docker
keeps the last 30 MB or so, and drops older lines. Export them more often
than that fills up if you want the whole four weeks. The time in the name keeps a second
export on the same day from replacing the first:

```sh
docker logs postelmd > ~/postelmd-logs-$(date +%F-%H%M%S).jsonl 2>&1
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

Count them in the newest export (the setup lines aren't JSON, hence `fromjson?`):

```sh
f=$(ls -t ~/postelmd-logs-*.jsonl | head -n 1)
jq -cR 'fromjson? | select(.msg == "request" and .status >= 500 and .status != 503)' "$f"
jq -cR 'fromjson? | select(.msg == "request" and .status == 412)' "$f" | wc -l
```

A 503 with `Retry-After` (the vault was busy) is retried by the browser and isn't a failure.

## Health

The first command prints `healthy`, `starting` or `unhealthy`; restarts show in the second's
STATUS:

```sh
docker inspect -f '{{.State.Health.Status}}' postelmd
docker ps --filter name=postelmd
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

If PostelMD says `format` is **damaged** (anything but a number on one line), back up
`~/postelmd` first. If only v0.1 ever used it, delete `~/postelmd/.postelmd/format` and start
again: the next start writes `1`. Otherwise restore the backup you took before the last
upgrade.

If PostelMD stops with **`.postelmd-trash` (or `.postelmd-tmp`) in your notes folder isn't
empty**, it found its own folders without its marker file. That happens after upgrading
from a build before 0.1.0-rc.1, after deleting `.postelmd-tmp` by hand, or after
`git clean -X` in the notes folder. If PostelMD made those folders, run this in the notes
folder (`~/postelmd/vaults/notes`, or your own notes folder if you mounted one) and start
again:

```sh
mkdir -p .postelmd-tmp && touch .postelmd-tmp/.created-by-postelmd
```

Don't rename PostelMD's own `.postelmd-trash`: the deleted notes in it would become ordinary
notes, shown and committed. Rename a folder only if it's really yours (ADR-0027).

To upgrade, run the block below. Its first command notes the digest the container runs now
(not `$IMAGE`'s: that is the new one), and `docker stop` waits up to 60 s for saves:

```sh
docker image inspect -f '{{index .RepoDigests 0}}' "$(docker inspect -f '{{.Image}}' postelmd)"
docker pull "$IMAGE"
docker stop postelmd
docker logs postelmd > ~/postelmd-logs-$(date +%F-%H%M%S).jsonl 2>&1
docker rm postelmd
docker run -d --name postelmd --restart unless-stopped --stop-timeout 60 \
  --log-opt max-size=10m --log-opt max-file=3 \
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
IDE with a git integration polling the folder. Check, then tick the box and remove it. If
this prints nothing for the vault, it's safe:

```sh
pgrep -a git
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
