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
sudo ./scripts/install-online.sh --role master --version v0.1.2 -- --token your-token --snmp-target 10.0.0.31
```

Upgrade an installed master without replacing config or state files:

```bash
sudo ./scripts/upgrade-master.sh
```
