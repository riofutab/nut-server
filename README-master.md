# nut-master package

This archive is for machines that only run the master service.

Contents:
- `bin/nut-master`
- `configs/master.example.yaml`
- `systemd/nut-master.service`
- `scripts/install-master.sh`
- `scripts/quick-install-master.sh`
- `scripts/install-online.sh`
- `scripts/upgrade-common.sh`
- `scripts/upgrade-master.sh`

Offline install:

```bash
sudo ./scripts/quick-install-master.sh --token your-token --snmp-target 10.0.0.31
```

Enable TLS only when you pass the normal `--tls-*` options to `install-master.sh` or `quick-install-master.sh`.

Online install from a published release:

```bash
sudo ./scripts/install-online.sh --role master --version v0.1.4 -- --token your-token --snmp-target 10.0.0.31
```

Upgrade an installed master without replacing config or state files:

```bash
sudo ./scripts/upgrade-master.sh
```

To let the master shut down its own host after remote nodes have had time to stop, enable `local_shutdown` in `/etc/nut-server/master.yaml`:

```yaml
local_shutdown:
  enabled: true
  max_wait: "15m"
  emergency_runtime_minutes: 15
```

With this enabled, the master waits for remote nodes first, then shuts down its own machine when the remote nodes finish, the wait expires, or the UPS runtime drops below the emergency threshold. A local slave is not required.

To print successful UPS polls into the service log, set `log_ups_status: true` in `/etc/nut-server/master.yaml`, then restart the service:

```bash
sudo systemctl restart nut-master
sudo journalctl -u nut-master -f
```

The admin status endpoint also returns the latest UPS view:

```bash
curl http://127.0.0.1:9001/status
```
