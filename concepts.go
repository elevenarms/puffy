package main

import "strings"

// concepts maps everyday words to the OpenBSD tools and files that handle
// them. Vector search alone cannot bridge "start a service at boot" to
// rcctl(8): the words never appear together. Each expansion is added to
// the keyword query and boosts the pages it names. It is also a small
// lesson: these are the names to learn.
var concepts = []struct {
	words []string
	pages []string
}{
	{[]string{"service", "services", "daemon", "daemons", "startup", "autostart"},
		[]string{"rcctl", "rc.conf", "rc.d"}},
	{[]string{"sudo", "root", "privileges", "privilege", "superuser", "admin"},
		[]string{"doas", "doas.conf"}},
	{[]string{"firewall", "nat", "forward", "forwarding", "redirect", "port"},
		[]string{"pf.conf", "pfctl"}},
	{[]string{"patch", "patches", "security", "update", "updates", "upgrade"},
		[]string{"syspatch", "sysupgrade", "pkg_add"}},
	{[]string{"install", "package", "packages", "software", "uninstall"},
		[]string{"pkg_add", "pkg_info", "pkg_delete"}},
	{[]string{"user", "users", "account", "accounts"},
		[]string{"useradd", "adduser", "usermod", "passwd"}},
	{[]string{"schedule", "scheduled", "cron", "periodic", "nightly"},
		[]string{"crontab", "cron"}},
	{[]string{"dns", "resolver", "resolve", "nameserver"},
		[]string{"unbound", "unbound.conf", "resolv.conf", "nsd"}},
	{[]string{"web", "website", "http", "https", "webserver"},
		[]string{"httpd", "httpd.conf", "relayd"}},
	{[]string{"certificate", "certificates", "tls", "ssl", "letsencrypt"},
		[]string{"acme-client", "acme-client.conf"}},
	{[]string{"vpn", "wireguard", "tunnel", "ipsec"},
		[]string{"wg", "ifconfig", "iked", "iked.conf"}},
	{[]string{"time", "clock", "ntp"},
		[]string{"ntpd", "ntpd.conf"}},
	{[]string{"network", "ip", "address", "dhcp", "wifi", "wireless", "interface"},
		[]string{"hostname.if", "ifconfig", "dhcpleased", "netstart"}},
	{[]string{"mail", "email", "smtp"},
		[]string{"smtpd", "smtpd.conf"}},
	{[]string{"log", "logs", "logging", "syslog"},
		[]string{"syslogd", "syslog.conf", "newsyslog"}},
	{[]string{"disk", "disks", "partition", "filesystem", "mount", "encrypt"},
		[]string{"disklabel", "fdisk", "mount", "newfs", "bioctl", "fstab"}},
	{[]string{"vm", "virtual", "virtualization", "hypervisor"},
		[]string{"vmd", "vmctl", "vm.conf"}},
	{[]string{"ssh", "remote", "login", "keys"},
		[]string{"sshd", "sshd_config", "ssh"}},
	{[]string{"sandbox", "sandboxing", "restrict", "syscalls"},
		[]string{"pledge", "unveil"}},
	{[]string{"kernel", "tunable", "tunables", "sysctl", "tuning"},
		[]string{"sysctl", "sysctl.conf"}},
	{[]string{"firmware", "driver", "drivers"},
		[]string{"fw_update"}},
	{[]string{"backup", "backups", "dump"},
		[]string{"dump", "restore", "tar", "openrsync"}},
}

// expandQuery returns the OpenBSD page names related to words in query.
func expandQuery(query string) []string {
	seen := map[string]bool{}
	var pages []string
	for _, word := range strings.Fields(strings.ToLower(query)) {
		word = strings.Trim(word, "\"'`,.?()!:;")
		for _, concept := range concepts {
			for _, w := range concept.words {
				if w != word {
					continue
				}
				for _, page := range concept.pages {
					if !seen[page] {
						seen[page] = true
						pages = append(pages, page)
					}
				}
			}
		}
	}
	return pages
}
