package bootstrap

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

const scriptHash = "$6$rounds=500000$abcdefghijklmnop$" + "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"

func TestLaunchScriptSyntaxAndInjectionBoundary(t *testing.T) {
	script, err := Script("debian_12", scriptHash)
	if err != nil {
		t.Fatal(err)
	}
	command := exec.Command("sh", "-n")
	command.Stdin = strings.NewReader(script)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatal("invalid shell", err, string(output))
	}
	if _, err := Script("app_wordpress", scriptHash); err == nil {
		t.Fatal("unsupported app image accepted")
	}
	if _, err := Script("debian_12", scriptHash+"'; echo unsafe"); err == nil {
		t.Fatal("hash injection accepted")
	}
}

func TestLaunchScriptConfigPrecedenceAndRollback(t *testing.T) {
	sshd := "/usr/sbin/sshd"
	if _, err := os.Stat(sshd); err != nil {
		t.Skip("native OpenSSH server binary unavailable")
	}
	keygen, err := exec.LookPath("ssh-keygen")
	if err != nil {
		t.Skip("ssh-keygen unavailable")
	}
	for _, fail := range []bool{false, true} {
		t.Run(map[bool]string{false: "success", true: "restart_failure"}[fail], func(t *testing.T) {
			root := t.TempDir()
			sshDir := filepath.Join(root, "ssh")
			bin := filepath.Join(root, "bin")
			for _, dir := range []string{sshDir, bin} {
				if err := os.Mkdir(dir, 0700); err != nil {
					t.Fatal(err)
				}
			}
			key := filepath.Join(root, "host_key")
			if output, err := exec.Command(keygen, "-q", "-t", "ed25519", "-N", "", "-f", key).CombinedOutput(); err != nil {
				t.Fatal(err, string(output))
			}
			config := filepath.Join(sshDir, "sshd_config")
			original := "HostKey " + key + "\nPasswordAuthentication no\nPermitRootLogin prohibit-password\nAuthenticationMethods publickey\n"
			if err := os.WriteFile(config, []byte(original), 0600); err != nil {
				t.Fatal(err)
			}
			probe := exec.Command(sshd, "-t", "-f", config)
			if output, err := probe.CombinedOutput(); err != nil {
				t.Skip("native OpenSSH config probe unavailable: " + string(output))
			}
			script, err := Script("debian_12", scriptHash)
			if err != nil {
				t.Fatal(err)
			}
			osRelease := filepath.Join(root, "os-release")
			_ = os.WriteFile(osRelease, []byte("ID=debian\n"), 0600)
			// Only paths and platform commands are substituted; the generated config,
			// password pipe, validation and rollback logic execute without host writes.
			wrapper := filepath.Join(bin, "sshd-fixture")
			wrapperBody := "#!/bin/sh\nif [ \"$#\" = 1 ] && [ \"$1\" = -t ]; then exec " + sshd + " -t -f '" + config + "'; fi\nexec " + sshd + " \"$@\"\n"
			_ = os.WriteFile(wrapper, []byte(wrapperBody), 0700)
			script = strings.NewReplacer("/etc/os-release", osRelease, "/etc/ssh", sshDir, "/var/lib/aws-panel", filepath.Join(root, "marker"), "/usr/sbin/sshd", wrapper).Replace(script)
			fake := map[string]string{
				"id":        "#!/bin/sh\nprintf '0\\n'\n",
				"chown":     "#!/bin/sh\nexit 0\n",
				"chpasswd":  "#!/bin/sh\n[ \"$1\" = -e ] || exit 1\ncat > \"$FIXTURE_HASH_FILE\"\n",
				"systemctl": "#!/bin/sh\nif [ \"$1\" = show ]; then printf 'loaded\\n'; exit 0; fi\n[ \"$1\" = reload-or-restart ] || exit 1\n[ \"$FIXTURE_RESTART_FAIL\" != true ]\n",
			}
			for name, body := range fake {
				_ = os.WriteFile(filepath.Join(bin, name), []byte(body), 0700)
			}
			hashFile := filepath.Join(root, "password-input")
			command := exec.Command("sh")
			command.Stdin = strings.NewReader(script)
			command.Env = append(os.Environ(), "PATH="+bin+":/usr/bin:/bin", "FIXTURE_HASH_FILE="+hashFile, "FIXTURE_RESTART_FAIL="+map[bool]string{true: "true", false: "false"}[fail])
			output, runErr := command.CombinedOutput()
			if fail != (runErr != nil) {
				t.Fatal("unexpected script outcome", runErr, string(output))
			}
			if strings.Contains(string(output), scriptHash) {
				t.Fatal("password hash leaked to startup log")
			}
			hashed, err := os.ReadFile(hashFile)
			if err != nil || string(hashed) != "root:"+scriptHash+"\n" {
				t.Fatal("password was not supplied through encrypted stdin", err)
			}
			saved, err := os.ReadFile(config)
			if err != nil {
				t.Fatal(err)
			}
			if fail {
				if string(saved) != original {
					t.Fatal("failed SSH reload did not restore configuration")
				}
				return
			}
			effective, err := exec.Command(sshd, "-T", "-f", config, "-C", "user=root,host=localhost,addr=127.0.0.1").CombinedOutput()
			if err != nil {
				t.Fatal(err, string(effective))
			}
			for _, line := range []string{"permitrootlogin yes", "passwordauthentication yes", "authenticationmethods any"} {
				if !strings.Contains(string(effective), line+"\n") {
					t.Fatal("root login override did not take precedence", line)
				}
			}
		})
	}
}
