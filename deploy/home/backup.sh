#!/usr/bin/env bash
# Backs up the database on the home server, and deletes backups older than
# the privacy policy allows (30 days).
#
#   deploy/home/backup.sh v0.13.0   dump to ~/wera-backups/wera-pre-v0.13.0-<time>.sql.gz, then prune
#   deploy/home/backup.sh --prune   delete backups past the limit (run daily from cron)
#
# BACKUP_DIR (default ~/wera-backups) and BACKUP_DAYS (default 30) change
# where backups go and how long they stay.
set -euo pipefail

dir=${BACKUP_DIR:-$HOME/wera-backups}
days=${BACKUP_DAYS:-30}
# A day early, so a daily run never lets a backup pass the limit.
limit_minutes=$(((days - 1) * 1440))

newest() {
	find "$dir" -maxdepth 1 -name 'wera-*.sql.gz' -printf '%T@ %p\n' | sort -rn | head -n 1 | cut -d' ' -f2-
}

dump() {
	local label=$1 file
	mkdir -p "$dir"
	file="$dir/wera-pre-$label-$(date +%Y%m%d-%H%M).sql.gz"
	docker exec wera-postgres pg_dump -U wera -d wera | gzip >"$file.part"
	gzip -t "$file.part"
	mv "$file.part" "$file"
	echo "Saved $file ($(du -h "$file" | cut -f1))"
}

prune() {
	local keep
	keep=$(newest)
	find "$dir" -maxdepth 1 -name 'wera-*.sql.gz' -mmin +"$limit_minutes" -print0 |
		while IFS= read -r -d '' f; do
			[[ $f == "$keep" ]] && continue
			rm -- "$f"
			echo "Deleted $(basename "$f"), older than $((days - 1)) days"
		done
}

case "${1:-}" in
--prune)
	# After a month with no deploys the newest backup is about to expire:
	# take a fresh one first, so there is always a backup and none too old.
	last=$(newest)
	if [[ -n $last && -n $(find "$last" -mmin +"$limit_minutes") ]]; then
		dump scheduled
	fi
	prune
	;;
"" | -*)
	echo "usage: $0 <label> | --prune" >&2
	exit 1
	;;
*)
	dump "$1"
	prune
	;;
esac
