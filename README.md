<div align="center">

<img src="cmd/flasher/winres/icon.svg" alt="Flasher" width="96">

# Flasher

A USB and SD card image writer for Linux and Windows.

</div>

## Features

- Linux ISOs, Raspberry Pi and other disk images written in "dd" mode
- Compressed images (xz, gzip, zstd, bzip2, zip) flashed without temporary files
- Windows 10 and 11 ISOs rebuilt as USB installers that boot on UEFI with Secure Boot on
- Verification of everything written
- Only removable drives listed by default for safety
- GUI or CLI

## Build from source

You need Go 1.27+. The Windows build cross-compiles from Linux.

```sh
go build ./cmd/flasher                       # Linux desktop app
go build ./cmd/flasher-cli                   # Linux command line
GOOS=windows go build -ldflags=-H=windowsgui ./cmd/flasher
GOOS=windows go build ./cmd/flasher-cli
```

## Licence

[GPL-3.0-or-later](LICENSE)
