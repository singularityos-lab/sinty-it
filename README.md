# sinty-it

Device management for Sinty OS: an inventory of the organisation's devices,
commands sent to them, per-group policy and a signed record of every action.
Administrators sign in through [sinty-identity](https://github.com/singularityos-lab/sinty-identity)
and must confirm the sign-in on one of their devices.

One binary holds the server, the administration command line and the device
agent.

## Server

```sh
sinty-it serve -issuer https://id.example.org -client it -secret-file it.secret \
  -accept it-cli,desktop -public https://it.example.org -admins alice,bob
```

The web console is served at the public address. It shows which devices need
attention, the requests waiting for a second administrator, each device's
protection and command history, the policy of every group, the active
enrollment codes and the signed activity log. It needs the
[sintyui](https://github.com/singularityos-lab/sintyui) files, by default from
`/usr/share/sintyui`.

The state directory (`/var/lib/sinty-it`) holds:

| File | Content |
| --- | --- |
| `state.json` | devices, commands, enrollment codes, policies |
| `command.key` | Ed25519 key that signs every command |
| `audit.key` | Ed25519 key that signs the activity log |
| `audit.log` | hash-chained, signed activity log |

`sinty-it verify-audit` checks the whole chain offline and names the first
entry that was changed, removed or reordered.

## Commands

| Command | On the device |
| --- | --- |
| `lock` | locks every user session through logind |
| `logout` | ends every user session |
| `reboot` | restarts through logind |
| `update` | asks the update service to check and download |
| `sync-policy` | reports that the policy arrives with every check-in |
| `wipe` | discards the data encryption keys with `sintykey wipe-var`, then restarts |

A `wipe` waits until a second administrator approves it; whoever asked for it
cannot approve it.

Each command carries the device it is for, a sequence number and an expiry,
signed with `command.key`. The agent refuses a command for another device, a
number it has already seen, an expired command or a bad signature. A command
sent again after a lost answer returns the stored result instead of running
twice.

## Agent

```sh
sinty-it agent enroll -server https://it.example.org -token sit_...
systemctl enable --now sinty-it-agent
```

Enrollment codes are created in the console or with `sinty-it token`, and
work for a set number of devices within a set time.

On Sinty OS a device is enrolled from Settings, Organization. Settings talks
to `sinty-it agent bus`, started on demand on the system bus as
`dev.sinty.ITAgent1`, which asks polkit (`dev.sinty.it.enroll`,
`dev.sinty.it.leave`) for an administrator before it enrolls the device,
starts the agent, or takes the device out of management. The server field
also takes an enrollment link, `https://it.example.org/enroll?token=sit_...`. The agent keeps an
Ed25519 key in `/var/lib/sinty-it-agent` and signs every check-in together
with a clock that must move forward, so a recorded check-in cannot be
replayed. It reports:

- the system version and update channel;
- whether the data is encrypted and the keys are sealed to the TPM;
- whether the firewall is on;
- the free space.

Outside Sinty OS, `/etc/sinty-it/agent.json` names the files and sockets of
the system services:

```json
{"update_socket": "/run/updated.sock", "firewall": "/var/lib/sinty-firewalld/state.json"}
```

## Command line

```sh
sinty-it login -server https://it.example.org -issuer https://id.example.org
sinty-it devices
sinty-it run DEVICE lock
sinty-it run DEVICE wipe
sinty-it approve COMMAND
sinty-it policy office updates.channel=stable screen.lock-after=300
sinty-it audit
```

Output is JSON. Exit status: 0 done, 1 failed, 2 wrong usage, 3 signed out,
4 not found, 5 refused.

## Desktop app

`app/` holds Sinty IT for Sinty OS, built with
[libsingularity](https://github.com/singularityos-lab/libsingularity). It uses
the organization account added in Settings, Online Accounts, which must list
the IT service in the identity provider's discovery document
(`sinty-identity serve -service it=https://it.example.org`) and be accepted by
the server (`-accept desktop`). When the server asks for a device
confirmation, the app opens the sign-in again at that level.

```sh
meson setup _build app
ninja -C _build
```

## Building

```sh
make
make test
sudo make install         # server
sudo make install-agent   # agent
```

## License

GPL-3.0-only, see LICENSE.
