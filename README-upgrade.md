# nut-server upgrade package

This archive is for machines that already have nut-server installed.

What it updates:
- binaries in `/usr/local/bin`
- systemd unit files in `/etc/systemd/system`
- helper upgrade scripts

What it keeps:
- config files under `/etc/nut-server`
- state files under `/var/lib/nut-server`

Upgrade examples:

```bash
sudo ./scripts/upgrade-master.sh
sudo ./scripts/upgrade-slave.sh
```

If the service is already running, the upgrade script restarts it after replacing the binary and unit file.
