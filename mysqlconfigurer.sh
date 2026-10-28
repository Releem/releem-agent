#!/usr/bin/env bash
# mysqlconfigurer.sh - Version 1.26.2
# Compatibility entry point for existing cron jobs and agent commands.
# Old installers download only this file, so fetch the implementation if absent.
releem_configurer_dir="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)" || exit 1
releem_configurer_script="$releem_configurer_dir/releem.sh"
if [ ! -f "$releem_configurer_script" ]; then
    releem_configurer_tmp="$(mktemp "$releem_configurer_dir/.releem.XXXXXX")" || exit 1
    if ! curl --fail --silent --show-error --location -o "$releem_configurer_tmp" https://releem.s3.amazonaws.com/v2/releem.sh ||
       ! bash -n "$releem_configurer_tmp" ||
       ! chmod 755 "$releem_configurer_tmp" ||
       ! mv -f "$releem_configurer_tmp" "$releem_configurer_script"; then
        rm -f "$releem_configurer_tmp"
        printf 'Unable to install %s. Retry the agent update.\n' "$releem_configurer_script" >&2
        exit 1
    fi
fi
if [[ "${BASH_SOURCE[0]}" != "$0" ]]; then
    source "$releem_configurer_script"
else
    exec bash "$releem_configurer_script" "$@"
fi
