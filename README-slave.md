# nut-slave package

This archive is for machines that only run the slave service.

Contents:
- `bin/nut-slave`
- `configs/slave.example.yaml`
- `systemd/nut-slave.service`
- `scripts/install-slave.sh`
- `scripts/quick-install-slave.sh`
- `scripts/install-online.sh`
- `scripts/upgrade-common.sh`
- `scripts/upgrade-slave.sh`

Offline install:

```bash
sudo ./scripts/quick-install-slave.sh --node-id slave-01 --master-addr 10.0.0.10:9000 --token your-token
```

Enable TLS only when you pass the normal `--tls-*` options to `install-slave.sh` or `quick-install-slave.sh`.

Online install from a published release:

```bash
sudo ./scripts/install-online.sh --role slave --version v0.1.2 -- --node-id slave-01 --master-addr 10.0.0.10:9000 --token your-token
```

Upgrade an installed slave without replacing config or state files:

```bash
sudo ./scripts/upgrade-slave.sh
```
