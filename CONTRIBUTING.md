# Contributing to sinty-it

Contributions are welcome under the terms in CLA.md.

```sh
git clone https://github.com/singularityos-lab/sinty-it
cd sinty-it
make test
```

- Every path that changes a device needs a test proving what it refuses: a
  command for another device, a replayed check-in, a self-approved wipe, a
  retired device.
- Run `gofmt` and `go vet ./...` before sending a change.
