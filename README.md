<div align="center">

<img src="cmd/flasher/winres/icon.svg" alt="Flasher" width="96">

# Flasher

A USB and SD card image writer for Linux and Windows.

</div>

## Features

- Linux ISOs, Raspberry Pi and other disk images, written byte for byte
- Compressed images (xz, gzip, zstd, bzip2, zip) streamed with no temporary files
- Windows 10 and 11 ISOs rebuilt as UEFI-bootable USB installers, Secure Boot included
- Everything written is read back and verified
- Only removable drives listed by default, never the system drive
- Plain-language errors for unplugged drives, write protection and damaged downloads
- A desktop app and a command-line tool
- Light and dark themes

## Build from source

You need Go 1.27+. No cgo, so the Windows build cross-compiles from Linux.

```sh
go build ./cmd/flasher                       # Linux desktop app
go build ./cmd/flasher-cli                   # Linux command line
GOOS=windows go build -ldflags=-H=windowsgui ./cmd/flasher
GOOS=windows go build ./cmd/flasher-cli
```

## Licence

[GPL-3.0-or-later](LICENSE)
