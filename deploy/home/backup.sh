#!/usr/bin/env bash
# Backs up the database on the home server, keeps a rotation of backups
# within the privacy policy's limit (30 days), and copies them off this
# machine when an offsite bucket is set.
#
#   deploy/home/backup.sh v0.13.0   dump to ~/wera-backups/wera-pre-v0.13.0-<time>.sql.gz, then rotate
#   deploy/home/backup.sh --daily   the daily dump, rotation and offsite copy (run from cron)
#   deploy/home/backup.sh --prune   rotate only
#
# Rotation keeps every backup from the last KEEP_DAILY days (default 7),
# then the newest of each week, and none past BACKUP_DAYS (default 30).
#
# Offsite: set BACKUP_S3_URI (s3://bucket/prefix) and, for an S3-compatible
# store such as Backblaze B2 or Cloudflare R2, BACKUP_S3_ENDPOINT. The
# bucket mirrors the backup folder, deletions included, so it keeps to the
# same limit. Credentials are the AWS CLI's usual ones (AWS_PROFILE or
# AWS_ACCESS_KEY_ID/AWS_SECRET_ACCESS_KEY). These settings can live in
# BACKUP_ENV (default ~/.config/wera/backup.env).
set -euo pipefail

env_file=${BACKUP_ENV:-$HOME/.config/wera/backup.env}
if [[ -f $env_file ]]; then
	# shellcheck source=/dev/null
	source "$env_file"
fi

dir=${BACKUP_DIR:-$HOME/wera-backups}
days=${BACKUP_DAYS:-30}
keep_daily=${KEEP_DAILY:-7}
# A day early, so a daily run never lets a backup pass the limit.
limit_minutes=$(((days - 1) * 1440))

# backups lists "mtime path" lines, newest first.
backups() {
	find "$dir" -maxdepth 1 -name 'wera-*.sql.gz' -printf '%T@ %p\n' | sort -rn
}

newest() {
	backups | head -n 1 | cut -d' ' -f2-
}

dump() {
	local label=$1 file
	mkdir -p "$dir"
	file="$dir/wera-$label-$(date +%Y%m%d-%H%M).sql.gz"
	docker exec wera-postgres pg_dump -U wera -d wera | gzip >"$file.part"
	gzip -t "$file.part"
	mv "$file.part" "$file"
	echo "Saved $file ($(du -h "$file" | cut -f1))"
}

# rotate deletes backups past the limit, and past the daily window all but
# the newest of each week. The newest backup is always kept.
rotate() {
	local now keep line mtime path age_days week
	now=$(date +%s)
	keep=$(newest)
	declare -A weeks=()
	while IFS= read -r line; do
		mtime=${line%% *}
		mtime=${mtime%.*}
		path=${line#* }
		[[ $path == "$keep" ]] && continue
		age_days=$(((now - mtime) / 86400))
		if ((now - mtime > limit_minutes * 60)); then
			rm -- "$path"
			echo "Deleted $(basename "$path"), older than $((days - 1)) days"
		elif ((age_days >= keep_daily)); then
			week=$(date -d "@$mtime" +%G-%V)
			if [[ -n ${weeks[$week]:-} ]]; then
				rm -- "$path"
				echo "Deleted $(basename "$path"), week $week has a newer backup"
			else
				weeks[$week]=1
			fi
		fi
	done < <(backups)
}

offsite() {
	[[ -z ${BACKUP_S3_URI:-} ]] && return 0
	local args=(s3 sync "$dir" "$BACKUP_S3_URI" --exclude '*' --include 'wera-*.sql.gz' --delete --only-show-errors)
	if [[ -n ${BACKUP_S3_ENDPOINT:-} ]]; then
		args+=(--endpoint-url "$BACKUP_S3_ENDPOINT")
	fi
	aws "${args[@]}"
	echo "Copied backups to $BACKUP_S3_URI"
}

case "${1:-}" in
--daily)
	dump daily
	rotate
	offsite
	;;
--prune)
	rotate
	;;
"" | -*)
	echo "usage: $0 <label> | --daily | --prune" >&2
	exit 1
	;;
*)
	dump "pre-$1"
	rotate
	offsite
	;;
esac
