# Running PostelMD v0.1

The runbook for one person running the v0.1 MVP with the README's `docker run`, on Linux or
WSL2. It covers backups, logs, upgrades and the things that can go wrong. Every command
assumes the README's setup: the container is called `postelmd`, its data directory is
`~/postelmd` on the host (after a restore, the folder the restore made: see
[Paths after a restore](#paths-after-a-restore)), and it runs as your user
(`--user "$(id -u):$(id -g)"`).

Set `IMAGE` once to the image you run, and use it below. Until the v0.1.0 release, that is
the newest release candidate (for testing, not for production):

```sh
IMAGE=ghcr.io/joaoheytor/postelmd:0.1.0-rc.1
```

After the v0.1.0 release, use `docker.io/joaoheytor/postelmd:0.1`. The candidate built from
every merge, `ghcr.io/joaoheytor/postelmd:main`, moves with each one. (The blocks in this
runbook have no trailing comments: zsh, by default, would take them as arguments.)

Note the digest of the image the container runs, so you can go back to it:

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

## Where your data is

The data folder is whatever the container mounts at `/data`: `~/postelmd` until your first
restore, a `~/postelmd-restored-…` folder after one. Run this in each new terminal; the
sections below use `"$data"` (the data folder), `"$notes"` (an adopted notes folder, empty
otherwise) and `"$vault"` (the folder that holds your notes and their `.git`):

```sh
data=$(docker inspect -f '{{range .Mounts}}{{if eq .Destination "/data"}}{{.Source}}{{end}}{{end}}' postelmd)
notes=$(docker inspect -f '{{range .Mounts}}{{if eq .Destination "/data/vaults/notes"}}{{.Source}}{{end}}{{end}}' postelmd)
vault=${notes:-$data/vaults/notes}
echo "data=$data notes=$notes vault=$vault"
```

## What to back up

All of the data folder, `~/postelmd` (or the folder the container mounts, after a restore).
Nothing important lives anywhere else:

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

The simplest consistent copy stops the server for a few seconds. It backs up the folders the
container mounts (after a restore, that is no longer `~/postelmd`; see
[Paths after a restore](#paths-after-a-restore)): the data folder to
`~/postelmd-backup-<time>.tar` and, with an adopted notes folder, that folder to
`~/notes-backup-<time>.tar`, with the same time. It checks that each archive reads back, and
prints the sizes:

```sh
data=$(docker inspect -f '{{range .Mounts}}{{if eq .Destination "/data"}}{{.Source}}{{end}}{{end}}' postelmd)
notes=$(docker inspect -f '{{range .Mounts}}{{if eq .Destination "/data/vaults/notes"}}{{.Source}}{{end}}{{end}}' postelmd)
s=$(date +%F-%H%M%S)
f=~/postelmd-backup-$s.tar
g=~/notes-backup-$s.tar
echo "backing up $data to $f${notes:+, and $notes to $g}"
[ -n "$data" ] && docker stop postelmd && tar -C "$data" -cpf "$f" . && tar -tf "$f" >/dev/null \
  && { [ -z "$notes" ] || { tar -C "$notes" -cpf "$g" . && tar -tf "$g" >/dev/null; }; } \
  && du -h ~/*-backup-$s.tar || echo "BACKUP FAILED" >&2
docker start postelmd
docker ps --filter name=postelmd
```

Run the `docker start` always, even when the backup failed, and check that `docker ps` says
"Up" under STATUS. `docker start` is a separate command on purpose: chained after `tar` with `&&`, a failed
backup would leave the server stopped, and `--restart unless-stopped` never restarts a
container you stopped yourself. `docker stop` waits for in-flight saves (up to the
`--stop-timeout 60` you ran it with). The browser keeps unsaved edits as drafts and retries,
so a short stop loses nothing. `tar -C … .` includes the hidden entries PostelMD needs
(`.postelmd/format`, `.postelmd-tmp/.created-by-postelmd`, `.postelmd-trash`).

A backup restores only with the same image or a newer one: `.postelmd/format` records the
data format, and an older build refuses data a newer one wrote
([ADR-0025](../adr/0025-data-format-version.md)).

**On WSL2, copy the backup off the WSL disk.** Your WSL home lives in one virtual disk file;
a backup next to the data is lost with it. A tar file is safe on the Windows side (only the
live data must stay on the Linux filesystem). Any Windows folder works; better, one that is
itself backed up. In the same terminal as the backup:

```sh
mkdir -p /mnt/c/postelmd-backups
cp ~/*-backup-$s.tar /mnt/c/postelmd-backups/
```

For a nightly copy, point restic or borg at the folder the container mounts. The files belong to your user
(because of `--user`), so the backup tool needs no root. A copy taken while the server runs
is usually fine for the notes, but stop the container first if you want a guaranteed
consistent `.git`.

**A named volume** (Docker Desktop on macOS, or `-v postelmd-data:/data`) isn't a folder you
can tar from the host, and the image has no shell. Use a helper container (it runs as root,
hence the `chown`), and start the server again even when the copy failed:

```sh
f=postelmd-backup-$(date +%F-%H%M%S).tar
docker stop postelmd
docker run --rm -v postelmd-data:/data:ro -v "$HOME:/backup" busybox:1.37 \
  tar -C /data -cpf "/backup/$f" .
docker start postelmd
docker ps --filter name=postelmd
sudo chown "$(id -u):$(id -g)" ~/"$f" && tar -tf ~/"$f" >/dev/null && du -h ~/"$f"
```

## Restore drill

Do this once at the start of the four weeks, and after any change to how you back up. It
proves the backup is complete without touching the running instance. It extracts the backup
into `~/postelmd-drill` (and its notes archive, if there is one, into `~/notes-drill`), and
starts a second container on it, on port 8081, with the image the server runs unless you set
`IMAGE`. `ls -t ~/*-backup-*.tar | head` lists the newest backups:

```sh
b=~/postelmd-backup-YYYY-MM-DD-HHMMSS.tar
n=${b/postelmd-backup/notes-backup}
IMAGE=${IMAGE:-$(docker inspect -f '{{.Config.Image}}' postelmd)}
v=(-v ~/postelmd-drill:/data)
[ ! -e "$n" ] || v+=(-v ~/notes-drill:/data/vaults/notes)
mkdir ~/postelmd-drill && tar -C ~/postelmd-drill -xpf "$b" \
  && { [ ! -e "$n" ] || { mkdir ~/notes-drill && tar -C ~/notes-drill -xpf "$n"; }; } \
  && docker run -d --name postelmd-drill -p 127.0.0.1:8081:8080 --user "$(id -u):$(id -g)" \
    "${v[@]}" "$IMAGE"
```

Open http://127.0.0.1:8081 **in a private window** (both instances are on `127.0.0.1`, and
browsers don't separate cookies by port, so logging in to one would log you out of the
other). Log in with your usual password, open a note, check the history of a note you
committed. Then remove it:

```sh
docker rm -f postelmd-drill && rm -rf ~/postelmd-drill ~/notes-drill
```

## Restore from a backup

**Never move, mark or delete the folder the server is using: restore into a new folder and
point the container at it.**

The live folder is whatever the running container mounts ([Where your data is](#where-your-data-is)).
Each restore and rollback appends one line to `~/postelmd-restores.log`:
`<time> <port> <image ID> <previous data> <previous notes> -> <new data> <new notes>` (`-` for
no notes folder; folder paths with spaces aren't supported). The image ID is the exact build
that served the previous folder. This is a manual procedure until
[the planned restore command](../adr/0017-backup-and-restore.md) (v0.3).

1. **Check the backup first** (optional, recommended): run the [Restore drill](#restore-drill)
   on it, and remove its container before step 2.
2. **Restore.** Set `b` to the backup (`ls -t ~/*-backup-*.tar | head`) and run the block.
   With an adopted notes folder it also restores `~/notes-backup-<same time>.tar`. Before it
   changes anything, it reads the served folders, the `-p` address and the image from the
   running container, and refuses if one is missing or the drill's container still runs.
   Then it extracts into `~/postelmd-restored-<time>` (and `~/notes-restored-<time>`): a
   failed or interrupted `tar` leaves the server untouched; delete the partial folder and
   retry. Last, it logs the switch, then stops, removes and recreates the container on the
   new folders with the README's flags and your `-p` (if you started it with other flags,
   edit the `docker run` line in the block). **Never `docker start` the old container after
   changing folders**: it would serve the old folder again. If its `docker run` fails with
   "port is already allocated" (Docker Desktop can hold a host port for a few seconds after
   the old container is removed), wait a few seconds and run the rollback below.

   ```sh
   b=~/postelmd-backup-YYYY-MM-DD-HHMMSS.tar
   n=${b/postelmd-backup/notes-backup}
   t=$(date +%F-%H%M%S)
   prev=$(docker inspect -f '{{range .Mounts}}{{if eq .Destination "/data"}}{{.Source}}{{end}}{{end}}' postelmd)
   prev_notes=$(docker inspect -f '{{range .Mounts}}{{if eq .Destination "/data/vaults/notes"}}{{.Source}}{{end}}{{end}}' postelmd)
   port=$(docker port postelmd 8080/tcp | head -n 1)
   img=$(docker inspect -f '{{.Image}}' postelmd)
   IMAGE=${IMAGE:-$img}
   new=~/postelmd-restored-$t new_notes=-
   [ -z "$prev_notes" ] || new_notes=~/notes-restored-$t
   v=(-v "$new":/data)
   [ "$new_notes" = - ] || v+=(-v "$new_notes":/data/vaults/notes)
   echo "prev=$prev ${prev_notes:+prev_notes=$prev_notes }port=$port IMAGE=$IMAGE"
   if docker inspect postelmd-drill >/dev/null 2>&1; then
     echo "RESTORE STOPPED: remove the drill first: docker rm -f postelmd-drill" >&2
   elif [ -z "$prev" ] || [ -z "$port" ] || [ -z "$IMAGE" ]; then
     echo "RESTORE STOPPED: nothing changed; is the container running?" >&2
   elif ! { mkdir "$new" && tar -C "$new" -xpf "$b" \
       && { [ "$new_notes" = - ] || { mkdir "$new_notes" && tar -C "$new_notes" -xpf "$n"; }; }; }; then
     echo "RESTORE STOPPED: the server is untouched; delete ~/*-restored-$t and retry" >&2
   else
     echo "$t $port $img $prev ${prev_notes:--} -> $new $new_notes" >> ~/postelmd-restores.log \
       && docker stop postelmd \
       && docker logs postelmd > ~/postelmd-logs-$t.jsonl 2>&1 \
       && docker rm postelmd \
       && docker run -d --name postelmd --restart unless-stopped --stop-timeout 60 \
         --log-opt max-size=10m --log-opt max-file=3 \
         -p "$port":8080 --user "$(id -u):$(id -g)" "${v[@]}" "$IMAGE" \
       && touch ~/postelmd-switched-$t \
       && echo "now serving $new; point git, your IDE and backup jobs at it" \
       || echo "RESTORE STOPPED: if the server is down, roll back (below)" >&2
   fi
   ```
3. **Check** in the web app (log in again if asked, with the password you had when the
   backup was taken): a note you saved after the backup is missing, and the notes you know
   the backup has are there.

**Rollback.** The previous folder was never touched, so going back is starting the container
on it again, with the build that served it. The block takes the log's last line and shows it.
It refuses unless the server serves that line's new folder, or there is no container because
that switch never finished (step 2 failed after `docker rm`). It logs itself, so running it
again undoes it, and it works in a new terminal. That includes a rollback whose own
`docker run` failed: running it again starts the server on the restored folder instead of
retrying, and a third run gets back to the previous one. No data moves either way.

```sh
read -r t port img prev prev_notes x new new_notes <<< "$(tail -n 1 ~/postelmd-restores.log)"
live=$(docker inspect -f '{{range .Mounts}}{{if eq .Destination "/data"}}{{.Source}}{{end}}{{end}}' postelmd 2>/dev/null)
echo "last switch $t: $prev $prev_notes -> $new $new_notes; served now: ${live:-no container}"
v=(-v "$prev":/data)
[ "$prev_notes" = - ] || v+=(-v "$prev_notes":/data/vaults/notes)
if [ -z "$live" ] && [ -e ~/postelmd-switched-$t ]; then
  echo "ROLLBACK STOPPED: the last restore finished; start the server with the README's docker run on $new ${new_notes#-}" >&2
elif [ -n "$live" ] && [ "$live" != "$new" ]; then
  echo "ROLLBACK STOPPED: the server doesn't serve $new, the last switch's new folder" >&2
elif [ ! -d "$prev" ] || { [ "$prev_notes" != - ] && [ ! -d "$prev_notes" ]; }; then
  echo "ROLLBACK STOPPED: $prev or $prev_notes is missing" >&2
else
  s=$(date +%F-%H%M%S)
  cur=$(docker inspect -f '{{.Image}}' postelmd 2>/dev/null)
  echo "$s $port ${cur:-$img} $new $new_notes -> $prev $prev_notes" >> ~/postelmd-restores.log \
    && { [ -z "$live" ] || { docker stop postelmd \
      && docker logs postelmd > ~/postelmd-logs-$s.jsonl 2>&1 && docker rm postelmd; }; } \
    && docker run -d --name postelmd --restart unless-stopped --stop-timeout 60 \
      --log-opt max-size=10m --log-opt max-file=3 \
      -p "$port":8080 --user "$(id -u):$(id -g)" "${v[@]}" "$img" \
    && touch ~/postelmd-switched-$s \
    || echo "ROLLBACK FAILED: see the error above" >&2
fi
```

**Previous folders.** Keep them: they cost only disk, and v0.1 has no cleanup command (the
planned restore command will handle it, ADR-0017, v0.3). Before you delete one by hand:

- take a backup of the current data;
- check that it isn't a folder `docker inspect` reports (see
  [Where your data is](#where-your-data-is));
- check for anything written there after the switch: `find <old folder> -newer
  ~/postelmd-switched-<time> | head` prints nothing when it's untouched. `<time>` is the
  first field of the log line that switched away from that folder (the last line, after a
  single restore);
- bring over commits made after the backup, which the restored vault lacks. Fetch the old
  vault's branches, list what the new one is missing, and merge what you want before you
  delete anything:

```sh
git -C <new vault> fetch <old vault> 'refs/heads/*:refs/remotes/before-restore/*'
git -C <new vault> log --oneline --remotes=before-restore --not HEAD
```

Without the refspec, `fetch` keeps only the old HEAD in `FETCH_HEAD`, on no branch, where
git can garbage-collect it. Then `git -C <new vault> merge before-restore/<branch>`.

### Paths after a restore

After a restore your data lives at the new path (`~/postelmd-restored-…`), on purpose: the
old folder is never moved, so a rollback is always one `docker run` away. The commands in
this runbook take the path from `docker inspect` ([Where your data is](#where-your-data-is)),
so they follow it. Anything else that points at the old path must be pointed at the new one:

- restic, borg or cron jobs, and shell aliases;
- an IDE or editor. On Windows, one that opened the notes through
  `\\wsl$\<distribution>\home\<you>\postelmd\vaults\notes` must open
  `\\wsl$\<distribution>\home\<you>\postelmd-restored-…\vaults\notes` instead (or the
  `notes-restored-…` folder, with an adopted folder);
- a git clone of the vault elsewhere: `git remote set-url origin <new vault path>`.

The restored vault's `.git` came with the backup, so git works at the new path, at the
backup's commit. Commits made after the backup are only in the old folder (or a remote you
pushed to).
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
`"$data"/.postelmd/config.yaml` (`allowed_hosts: [notes.example.org]`; `$data` from
[Where your data is](#where-your-data-is)) or
`POSTELMD_ALLOWED_HOSTS`, and restart. Until then that name gets `421 Misdirected Request`
and the log a `WARN` naming the setting; `localhost` and IP addresses always work. Give
PostelMD its own host name: its session cookie is scoped to the host. Behind a TLS proxy,
see v0.2's trusted-proxy setting.

## Upgrade and rollback

v0.1 has no data migrations: any v0.1 build reads the same data folder. The data's format
is recorded in `"$data"/.postelmd/format` (`$data` from
[Where your data is](#where-your-data-is); v0.1 writes `1` at its first start; ADR-0025).
A later release that changes the format raises that number, and an older build then refuses
to start with `this data was written by a newer PostelMD (format N); run that version or
newer`, before it changes anything. So a **rollback across a format change** means restoring
the backup you took before the upgrade, not only the older image; within the same format,
the digest swap below is enough. Never edit `format` to get past the check.

If PostelMD says `format` is **damaged** (anything but a number on one line), back up
the data folder first. If only v0.1 ever used it, delete `"$data"/.postelmd/format` and start
again: the next start writes `1`. Otherwise restore the backup you took before the last
upgrade.

If PostelMD stops with **`.postelmd-trash` (or `.postelmd-tmp`) in your notes folder isn't
empty**, it found its own folders without its marker file. That happens after upgrading
from a build before 0.1.0-rc.1, after deleting `.postelmd-tmp` by hand, or after
`git clean -X` in the notes folder. If PostelMD made those folders, run this (`$vault` from
[Where your data is](#where-your-data-is)) and start again:

```sh
mkdir -p "$vault/.postelmd-tmp" && touch "$vault/.postelmd-tmp/.created-by-postelmd"
```

Don't rename PostelMD's own `.postelmd-trash`: the deleted notes in it would become ordinary
notes, shown and committed. Rename a folder only if it's really yours (ADR-0027).

To upgrade, set `IMAGE` to the new image (see the top of this runbook), then run the block
below. Its first command notes the digest the container runs now (not `$IMAGE`'s: that is the
new one). The next take the data folder and the `-p` host address from the container before
it is removed, so the new one keeps them; use the other flags you started with. It refuses
before stopping anything if one of them, or `$IMAGE`, is empty. `docker stop` waits up to
60 s for saves:

```sh
docker image inspect -f '{{index .RepoDigests 0}}' "$(docker inspect -f '{{.Image}}' postelmd)"
data=$(docker inspect -f '{{range .Mounts}}{{if eq .Destination "/data"}}{{.Source}}{{end}}{{end}}' postelmd)
port=$(docker port postelmd 8080/tcp | head -n 1)
echo "data=$data port=$port IMAGE=$IMAGE"
[ -n "$data" ] && [ -n "$port" ] && [ -n "$IMAGE" ] && docker pull "$IMAGE" && docker stop postelmd \
  && docker logs postelmd > ~/postelmd-logs-$(date +%F-%H%M%S).jsonl 2>&1 \
  && docker rm postelmd \
  && docker run -d --name postelmd --restart unless-stopped --stop-timeout 60 \
    --log-opt max-size=10m --log-opt max-file=3 \
    -p "$port":8080 --user "$(id -u):$(id -g)" -v "$data":/data "$IMAGE" \
  || echo "UPGRADE STOPPED" >&2
```

With an adopted notes folder, instead:

```sh
docker image inspect -f '{{index .RepoDigests 0}}' "$(docker inspect -f '{{.Image}}' postelmd)"
data=$(docker inspect -f '{{range .Mounts}}{{if eq .Destination "/data"}}{{.Source}}{{end}}{{end}}' postelmd)
notes=$(docker inspect -f '{{range .Mounts}}{{if eq .Destination "/data/vaults/notes"}}{{.Source}}{{end}}{{end}}' postelmd)
port=$(docker port postelmd 8080/tcp | head -n 1)
echo "data=$data notes=$notes port=$port IMAGE=$IMAGE"
[ -n "$data" ] && [ -n "$notes" ] && [ -n "$port" ] && [ -n "$IMAGE" ] && docker pull "$IMAGE" \
  && docker stop postelmd \
  && docker logs postelmd > ~/postelmd-logs-$(date +%F-%H%M%S).jsonl 2>&1 \
  && docker rm postelmd \
  && docker run -d --name postelmd --restart unless-stopped --stop-timeout 60 \
    --log-opt max-size=10m --log-opt max-file=3 \
    -p "$port":8080 --user "$(id -u):$(id -g)" \
    -v "$data":/data -v "$notes":/data/vaults/notes "$IMAGE" \
  || echo "UPGRADE STOPPED" >&2
```

If it stops after `docker rm`, run its `docker run` again by hand with the values it printed.
**Rollback** is the same sequence with the digest you noted
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
data=$(docker inspect -f '{{range .Mounts}}{{if eq .Destination "/data"}}{{.Source}}{{end}}{{end}}' postelmd)
IMAGE=${IMAGE:-$(docker inspect -f '{{.Config.Image}}' postelmd)}
[ -n "$data" ] && [ -n "$IMAGE" ] && docker stop postelmd \
  && docker run --rm -it --user "$(id -u):$(id -g)" -v "$data":/data "$IMAGE" reset-password
docker start postelmd
```

It asks for the new password twice (12 characters or more) and logs out every session.

## "A git lock is blocking commits"

The commit dialog shows this banner when `.git/index.lock` or the branch's ref lock exists.
A crash during a commit can leave one behind; so can a git command you're running on the host
against the vault (`"$vault"`, from [Where your data is](#where-your-data-is)). **Remove the lock** is safe only when no git command is
running there: no open `git commit`, `git rebase` or editor waiting for a commit message, no
IDE with a git integration polling the folder. Check, then tick the box and remove it. If
this prints nothing for the vault, it's safe:

```sh
pgrep -a git
```

`pgrep` only sees Linux processes: it can't see a Windows `git.exe` or an IDE working on the
vault through `\\wsl$`. Close those first.

## Restoring a deleted note

Deleting moves the note or folder into `.postelmd-trash/` in the vault, into a
folder named after the UTC time of the delete, under its own name:
`20261110T143005.123456789Z/runbook.md` (a second delete in the same instant uses a folder
ending in `-1`). Deletes made before this layout are named `20261110T143005.123456789Z-runbook.md`
instead. The trash doesn't record the folder the note was in.

In the web app, **Trash** (at the top) lists what was deleted, newest first; **Restore** asks
where to put the item (the vault root under its own name by default; `ops/` keeps the name in
the `ops` folder) and never overwrites: if the name is taken, type another one
([ADR-0024](../adr/0024-restore-from-the-trash.md)).

Without the web app, move it back by hand under a name that doesn't exist, then reload the
page (`$vault` from [Where your data is](#where-your-data-is)):

```sh
ls -lt "$vault"/.postelmd-trash/*/ | head -20
mv -n "$vault"/.postelmd-trash/20261110T143005.123456789Z/runbook.md "$vault"/runbook.md
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

- **Disk full:** free space on the filesystem holding the data folder (`df -h "$data"`, with
  `$data` from [Where your data is](#where-your-data-is)). The next retry saves.
- **Permission denied:** something changed ownership, usually a `sudo` command or a `docker
  run` without `--user`. Fix it with `sudo chown -R "$(id -u):$(id -g)" "$data"` (and
  `"$notes"`, with an adopted folder).

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
