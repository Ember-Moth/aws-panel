// Package instanceinit renders fixed initialization for supported Linux images.
package instanceinit

import (
	"errors"
	"github.com/Ember-Moth/aws-panel/internal/auth/unixpassword"
	"strings"
)

func RootPassword(hash string) (string, error) {
	if !unixpassword.ValidHash(hash) {
		return "", errors.New("invalid password hash")
	}
	return strings.Replace(rootPasswordScript, "@PASSWORD_HASH@", hash, 1), nil
}

// Password material is never placed in command-line arguments or shell traces.
// The launch script runs as root after the image's cloud-init SSH defaults.
const rootPasswordScript = `#!/bin/sh
set +x
set -eu
umask 077
[ "$(id -u)" = 0 ]
. /etc/os-release
case "$ID" in debian|ubuntu|amzn) ;; *) exit 1 ;; esac
command -v chpasswd >/dev/null
command -v systemctl >/dev/null
sshd=/usr/sbin/sshd
config=/etc/ssh/sshd_config
[ -x "$sshd" ] && [ -f "$config" ]
if [ "$(systemctl show -p LoadState --value ssh.service 2>/dev/null)" = loaded ]; then
  unit=ssh.service
elif [ "$(systemctl show -p LoadState --value sshd.service 2>/dev/null)" = loaded ]; then
  unit=sshd.service
else
  exit 1
fi
candidate=$(mktemp /etc/ssh/.aws-panel-sshd.XXXXXX)
backup=/etc/ssh/sshd_config.aws-panel-before-password
applied=0
complete=0
cleanup() {
  code=$?
  trap - EXIT HUP INT TERM
  rm -f "$candidate"
  if [ "$applied" = 1 ] && [ "$complete" != 1 ]; then
    cp -p "$backup" "$config"
    if command -v restorecon >/dev/null 2>&1; then restorecon -F "$config" || true; fi
    systemctl reload-or-restart "$unit" >/dev/null 2>&1 || true
  fi
  exit "$code"
}
trap cleanup EXIT
trap 'exit 1' HUP INT TERM
{
  printf '%s\n' '# AWS Panel root-password-v1' 'PermitRootLogin yes' 'PasswordAuthentication yes' 'AuthenticationMethods any'
  cat "$config"
} > "$candidate"
"$sshd" -t -f "$candidate"
effective=$("$sshd" -T -f "$candidate" -C user=root,host=localhost,addr=127.0.0.1)
printf '%s\n' "$effective" | grep -qx 'permitrootlogin yes'
printf '%s\n' "$effective" | grep -qx 'passwordauthentication yes'
printf '%s\n' "$effective" | grep -qx 'authenticationmethods any'
printf '%s\n' 'root:@PASSWORD_HASH@' | chpasswd -e
cp -p "$config" "$backup"
chmod 600 "$candidate"
chown root:root "$candidate"
applied=1
mv "$candidate" "$config"
if command -v restorecon >/dev/null 2>&1; then restorecon -F "$config"; fi
"$sshd" -t
systemctl reload-or-restart "$unit"
complete=1
mkdir -p /var/lib/aws-panel
printf '%s\n' 'root-password-v1 configured' > /var/lib/aws-panel/root-login-configured
printf '%s\n' 'AWS Panel: root password authentication configured'
`
