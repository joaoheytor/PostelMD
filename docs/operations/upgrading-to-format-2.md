# Upgrading to data format 2

The runbook for the first release that writes **data format 2** (file IDs; the IDs slice,
ADR-0030, ADR-0031, ADR-0041, ADR-0042), and for going back to 0.1. It assumes the setup of
[Running PostelMD v0.1](running-v0.1.md): a container called `postelmd`, `IMAGE` set to the
image you run, and `$data`, `$notes` and `$vault` from
[Where your data is](running-v0.1.md#where-your-data-is). The blocks have no trailing
comments, and run the same in `sh`, `bash` and `zsh`.

**Until that release, nothing here applies.** A build that writes format 1 (0.1.x, and
`:main` until format 2 lands) leaves `.postelmd/format` at `1`, and refuses `db rebuild`
and `vault claim` with `this command arrives with data format 2`, changing nothing. To check
what the running build writes, start it and read `"$data"/.postelmd/format`: `1` or `2`.

## Before you upgrade

1. Note the digest of the image the container runs
   ([how](running-v0.1.md#running-postelmd-v01)): it is the image you go back to. Set
   `PREV` to it now, in the terminal you will use for a rollback:

   ```sh
   PREV=$(docker image inspect -f '{{index .RepoDigests 0}}' "$(docker inspect -f '{{.Image}}' postelmd)")
   echo "PREV=$PREV"
   ```

2. Take a [backup](running-v0.1.md#backup), server stopped, and keep it until the upgraded
   instance has run for a while. It is the only copy of the data as 0.1 left it: the
   upgrade changes `.postelmd/` and the vault marker, and the rollback below gives back
   your notes and credentials, not the file IDs.
3. Check the free space: the first start writes a row per file into `app.db`, a few hundred
   bytes each (`df -h "$data"`).

Then upgrade with the [upgrade block](running-v0.1.md#upgrade-and-rollback), with `IMAGE`
set to the new image, and the same `--user` as before: the server must run as the user who
owns `.postelmd`.

## What the first start does

On data at format 1 (or with no `format` file, which is read as 1 and not written), the first
start of a format-2 build runs the **first upgrade** before it serves. `/healthz` answers at
once; `/readyz` and every page answer `503` until the upgrade and the first scan are done.
It reads every file of the vault once, so on a large vault it takes a while: let it finish.

Before it changes anything, it checks the vault (below) and writes
`.postelmd/upgrade.intent`, which names the vault UUID it is about to assign. Then:

1. it copies `format` (if there is one), `auth.json`, `secret.key` and `config.yaml` (if
   there is one) into `.postelmd/pre-format-2/`, owner-only, and writes `complete` there
   last, holding `<vault UUID> <time>`. An earlier `pre-format-2/` that isn't this upgrade's
   is moved to `pre-format-2-<time>/` first, never written over;
2. it opens and migrates `app.db` (this happens at every start);
3. in one transaction, it gives every note an ID, imports the trash, and records that IDs
   are assigned;
4. it writes the vault marker (`$vault/.postelmd-tmp/.created-by-postelmd`): the vault UUID
   and format `2`;
5. it writes `2` into `.postelmd/format`, then removes `upgrade.intent`.

If it stops in the middle (a crash, `docker stop`, a full disk), the next start finishes
it from where it stopped: `upgrade.intent` is the record of how far it got. Don't move or
delete `upgrade.intent`, and don't start 0.1 meanwhile.

When it is done, the log has one `first upgrade` record, and the format is `2`:

```sh
docker logs postelmd 2>&1 | grep '"msg":"first upgrade"'
cat "$data/.postelmd/format"
```

The record has the vault UUID, `files` (notes given an ID), `trash` (trash items imported),
`resumed` (it finished an interrupted upgrade) and `duration_ms`. The deleted notes already
in the trash keep their place there. Once PostelMD empties the trash by itself (a later
release, ADR-0033), each of them gets 7 days counted from the upgrade, not from when you
deleted it; until then nothing in the trash is removed automatically.

## If the start refuses

Each refusal changes nothing in the vault: it comes before the vault is opened. Find its
message in `docker logs postelmd`:

| The message says | What it means | Fix |
|---|---|---|
| `the vault marker names a vault UUID, so this vault belongs to a v0.2 instance whose state directory isn't this one` | The notes folder was upgraded by another instance (or rolled back without emptying its marker) | Mount the data folder that belongs to it. After a rollback that skipped the marker step, run [`db rebuild`](running-v0.1.md#database-lost-or-the-vault-marker-doesnt-match) |
| `the vault marker names another vault than this state directory's` | Another notes folder is mounted | Mount the right one; or, to keep this one with new file IDs, `db rebuild` |
| `the first upgrade's files don't match its intent` | Something changed the database or the marker while the upgrade was unfinished | `db rebuild` takes the upgrade over (see [below](#a-pre-format-2-left-by-an-upgrade-db-rebuild-took-over)) |
| `the mounted vault has no vault marker naming this database's vault` | At format 2, the marker is missing or empty and the files don't match the database | Mount the right folder; if it is the right one, [`vault claim`](running-v0.1.md#database-lost-or-the-vault-marker-doesnt-match) |
| `the vault marker … can't be read as PostelMD's` | The marker holds something else, or isn't a regular file | If it isn't a regular file, move it aside; then `vault claim` |
| `there is no app.db in the state directory` | At format 2, the database is gone | If this is the right data folder, `db rebuild` |
| `the database is damaged or isn't a database` | `app.db` is corrupt | `db rebuild` (it moves the old files into `.postelmd/damaged-<time>/`) |
| `the data is at format 2, but app.db has no file IDs` | `app.db` isn't the one the upgrade wrote | `db rebuild` |
| `a db rebuild was interrupted` | `rebuild.intent` is there | Run `db rebuild` again; it finishes |
| `this data was written by a newer PostelMD (format 2)` | 0.1 started on upgraded data | Start the format-2 image again, or [roll back](#rollback-to-01) |

A start never fixes these by itself: a missing database or marker is also what a wrong
mount looks like, and `db rebuild` gives every file a new ID.

## Offline commands run as the state's owner

`db rebuild`, `vault claim`, `db rollback` and `reset-password` run with the server stopped,
in a container of the same image. From format 2 they refuse with `this command must run as
the user who owns /data/.postelmd` when they run as anyone else, root included, and change
nothing: a database or a moved folder they created as another user would be one the server
can't open. The message names the owner's UID and the `--user` to pass.

- A host folder at `/data` (`-v ~/postelmd:/data`): pass `--user "$(id -u):$(id -g)"`, as
  for the server. Don't run them with `sudo`.
- A named volume: run them without `--user`; the image's user owns the volume.

The [`db rebuild` and `vault claim` block](running-v0.1.md#database-lost-or-the-vault-marker-doesnt-match)
already does this. To see who owns the state folder of a named volume (`postelmd-data`
here):

```sh
docker run --rm -v postelmd-data:/data:ro busybox:1.37 stat -c %u:%g /data/.postelmd
```

## Rollback to 0.1

Going back to 0.1 after a finished upgrade, with the format-2 server stopped cleanly (a
`docker stop`, so no operation is open). You keep your notes, the trash, the git history,
and the credentials as they are now: format 2 doesn't change `auth.json` or `secret.key`,
and 0.1 reads them as they are. The rollback never restores the copies in `pre-format-2/`;
for a lost password, use [`reset-password`](running-v0.1.md#forgotten-password).
**Lost:** the file IDs, and with them the ETags of open tabs (they reload) and the
database's records, such as the trash's import times; edits made since the upgrade are
kept, as files. The next start of a format-2 build runs a new first upgrade, with new IDs.

If the upgrade didn't finish (`upgrade.intent` is in `.postelmd`), don't roll back: start
the format-2 image again so it finishes, or restore the backup you took before it.

**With Docker Compose,** set the service's `image:` to `$PREV` and run `docker compose up -d`
after step 2, instead of step 3. For steps 1 and 2, `c` is the container's name
(`docker compose ps`); in step 1, run `docker compose down` in place of its `docker rm`,
and don't run `up` before step 2 printed `rolled back to format 1`.

**1. Note the container's settings, then stop and remove it.** This reads how the container
mounts `/data` (a host folder or a named volume), the notes folder if you adopted one, its
host port and its user, and saves its log. With any value missing, it stops nothing. Set
`c` to your container's name:

```sh
c=postelmd
m=$(docker inspect -f '{{range .Mounts}}{{if eq .Destination "/data"}}{{.Type}} {{.Name}}{{end}}{{end}}' "$c")
data=$(docker inspect -f '{{range .Mounts}}{{if eq .Destination "/data"}}{{.Source}}{{end}}{{end}}' "$c")
notes=$(docker inspect -f '{{range .Mounts}}{{if eq .Destination "/data/vaults/notes"}}{{.Source}}{{end}}{{end}}' "$c")
port=$(docker inspect -f '{{with index .HostConfig.PortBindings "8080/tcp"}}{{with index . 0}}{{if .HostIp}}{{.HostIp}}:{{end}}{{.HostPort}}{{end}}{{end}}' "$c")
case $port in *:*:*) port="[${port%:*}]:${port##*:}" ;; esac
runas=$(docker inspect -f '{{.Config.User}}' "$c")
src=
case "${m%% *}" in
  bind) src=$data ;;
  volume) src=${m#* } ;;
esac
echo "src=$src notes=$notes port=$port runas=$runas PREV=$PREV"
[ -n "$src" ] && [ -n "$port" ] && docker stop "$c" \
  && docker logs "$c" > ~/postelmd-logs-$(date +%F-%H%M%S).jsonl 2>&1 \
  && docker rm "$c" || echo "ROLLBACK STOPPED" >&2
```

If it printed an empty `PREV`, set it to the digest you noted before the upgrade
(`PREV=ghcr.io/joaoheytor/postelmd@sha256:…`) before step 3.

**2. Roll the data back.** This runs in a `busybox` helper container as the owner of
`.postelmd`, so it works the same for a host folder and a named volume, and keeps every
file's owner and mode. It runs only when the container of step 1 no longer exists. It
refuses, changing nothing, unless the data is at format 2 with no operation open, and this
user can reach and write the vault marker and write `.postelmd` (an adopted notes folder
that belongs to another user fails here, before anything moves). Then it:

1. moves `app.db` and the files beside it (`-wal`, `-shm`, `-journal`, the `pre-migrate-*`
   copies and `post-migration-started-*` records; ADR-0043) into
   `.postelmd/rolled-back-<time>/`;
2. moves `pre-format-2/` to `pre-format-2-<time>/`, so the next upgrade takes a new copy;
3. empties the vault marker in place (never delete it: 0.1 refuses a vault whose trash
   isn't empty without it);
4. writes `1` into `.postelmd/format.new` (a copy of `format`, so it keeps its owner and
   mode), syncs, renames it over `format`, and syncs again, last.

Nothing is deleted, and no credential is copied or moved.

```sh
if ! left=$(docker ps -aq --filter "name=^$c\$"); then
  echo "ROLLBACK STOPPED: docker ps failed, so it can't tell whether a server runs" >&2
elif [ -n "$left" ]; then
  echo "ROLLBACK STOPPED: the $c container still exists; run step 1 first" >&2
elif [ -z "$c" ] || [ -z "$src" ]; then
  echo "ROLLBACK STOPPED: \$c or \$src is empty; run step 1 first" >&2
elif ! owner=$(docker run --rm -v "$src":/data:ro busybox:1.37 stat -c %u:%g /data/.postelmd); then
  echo "ROLLBACK STOPPED: can't read the owner of .postelmd" >&2
else
  docker run --rm -i --user "$owner" -v "$src":/data ${notes:+-v} ${notes:+"$notes":/data/vaults/notes} \
    busybox:1.37 sh -s -- /data /data/vaults/notes <<'EOF'
set -u
st=$1/.postelmd
m=$2/.postelmd-tmp/.created-by-postelmd
stop() { echo "ROLLBACK STOPPED: $*" >&2; exit 1; }
[ -d "$st" ] && [ -d "$2" ] || stop "no .postelmd or vault folder in the mounted data"
[ "$(cat "$st/format" 2>/dev/null)" = 2 ] || stop "the data format isn't 2, so there is nothing to roll back; nothing was changed"
for f in upgrade.intent rebuild.intent migrate.intent rollback.intent; do
  if [ -e "$st/$f" ] || [ -L "$st/$f" ]; then
    stop "$f is in .postelmd: an operation didn't finish (see the runbook); nothing was changed"
  fi
done
[ -x "$2/.postelmd-tmp" ] || stop "user $(id -u) can't open the vault's .postelmd-tmp (is the notes folder another user's?); nothing was changed"
[ -f "$m" ] && [ ! -L "$m" ] || stop "the vault marker isn't a regular file; nothing was changed"
[ -w "$m" ] || stop "user $(id -u) can't write the vault marker (is the notes folder another user's?); nothing was changed"
[ -w "$st" ] && [ -w "$st/format" ] || stop "user $(id -u) can't write .postelmd; nothing was changed"
t=$(date +%Y%m%d-%H%M%S)
d=$st/rolled-back-$t
mkdir -m 700 "$d" || stop "can't create $d"
for f in "$st"/app.db "$st"/app.db-wal "$st"/app.db-shm "$st"/app.db-journal "$st"/pre-migrate-* "$st"/post-migration-started-*; do
  [ ! -e "$f" ] || mv "$f" "$d/" || stop "$f didn't move; see $d"
done
[ ! -e "$st/pre-format-2" ] || mv "$st/pre-format-2" "$st/pre-format-2-$t" || stop "pre-format-2 didn't move"
: > "$m" || stop "the vault marker wasn't emptied"
rm -f "$st/format.new" && cp -p "$st/format" "$st/format.new" && printf '1\n' > "$st/format.new" \
  && sync && mv -f "$st/format.new" "$st/format" && sync || stop "the format wasn't written"
echo "rolled back to format 1: the database is in .postelmd/rolled-back-$t; start 0.1 now"
EOF
fi
```

If it stops part way (a full disk), fix the cause and run it again: the format stays `2`
until the last step, and each step is safe to repeat. A crash can't leave `format` half
written: the new one replaces it in one rename. If you still find it empty, or holding
something other than a number (0.1 refuses it as damaged; a disk fault, or a rollback
done by hand that wrote it in place), look at `.postelmd` before anything else:

- `app.db` is still there: the rollback didn't move the database, so the data is at
  format 2. Write `2` (below), then run step 2 again.
- `app.db` is in a `rolled-back-*/` folder and the vault marker is empty: step 2 had reached
  its last step. Write `1` (below), then step 3.
- Anything else: restore the backup you took before the upgrade.

Set `n` to the number, with `src`, `owner` and `c` from step 2 and the server stopped. The
block writes it owner-only (mode 600), as the format file is:

```sh
n=1
[ -n "$src" ] && [ -n "$owner" ] && [ -z "$(docker ps -aq --filter "name=^$c\$")" ] \
  && docker run --rm --user "$owner" -v "$src":/data busybox:1.37 \
    sh -c 'umask 077 && rm -f /data/.postelmd/format.new && printf "%s\n" "$1" > /data/.postelmd/format.new && sync && mv -f /data/.postelmd/format.new /data/.postelmd/format && sync' sh "$n" \
  || echo "FORMAT NOT WRITTEN: src or owner is empty, the container exists, or the write failed" >&2
```

**3. Start 0.1.** Only if step 2 printed `rolled back to format 1`, start the image you
noted, with the settings of step 1 (add any other flags you started it with):

```sh
[ -n "$c" ] && [ -n "$src" ] && [ -n "$port" ] && [ -n "$PREV" ] \
  && docker run -d --name "$c" --restart unless-stopped --stop-timeout 60 \
    --log-opt max-size=10m --log-opt max-file=3 \
    -p "$port":8080 ${runas:+--user} ${runas:+"$runas"} -v "$src":/data \
    ${notes:+-v} ${notes:+"$notes":/data/vaults/notes} "$PREV" \
  || echo "START STOPPED: c, src, port or PREV is empty, or docker run failed" >&2
```

0.1 starts on the notes as files. It creates a new `app.db`; the old one stays in
`rolled-back-<time>/`.

### A pre-format-2 left by an upgrade db rebuild took over

When a first upgrade stopped on interference (`the first upgrade's files don't match its
intent`) and `db rebuild` took it over, the rebuild moved `upgrade.intent` into
`.postelmd/damaged-<time>/` with the old database, and left `pre-format-2/` where it was.
The data is then at format 2 with the rebuilt database, and the rollback above applies as
it is: it moves that `pre-format-2/` aside like any other.

That copy holds `auth.json` and `secret.key` (and `format` and `config.yaml` if 0.1 had
them) as they were when the stopped upgrade began. With `complete` (`<vault UUID> <time>`,
the stopped upgrade's UUID, not the marker's), the copy is whole; without it, it may be
partial. Either way it is only a safety copy: nothing in PostelMD or this runbook reads it
back, and the credentials in use are the ones in `.postelmd`.

## Cleanup

`rolled-back-*/`, `pre-format-2/`, `pre-format-2-*/`, `damaged-*/` and the `pre-migrate-*`
copies are never removed automatically. The blocks below run in a `busybox` helper, so they
work for a host folder and a named volume alike. To list them, with `c` set to your
container's name (the server may run):

```sh
c=postelmd
src=$(docker inspect -f '{{range .Mounts}}{{if eq .Destination "/data"}}{{if eq .Type "volume"}}{{.Name}}{{else}}{{.Source}}{{end}}{{end}}{{end}}' "$c")
echo "src=$src"
[ -n "$src" ] && docker run --rm -v "$src":/data:ro busybox:1.37 \
  find /data/.postelmd -maxdepth 1 \( -name 'rolled-back-*' -o -name 'pre-format-2*' -o -name 'damaged-*' -o -name 'pre-migrate-*' \) -exec du -sh {} +
```

Once the instance has run well for a while and you have a new backup, you may remove them.
`damaged-*/` is the only copy of an old database: move it to your backup storage rather
than delete it. `recovery/rebuild-*/` holds unfinished saves a `db rebuild` kept: copy out
anything you want within 7 days.

**After a password reset or a key compromise,** remove `pre-format-2/` and every
`pre-format-2-*/` (and any backup that holds them). Besides `auth.json` and `secret.key`,
they are the only places that hold credentials: the old password hash and the old
`secret.key`. (`rolled-back-*/` and `damaged-*/` hold only database files, which have no
credentials.) This stops the server, removes them as the owner of `.postelmd`, and starts
it again:

```sh
c=postelmd
src=$(docker inspect -f '{{range .Mounts}}{{if eq .Destination "/data"}}{{if eq .Type "volume"}}{{.Name}}{{else}}{{.Source}}{{end}}{{end}}{{end}}' "$c")
echo "src=$src"
[ -n "$src" ] && docker stop "$c" \
  && owner=$(docker run --rm -v "$src":/data:ro busybox:1.37 stat -c %u:%g /data/.postelmd) \
  && docker run --rm --user "$owner" -v "$src":/data busybox:1.37 \
    find /data/.postelmd -maxdepth 1 -name 'pre-format-2*' -exec rm -r {} + \
  || echo "CLEANUP STOPPED" >&2
docker start "$c"
```

`reset-password` doesn't replace `secret.key`. After a key compromise, replace it: with the
server stopped, move `secret.key` aside; the next start writes a new one, and every session
is logged out.
