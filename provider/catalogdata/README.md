# Embedded model catalog

`catalog.json` is the validated offline catalog embedded in every Sol binary.
Runtime refreshes use `https://models.airlock.run/models.json` and retain this
snapshot whenever the endpoint is unavailable or invalid.

Refresh the committed snapshot explicitly before a Sol release:

```sh
go generate ./provider
```

The generator downloads the public catalog, validates the same contract used by
the runtime loader, and writes the snapshot only after validation succeeds.
